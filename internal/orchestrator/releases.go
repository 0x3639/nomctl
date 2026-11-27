package orchestrator

import (
	"archive/zip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"syscall"
	"time"

	"github.com/0x3639/nomctl/internal/backup"
	"github.com/0x3639/nomctl/internal/config"
	"github.com/0x3639/nomctl/internal/logx"
	"github.com/0x3639/nomctl/internal/service"
)

const (
	// DefaultRepo is the default GitHub repository for orchestrator releases.
	DefaultRepo    = config.DefaultOrchestratorRepo
	checksumName   = "SHA256CHECKSUMS.txt"
	maxArchiveSize = 200 << 20
	maxChecksumSz  = 1 << 20
)

// RepoChoices are the preset GitHub accounts offered in interactive menus.
var RepoChoices = []struct{ Label, Repo, URL string }{
	{"0x3639 (default)", "0x3639/orchestrator", "https://github.com/0x3639/orchestrator"},
	{"HyperCore-Team", "HyperCore-Team/orchestrator", "https://github.com/HyperCore-Team/orchestrator"},
}

var (
	hostGOOS   = runtime.GOOS
	hostGOARCH = runtime.GOARCH
	reGHPart   = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,99}$`)
)

// Asset is one downloadable GitHub release asset.
type Asset struct {
	ID     int64  `json:"id"`
	Name   string `json:"name"`
	Size   int64  `json:"size"`
	Digest string `json:"digest"`
}

// Release is one published GitHub release.
type Release struct {
	Tag         string    `json:"tag_name"`
	Name        string    `json:"name"`
	PublishedAt time.Time `json:"published_at"`
	CreatedAt   time.Time `json:"created_at"`
	Draft       bool      `json:"draft"`
	Prerelease  bool      `json:"prerelease"`
	Assets      []Asset   `json:"assets"`
}

func (r Release) effectiveTime() time.Time {
	if !r.PublishedAt.IsZero() {
		return r.PublishedAt
	}
	return r.CreatedAt
}

// NormalizeRepo converts a GitHub account name ("0x3639"), an "owner/repo"
// slug, or a GitHub URL ("https://github.com/0x3639/orchestrator/") into a
// validated "owner/repo" slug. Empty input defaults to DefaultRepo.
func NormalizeRepo(input string) (string, error) {
	s := strings.TrimSpace(input)
	if s == "" {
		return DefaultRepo, nil
	}
	if strings.ContainsAny(s, " \t\r\n?#") {
		return "", fmt.Errorf("invalid GitHub repository %q", input)
	}
	if strings.Contains(s, "://") {
		u, err := url.Parse(s)
		if err != nil {
			return "", fmt.Errorf("invalid GitHub repository %q: %w", input, err)
		}
		if (u.Scheme != "https" && u.Scheme != "http") || !strings.EqualFold(u.Host, "github.com") {
			return "", fmt.Errorf("orchestrator releases must come from github.com (got %q)", input)
		}
		s = strings.TrimPrefix(u.Path, "/")
	} else if strings.HasPrefix(strings.ToLower(s), "github.com/") {
		s = s[len("github.com/"):]
	}
	s = strings.TrimSuffix(strings.Trim(s, "/"), ".git")
	if s == "" {
		return "", fmt.Errorf("invalid GitHub repository %q", input)
	}
	parts := strings.Split(s, "/")
	var owner, repo string
	switch len(parts) {
	case 1:
		owner, repo = parts[0], "orchestrator"
	case 2:
		owner, repo = parts[0], parts[1]
	default:
		return "", fmt.Errorf("invalid GitHub repository %q: use an account name, owner/repo, or https://github.com/owner/repo", input)
	}
	for _, part := range []string{owner, repo} {
		if !reGHPart.MatchString(part) || part == "." || part == ".." || strings.Contains(part, "..") {
			return "", fmt.Errorf("invalid GitHub repository %q", input)
		}
	}
	return owner + "/" + repo, nil
}

// RepoURL returns the HTTPS GitHub URL for a normalized owner/repo slug.
func RepoURL(repo string) string {
	return "https://github.com/" + repo
}

// Client talks to GitHub's releases API for one validated repository.
type Client struct {
	repo    string
	token   string
	apiBase string
	http    *http.Client
}

// NewClient constructs a GitHub releases client for repo (owner, owner/repo,
// or GitHub URL). Empty repo uses DefaultRepo.
func NewClient(repo, token string) (*Client, error) {
	normalized, err := NormalizeRepo(repo)
	if err != nil {
		return nil, err
	}
	return &Client{
		repo:    normalized,
		token:   strings.TrimSpace(token),
		apiBase: "https://api.github.com",
		http: &http.Client{
			Timeout: 10 * time.Minute,
			CheckRedirect: func(req *http.Request, via []*http.Request) error {
				if len(via) >= 10 {
					return fmt.Errorf("too many download redirects")
				}
				if req.URL.Scheme != "https" {
					return fmt.Errorf("refusing non-HTTPS download redirect")
				}
				req.Header.Del("Authorization")
				return nil
			},
		},
	}, nil
}

// Repo returns the normalized owner/repo slug.
func (c *Client) Repo() string { return c.repo }

// List returns all published non-draft releases sorted newest first.
func (c *Client) List(ctx context.Context) ([]Release, error) {
	slog.Info("Listing available orchestrator releases from " + c.repo + "…")
	var releases []Release
	for page := 1; page <= 100; page++ {
		endpoint := fmt.Sprintf("%s/repos/%s/releases?per_page=100&page=%d", c.apiBase, c.repo, page)
		res, err := c.get(ctx, endpoint, "application/vnd.github+json")
		if err != nil {
			return nil, err
		}
		var batch []Release
		err = json.NewDecoder(io.LimitReader(res.Body, 8<<20)).Decode(&batch)
		_ = res.Body.Close()
		if err != nil {
			return nil, fmt.Errorf("read releases from %s: %w", c.repo, err)
		}
		for _, r := range batch {
			if !r.Draft && r.Tag != "" {
				releases = append(releases, r)
			}
		}
		if len(batch) < 100 {
			sort.SliceStable(releases, func(i, j int) bool {
				return releases[i].effectiveTime().After(releases[j].effectiveTime())
			})
			if len(releases) == 0 {
				return nil, fmt.Errorf("no published releases found in %s", c.repo)
			}
			return releases, nil
		}
	}
	return nil, fmt.Errorf("too many releases in %s", c.repo)
}

// SelectRelease picks tag from releases, defaulting to the most recent release
// (releases[0]) when tag is empty.
func SelectRelease(releases []Release, tag string) (Release, error) {
	if len(releases) == 0 {
		return Release{}, errors.New("no published releases available")
	}
	tag = strings.TrimSpace(tag)
	if tag == "" {
		return releases[0], nil
	}
	for _, r := range releases {
		if r.Tag == tag {
			return r, nil
		}
	}
	return Release{}, fmt.Errorf("published release %q not found", tag)
}

// ArchiveName returns the expected orchestrator release zip filename for
// goos/goarch.
func ArchiveName(goos, goarch string) (string, error) {
	if goos != "linux" || (goarch != "amd64" && goarch != "arm64") {
		return "", fmt.Errorf("orchestrator releases support only linux/amd64 and linux/arm64 (got %s/%s)", goos, goarch)
	}
	return "orchestrator-linux-" + goarch + ".zip", nil
}

// ParseChecksums parses SHA256CHECKSUMS.txt in either one-entry-per-line form
// or the legacy single-line form ("<hash1> <file1> <hash2> <file2>").
func ParseChecksums(data []byte) (map[string]string, error) {
	fields := strings.Fields(string(data))
	if len(fields) == 0 || len(fields)%2 != 0 {
		return nil, fmt.Errorf("invalid %s format", checksumName)
	}
	sums := make(map[string]string, len(fields)/2)
	for i := 0; i < len(fields); i += 2 {
		digest := strings.ToLower(fields[i])
		decoded, err := hex.DecodeString(digest)
		if err != nil || len(decoded) != sha256.Size {
			return nil, fmt.Errorf("invalid SHA-256 digest %q in %s", fields[i], checksumName)
		}
		name := strings.TrimPrefix(strings.TrimPrefix(fields[i+1], "*"), "./")
		if name == "" {
			return nil, fmt.Errorf("empty filename in %s", checksumName)
		}
		if _, dup := sums[name]; dup {
			return nil, fmt.Errorf("duplicate checksum entry for %s in %s", name, checksumName)
		}
		sums[name] = digest
	}
	return sums, nil
}

func (r Release) findAsset(name string) (Asset, bool, error) {
	var found Asset
	for _, a := range r.Assets {
		if a.Name != name {
			continue
		}
		if found.ID != 0 || a.ID <= 0 {
			return Asset{}, false, fmt.Errorf("invalid or duplicate asset %s in %s", name, r.Tag)
		}
		found = a
	}
	return found, found.ID != 0, nil
}

func parseAssetDigest(digest string) (string, error) {
	digest = strings.TrimSpace(digest)
	if digest == "" {
		return "", nil
	}
	hexPart, ok := strings.CutPrefix(strings.ToLower(digest), "sha256:")
	if !ok {
		return "", fmt.Errorf("unsupported asset digest %q", digest)
	}
	decoded, err := hex.DecodeString(hexPart)
	if err != nil || len(decoded) != sha256.Size {
		return "", fmt.Errorf("invalid asset SHA-256 digest %q", digest)
	}
	return hexPart, nil
}

// Fetch downloads the platform zip archive for release into dir, verifies its
// SHA-256 digest before extracting, and returns the extracted binary path and
// the extracted binary's SHA-256 hex digest.
func (c *Client) Fetch(ctx context.Context, release Release, dir, goos, goarch string) (binaryPath, binarySHA string, err error) {
	archiveName, err := ArchiveName(goos, goarch)
	if err != nil {
		return "", "", err
	}
	zipAsset, ok, err := release.findAsset(archiveName)
	if err != nil {
		return "", "", err
	}
	if !ok {
		return "", "", fmt.Errorf("release %s in %s is missing %s", release.Tag, c.repo, archiveName)
	}

	apiDigest, err := parseAssetDigest(zipAsset.Digest)
	if err != nil {
		return "", "", err
	}
	var expectedSHA string
	sumAsset, hasSums, err := release.findAsset(checksumName)
	if err != nil {
		return "", "", err
	}
	switch {
	case hasSums:
		slog.Info("Downloading " + checksumName + " for " + release.Tag + "…")
		sumPath := filepath.Join(dir, checksumName)
		if err := c.downloadAsset(ctx, sumAsset.ID, sumPath, maxChecksumSz); err != nil {
			return "", "", err
		}
		rawSums, err := os.ReadFile(sumPath)
		if err != nil {
			return "", "", err
		}
		sums, err := ParseChecksums(rawSums)
		if err != nil {
			return "", "", err
		}
		want, ok := sums[archiveName]
		if !ok {
			return "", "", fmt.Errorf("%s is not listed in %s for %s", archiveName, checksumName, release.Tag)
		}
		if apiDigest != "" && apiDigest != want {
			return "", "", fmt.Errorf("SHA-256 mismatch between %s and GitHub release metadata for %s", checksumName, archiveName)
		}
		expectedSHA = want
	case apiDigest != "":
		expectedSHA = apiDigest
	default:
		return "", "", fmt.Errorf("release %s in %s has no %s or SHA-256 digest to verify %s", release.Tag, c.repo, checksumName, archiveName)
	}

	slog.Info("Downloading " + archiveName + " for " + release.Tag + "…")
	zipPath := filepath.Join(dir, archiveName)
	if err := c.downloadAsset(ctx, zipAsset.ID, zipPath, maxArchiveSize); err != nil {
		return "", "", err
	}
	slog.Info("Verifying SHA-256 of " + archiveName + "…")
	gotSHA, err := backup.SHA256File(zipPath)
	if err != nil {
		return "", "", err
	}
	if gotSHA != expectedSHA {
		return "", "", fmt.Errorf("SHA-256 mismatch for %s: got %s, want %s", archiveName, gotSHA, expectedSHA)
	}
	slog.Info("Extracting verified orchestrator binary…")
	extracted, err := extractZipBinary(zipPath, dir, goarch)
	if err != nil {
		return "", "", err
	}
	binSHA, err := backup.SHA256File(extracted)
	if err != nil {
		return "", "", err
	}
	return extracted, binSHA, nil
}

func (c *Client) get(ctx context.Context, endpoint, accept string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", accept)
	req.Header.Set("User-Agent", "nomctl")
	if apiURL, err := url.Parse(c.apiBase); err == nil && req.URL.Host == apiURL.Host {
		req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
		if c.token != "" {
			req.Header.Set("Authorization", "Bearer "+c.token)
		}
	}
	res, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("GitHub request failed: %w", err)
	}
	if res.StatusCode != http.StatusOK {
		_ = res.Body.Close()
		if res.StatusCode == http.StatusNotFound {
			return nil, fmt.Errorf("GitHub HTTP 404 for %s (check repository name, release tag, or NOMCTL_GITHUB_TOKEN for private repos)", c.repo)
		}
		if res.StatusCode == http.StatusUnauthorized || res.StatusCode == http.StatusForbidden {
			return nil, fmt.Errorf("GitHub HTTP %d for %s (check API rate limits or NOMCTL_GITHUB_TOKEN)", res.StatusCode, c.repo)
		}
		return nil, fmt.Errorf("GitHub download failed: HTTP %d", res.StatusCode)
	}
	return res, nil
}

func (c *Client) downloadAsset(ctx context.Context, assetID int64, dst string, limit int64) error {
	endpoint := fmt.Sprintf("%s/repos/%s/releases/assets/%d", c.apiBase, c.repo, assetID)
	res, err := c.get(ctx, endpoint, "application/octet-stream")
	if err != nil {
		return err
	}
	defer func() { _ = res.Body.Close() }()
	f, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	n, err := io.Copy(f, io.LimitReader(res.Body, limit+1))
	closeErr := f.Close()
	if err != nil {
		_ = os.Remove(dst)
		return err
	}
	if n > limit {
		_ = os.Remove(dst)
		return fmt.Errorf("%s exceeds download size limit", filepath.Base(dst))
	}
	return closeErr
}

func extractZipBinary(zipPath, dir, goarch string) (string, error) {
	zr, err := zip.OpenReader(zipPath)
	if err != nil {
		return "", fmt.Errorf("open release zip: %w", err)
	}
	defer func() { _ = zr.Close() }()

	wantPrimary := "orchestrator-linux-" + goarch
	var chosen *zip.File
	for _, f := range zr.File {
		name := strings.ReplaceAll(f.Name, "\\", "/")
		if strings.HasPrefix(name, "/") || strings.Contains(name, "..") {
			return "", fmt.Errorf("unsafe path in release archive: %q", f.Name)
		}
		clean := strings.TrimPrefix(filepath.ToSlash(filepath.Clean(name)), "./")
		if clean == "." || clean == "" {
			continue
		}
		if f.FileInfo().IsDir() {
			continue
		}
		mode := f.Mode()
		if mode&os.ModeSymlink != 0 {
			return "", fmt.Errorf("symlink entry rejected in release archive: %q", f.Name)
		}
		if mode != 0 && !mode.IsRegular() {
			return "", fmt.Errorf("non-regular entry rejected in release archive: %q", f.Name)
		}
		base := filepath.Base(clean)
		if base != wantPrimary && base != "orchestrator" {
			continue
		}
		if chosen != nil {
			return "", fmt.Errorf("release archive contains multiple orchestrator binaries (%q and %q)", chosen.Name, f.Name)
		}
		chosen = f
	}
	if chosen == nil {
		return "", fmt.Errorf("release archive does not contain %s or orchestrator", wantPrimary)
	}
	rc, err := chosen.Open()
	if err != nil {
		return "", err
	}
	defer func() { _ = rc.Close() }()

	target := filepath.Join(dir, "orchestrator.verified")
	out, err := os.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o755)
	if err != nil {
		return "", err
	}
	n, copyErr := io.Copy(out, io.LimitReader(rc, maxArchiveSize+1))
	closeErr := out.Close()
	if copyErr != nil {
		_ = os.Remove(target)
		return "", copyErr
	}
	if closeErr != nil {
		_ = os.Remove(target)
		return "", closeErr
	}
	if n == 0 {
		_ = os.Remove(target)
		return "", errors.New("extracted orchestrator binary is empty")
	}
	if n > maxArchiveSize {
		_ = os.Remove(target)
		return "", errors.New("extracted orchestrator binary exceeds size limit")
	}
	return target, nil
}

// DeployResult summarizes a completed orchestrator install or upgrade.
type DeployResult struct {
	Repo           string
	Tag            string
	BinaryPath     string
	PreviousBinary string
	UnitPath       string
	UnitCreated    bool
	ConfigCreated  bool
	BinarySHA256   string
}

// Install deploys the selected orchestrator release: downloads and verifies
// before stopping, stops the service if installed, atomically installs the
// binary to /usr/local/bin/orchestrator (or configured path), ensures the
// state directory, default config.json, and systemd service unit exist, then
// starts/restarts the orchestrator.
func Install(ctx context.Context, cfg config.Config, client *Client, release Release) (DeployResult, error) {
	return deployRelease(ctx, cfg, client, release, false)
}

// Upgrade upgrades an existing orchestrator installation to the selected
// release: requires an existing unit and regular binary, downloads and
// verifies before stopping, stops the orchestrator, preserves the previous
// binary as .previous, installs the new binary, and restarts the service.
func Upgrade(ctx context.Context, cfg config.Config, client *Client, release Release) (DeployResult, error) {
	return deployRelease(ctx, cfg, client, release, true)
}

func deployRelease(ctx context.Context, cfg config.Config, client *Client, release Release, upgradeOnly bool) (DeployResult, error) {
	var res DeployResult
	if client == nil {
		return res, errors.New("release client is required")
	}
	dir, err := ValidateDir(cfg.OrchestratorDir)
	if err != nil {
		return res, err
	}
	binPath, err := ValidateBinaryPath(cfg.OrchestratorBinaryPath())
	if err != nil {
		return res, err
	}
	if _, err := ArchiveName(hostGOOS, hostGOARCH); err != nil {
		return res, err
	}

	installed, err := Installed(cfg)
	if err != nil {
		return res, err
	}
	if upgradeOnly && !installed {
		return res, ErrNotInstalled
	}
	if installed {
		execPath, err := unitExecPath(cfg.OrchestratorService)
		if err != nil {
			return res, err
		}
		if execPath != "" && !sameExecutablePath(execPath, binPath) {
			return res, fmt.Errorf("existing %s unit executes %s, not configured install target %s; update the unit or set NOMCTL_INSTALL_DIR/NOMCTL_ORCHESTRATOR_BINARY", cfg.OrchestratorService, execPath, binPath)
		}
	}

	if !upgradeOnly {
		if err := preflightInstallSetup(cfg, dir, installed); err != nil {
			return res, err
		}
	}

	hasExistingBin := false
	if st, err := os.Lstat(binPath); err == nil {
		if st.Mode()&os.ModeSymlink != 0 || !st.Mode().IsRegular() {
			return res, fmt.Errorf("existing orchestrator binary %s must be a regular file", binPath)
		}
		hasExistingBin = true
	} else if !errors.Is(err, os.ErrNotExist) {
		return res, err
	} else if upgradeOnly {
		return res, fmt.Errorf("orchestrator binary not found at %s; run orchestrator install first", binPath)
	}

	tmpDir, err := os.MkdirTemp("", "nomctl-orchestrator-")
	if err != nil {
		return res, err
	}
	defer func() { _ = os.RemoveAll(tmpDir) }()

	verifiedBin, binSHA, err := client.Fetch(ctx, release, tmpDir, hostGOOS, hostGOARCH)
	if err != nil {
		return res, err
	}
	if err := ctx.Err(); err != nil {
		return res, err
	}

	if err := os.MkdirAll(filepath.Dir(binPath), 0o755); err != nil {
		return res, fmt.Errorf("prepare install directory %s: %w", filepath.Dir(binPath), err)
	}

	// Stage both the new binary and any rollback copy on the target filesystem
	// before stopping the orchestrator.
	staged, err := stageCopy(verifiedBin, binPath)
	if err != nil {
		return res, err
	}
	defer func() { _ = os.Remove(staged) }()

	var previousStaged string
	if hasExistingBin {
		previousStaged, err = stageCopy(binPath, binPath)
		if err != nil {
			return res, err
		}
		defer func() { _ = os.Remove(previousStaged) }()
	}
	if err := ctx.Err(); err != nil {
		return res, err
	}

	finishSignals := protectTransaction()
	defer finishSignals()
	if err := ctx.Err(); err != nil {
		return res, err
	}

	var wasRunning bool
	recoverService := func(cause error) error {
		if installed && wasRunning {
			slog.Info("Restarting previous " + cfg.OrchestratorService + " after failure…")
			cause = errors.Join(cause, startService(cfg.OrchestratorService))
		}
		return cause
	}
	if installed {
		st, err := serviceStatus(cfg.OrchestratorService)
		if err != nil {
			return res, err
		}
		wasRunning = st.Running()
		slog.Info("Stopping " + cfg.OrchestratorService + "…")
		if err := stopService(cfg.OrchestratorService); err != nil {
			return res, recoverService(err)
		}
	}

	slog.Info("Installing orchestrator binary at " + binPath + "…")
	if err := os.Rename(staged, binPath); err != nil {
		return res, recoverService(fmt.Errorf("install orchestrator binary: %w", err))
	}

	unitCreated := false
	unitPath := unitPathFor(cfg)
	configCreated := false
	rollbackBinary := func(cause error, stopFirst bool) error {
		if stopFirst && (installed || unitCreated) {
			_ = stopService(cfg.OrchestratorService)
		}
		if previousStaged != "" {
			if restoreErr := os.Rename(previousStaged, binPath); restoreErr != nil {
				cause = errors.Join(cause, fmt.Errorf("restore previous orchestrator binary at %s: %w", binPath, restoreErr))
			} else {
				previousStaged = ""
			}
		}
		return recoverService(cause)
	}

	if !upgradeOnly {
		if _, err := EnsureDataDir(dir); err != nil {
			return res, rollbackBinary(err, false)
		}
		configCreated, _, err = EnsureDefaultConfig(dir)
		if err != nil {
			return res, rollbackBinary(err, false)
		}
		unitCreated, unitPath, err = EnsureService(cfg)
		if err != nil {
			return res, rollbackBinary(err, false)
		}
	}

	slog.Info("Starting " + cfg.OrchestratorService + "…")
	if err := startService(cfg.OrchestratorService); err != nil {
		return res, rollbackBinary(fmt.Errorf("failed to start %s after installing %s: %w", cfg.OrchestratorService, release.Tag, err), true)
	}
	if st, err := serviceStatus(cfg.OrchestratorService); err != nil {
		return res, rollbackBinary(fmt.Errorf("cannot verify %s state after start: %w", cfg.OrchestratorService, err), true)
	} else if st != service.Active {
		return res, rollbackBinary(fmt.Errorf("%s is %s after installing %s", cfg.OrchestratorService, st, release.Tag), true)
	}

	// Only publish .previous after the candidate passes startup verification, so
	// an unsuccessful retry can never overwrite a known-good recovery binary.
	previousPath := ""
	if previousStaged != "" {
		previousPath = binPath + ".previous"
		if err := os.Rename(previousStaged, previousPath); err != nil {
			return res, fmt.Errorf("orchestrator %s is running, but saving previous binary to %s failed: %w", release.Tag, previousPath, err)
		}
	}

	res = DeployResult{
		Repo:           client.Repo(),
		Tag:            release.Tag,
		BinaryPath:     binPath,
		PreviousBinary: previousPath,
		UnitPath:       unitPath,
		UnitCreated:    unitCreated,
		ConfigCreated:  configCreated,
		BinarySHA256:   binSHA,
	}
	logx.Success(fmt.Sprintf("%s %s installed at %s and running", cfg.OrchestratorService, release.Tag, binPath))
	return res, nil
}

func preflightInstallSetup(cfg config.Config, dir string, installed bool) error {
	if st, err := os.Lstat(dir); err == nil {
		if st.Mode()&os.ModeSymlink != 0 || !st.IsDir() {
			return fmt.Errorf("orchestrator directory %s must be a real directory", dir)
		}
		cfgPath := ConfigPath(dir)
		if cst, err := os.Lstat(cfgPath); err == nil {
			if cst.Mode()&os.ModeSymlink != 0 || !cst.Mode().IsRegular() {
				return fmt.Errorf("orchestrator config %s must be a regular file", cfgPath)
			}
		} else if !errors.Is(err, os.ErrNotExist) {
			return err
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}

	unitPath := unitPathFor(cfg)
	unitFilePresent := false
	if ust, err := os.Lstat(unitPath); err == nil {
		if ust.Mode()&os.ModeSymlink != 0 || !ust.Mode().IsRegular() {
			return fmt.Errorf("orchestrator unit %s must be a regular file", unitPath)
		}
		unitFilePresent = true
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if !unitFilePresent && !installed {
		if _, err := UnitFile(cfg); err != nil {
			return err
		}
	}
	return nil
}

func stageCopy(src, target string) (string, error) {
	in, err := os.Open(src)
	if err != nil {
		return "", err
	}
	defer func() { _ = in.Close() }()
	f, err := os.CreateTemp(filepath.Dir(target), ".nomctl-orchestrator-")
	if err != nil {
		return "", err
	}
	path := f.Name()
	if err := f.Chmod(0o755); err != nil {
		_ = f.Close()
		_ = os.Remove(path)
		return "", err
	}
	if _, err := io.Copy(f, in); err != nil {
		_ = f.Close()
		_ = os.Remove(path)
		return "", err
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		_ = os.Remove(path)
		return "", err
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(path)
		return "", err
	}
	return path, nil
}

func protectTransaction() func() {
	signals := make(chan os.Signal, 1)
	done := make(chan struct{})
	stopped := make(chan struct{})
	signal.Notify(signals, os.Interrupt, syscall.SIGTERM)
	go func() {
		defer close(stopped)
		for {
			select {
			case sig := <-signals:
				slog.Warn("Received " + sig.String() + "; waiting for orchestrator install/upgrade to finish safely…")
			case <-done:
				return
			}
		}
	}()
	return func() {
		signal.Stop(signals)
		close(done)
		<-stopped
	}
}
