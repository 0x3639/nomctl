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
	ID          int64     `json:"id"`
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
	archiveName, err := ArchiveName(hostGOOS, hostGOARCH)
	if err != nil {
		return res, err
	}
	reqReleaseID := releaseArchiveIdentity(release, archiveName)

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

	if pending, err := loadPendingDeployState(binPath); err != nil {
		return res, err
	} else if pending != nil {
		if err := checkPendingService(binPath, pending, cfg.OrchestratorService); err != nil {
			return res, err
		}
		switch pending.Phase {
		case phaseCutover, phaseRestored:
			return res, fmt.Errorf("unresolved orchestrator rollback from failed release %s (previous binary retained at %s; state %s): run \"sudo nomctl orchestrator recover\" before deploying another release", pending.ReleaseTag, pending.PreviousStaged, deployStatePath(binPath))
		case phasePublishPrevious:
			previousPath, err := completePendingPreviousPublish(cfg, binPath, pending)
			if err != nil {
				return res, err
			}
			if pending.matchesRelease(client.Repo(), release.Tag, reqReleaseID) {
				res = DeployResult{
					Repo:           client.Repo(),
					Tag:            release.Tag,
					BinaryPath:     binPath,
					PreviousBinary: previousPath,
					UnitPath:       unitPathFor(cfg),
					BinarySHA256:   pending.CandidateSHA,
				}
				logx.Success(fmt.Sprintf("%s %s already running; published previous binary to %s", cfg.OrchestratorService, release.Tag, previousPath))
				return res, nil
			}
		default:
			return res, fmt.Errorf("unknown pending orchestrator deploy state %q in %s", pending.Phase, deployStatePath(binPath))
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
	var previousSHA string
	keepPreviousStaged := false
	if hasExistingBin {
		previousStaged, err = stageCopy(binPath, binPath)
		if err != nil {
			return res, err
		}
		defer func() {
			if !keepPreviousStaged && previousStaged != "" {
				_ = os.Remove(previousStaged)
			}
		}()
		previousSHA, err = fileSHA256Hex(previousStaged)
		if err != nil {
			return res, fmt.Errorf("hash staged previous binary %s: %w", previousStaged, err)
		}
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
	}

	// Persist transaction intent BEFORE stopping the service or replacing the
	// live binary so a later disk-full error or crash cannot drop rollback state.
	txState := pendingDeployState{
		Phase:          phaseCutover,
		Service:        cfg.OrchestratorService,
		Repo:           client.Repo(),
		ReleaseTag:     release.Tag,
		ReleaseID:      reqReleaseID,
		CandidateSHA:   binSHA,
		PreviousStaged: previousStaged,
		PreviousSHA:    previousSHA,
		WasRunning:     installed && wasRunning,
	}
	if err := writePendingState(binPath, txState); err != nil {
		return res, fmt.Errorf("record orchestrator deploy transaction before cutover: %w", err)
	}

	if installed {
		slog.Info("Stopping " + cfg.OrchestratorService + "…")
		if err := stopService(cfg.OrchestratorService); err != nil {
			err = recoverService(err)
			if rmErr := removePendingDeployState(binPath); rmErr != nil {
				err = errors.Join(err, rmErr)
			}
			return res, err
		}
	}

	slog.Info("Installing orchestrator binary at " + binPath + "…")
	if err := os.Rename(staged, binPath); err != nil {
		cause := recoverService(fmt.Errorf("install orchestrator binary: %w", err))
		if rmErr := removePendingDeployState(binPath); rmErr != nil {
			cause = errors.Join(cause, rmErr)
		}
		return res, cause
	}

	unitCreated := false
	unitPath := unitPathFor(cfg)
	configCreated := false
	rollbackBinary := func(cause error, stopFirst bool) error {
		keepUnresolved := func(err error) error {
			if previousStaged != "" {
				keepPreviousStaged = true
			}
			// txState (phase=cutover) was already persisted before cutover; do not
			// clear it while rollback remains unresolved.
			return fmt.Errorf("%w (run \"sudo nomctl orchestrator recover\" to finish rollback; state %s)", err, deployStatePath(binPath))
		}
		if stopFirst && (installed || unitCreated) {
			if stopErr := stopService(cfg.OrchestratorService); stopErr != nil {
				cause = errors.Join(cause, fmt.Errorf("stop %s during rollback: %w", cfg.OrchestratorService, stopErr))
			}
			// Independently confirm whether the unit reached a stopped state,
			// even if stopService returned a transient post-stop query error.
			st, stErr := serviceStatus(cfg.OrchestratorService)
			if stErr != nil || !st.Stopped() {
				var stateErr error
				if stErr != nil {
					stateErr = fmt.Errorf("confirm %s stopped during rollback (candidate binary may still be running): %w", cfg.OrchestratorService, stErr)
				} else {
					stateErr = fmt.Errorf("%s is still %s during rollback; refusing to replace running candidate binary", cfg.OrchestratorService, st)
				}
				if previousStaged != "" {
					stateErr = fmt.Errorf("%w (previous binary retained at %s)", stateErr, previousStaged)
				}
				return keepUnresolved(errors.Join(cause, stateErr))
			}
		}
		if previousStaged != "" {
			if restoreErr := os.Rename(previousStaged, binPath); restoreErr != nil {
				return keepUnresolved(errors.Join(cause, fmt.Errorf("restore previous orchestrator binary at %s (retained at %s): %w", binPath, previousStaged, restoreErr)))
			}
			previousStaged = ""
		} else if err := os.Remove(binPath); err != nil && !errors.Is(err, os.ErrNotExist) {
			return keepUnresolved(errors.Join(cause, fmt.Errorf("remove failed orchestrator binary %s: %w", binPath, err)))
		}
		txState.Phase = phaseRestored
		_ = writePendingState(binPath, txState)
		if installed && wasRunning {
			slog.Info("Restarting previous " + cfg.OrchestratorService + " after failure…")
			if startErr := startService(cfg.OrchestratorService); startErr != nil {
				return errors.Join(cause, fmt.Errorf("restart restored %s failed (run \"sudo nomctl orchestrator recover\" to retry starting): %w", cfg.OrchestratorService, startErr))
			}
		}
		if rmErr := removePendingDeployState(binPath); rmErr != nil {
			cause = errors.Join(cause, fmt.Errorf("rollback complete, but clearing pending state %s failed (run \"sudo nomctl orchestrator recover\" to clean up): %w", deployStatePath(binPath), rmErr))
		}
		return cause
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
		txState.Phase = phasePublishPrevious
		pubStateErr := writePendingState(binPath, txState)
		previousPath = binPath + ".previous"
		if err := os.Rename(previousStaged, previousPath); err != nil {
			keepPreviousStaged = true
			pubErr := fmt.Errorf("orchestrator %s is running, but saving previous binary to %s failed (previous binary retained at %s; resolve the blocker and run \"sudo nomctl orchestrator recover\" or re-run the same release): %w", release.Tag, previousPath, previousStaged, err)
			return res, errors.Join(pubStateErr, pubErr)
		}
		previousStaged = ""
		if rmErr := removePendingDeployState(binPath); rmErr != nil {
			return res, errors.Join(pubStateErr, fmt.Errorf("orchestrator %s is running and %s was published, but clearing pending state %s failed (run \"sudo nomctl orchestrator recover\" to finish cleanup): %w", release.Tag, previousPath, deployStatePath(binPath), rmErr))
		}
	} else if rmErr := removePendingDeployState(binPath); rmErr != nil {
		return res, fmt.Errorf("orchestrator %s is running, but clearing pending state %s failed: %w", release.Tag, deployStatePath(binPath), rmErr)
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

const (
	phaseCutover         = "cutover"
	phaseRestored        = "restored"
	phasePublishPrevious = "publish-previous"
)

var (
	writePendingState  = defaultWritePendingDeployState
	removePendingState = os.Remove
)

type pendingDeployState struct {
	Phase          string `json:"phase"`
	Service        string `json:"service"`
	Repo           string `json:"repo"`
	ReleaseTag     string `json:"releaseTag"`
	ReleaseID      string `json:"releaseId"`
	CandidateSHA   string `json:"candidateSha256"`
	PreviousStaged string `json:"previousStaged"`
	PreviousSHA    string `json:"previousSha256"`
	WasRunning     bool   `json:"wasRunning"`
}

func (p *pendingDeployState) matchesRelease(repo, tag, releaseID string) bool {
	return p != nil &&
		p.Repo != "" && p.Repo == repo &&
		p.ReleaseTag != "" && p.ReleaseTag == tag &&
		p.ReleaseID != "" && p.ReleaseID == releaseID
}

func releaseArchiveIdentity(release Release, archiveName string) string {
	var parts []string
	if release.ID != 0 {
		parts = append(parts, fmt.Sprintf("release:%d", release.ID))
	}
	for _, a := range release.Assets {
		if a.Name == archiveName {
			parts = append(parts, fmt.Sprintf("archive:%d:%s:%d:%s", a.ID, a.Name, a.Size, strings.TrimSpace(a.Digest)))
		}
		if a.Name == checksumName {
			parts = append(parts, fmt.Sprintf("checksums:%d:%d:%s", a.ID, a.Size, strings.TrimSpace(a.Digest)))
		}
	}
	sort.Strings(parts)
	return strings.Join(parts, "|")
}

func deployStatePath(binPath string) string {
	return binPath + ".pending.json"
}

func checkPendingService(binPath string, pending *pendingDeployState, configuredService string) error {
	if pending == nil {
		return nil
	}
	if strings.TrimSpace(pending.Service) == "" {
		return fmt.Errorf("pending orchestrator deploy state %s is missing original service identity", deployStatePath(binPath))
	}
	if pending.Service != configuredService {
		return fmt.Errorf("pending orchestrator deploy state %s belongs to service %q, not configured service %q; re-run with NOMCTL_ORCHESTRATOR_SERVICE=%s", deployStatePath(binPath), pending.Service, configuredService, pending.Service)
	}
	return nil
}

func loadPendingDeployState(binPath string) (*pendingDeployState, error) {
	statePath := deployStatePath(binPath)
	st, err := os.Lstat(statePath)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("inspect pending orchestrator deploy state %s: %w", statePath, err)
	}
	if st.Mode()&os.ModeSymlink != 0 || !st.Mode().IsRegular() {
		return nil, fmt.Errorf("pending orchestrator deploy state %s must be a regular file", statePath)
	}
	raw, err := os.ReadFile(statePath)
	if err != nil {
		return nil, fmt.Errorf("read pending orchestrator deploy state %s: %w", statePath, err)
	}
	var state pendingDeployState
	if err := json.Unmarshal(raw, &state); err != nil {
		return nil, fmt.Errorf("parse pending orchestrator deploy state %s: %w", statePath, err)
	}
	return &state, nil
}

func defaultWritePendingDeployState(binPath string, state pendingDeployState) error {
	statePath := deployStatePath(binPath)
	if st, err := os.Lstat(statePath); err == nil {
		if st.Mode()&os.ModeSymlink != 0 || !st.Mode().IsRegular() {
			return fmt.Errorf("pending orchestrator deploy state %s must be a regular file", statePath)
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	raw, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return err
	}
	raw = append(raw, '\n')
	tmp, err := os.CreateTemp(filepath.Dir(binPath), ".nomctl-pending-")
	if err != nil {
		return fmt.Errorf("record pending orchestrator deploy state: %w", err)
	}
	tmpPath := tmp.Name()
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpPath)
		return fmt.Errorf("record pending orchestrator deploy state: %w", err)
	}
	if _, err := tmp.Write(raw); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpPath)
		return fmt.Errorf("record pending orchestrator deploy state: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpPath)
		return fmt.Errorf("record pending orchestrator deploy state: %w", err)
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("record pending orchestrator deploy state: %w", err)
	}
	if err := os.Rename(tmpPath, statePath); err != nil {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("record pending orchestrator deploy state %s: %w", statePath, err)
	}
	return nil
}

func removePendingDeployState(binPath string) error {
	err := removePendingState(deployStatePath(binPath))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}

func validateRetainedPreviousPath(binPath, retainedPath string) (string, error) {
	cleanRetained := filepath.Clean(strings.TrimSpace(retainedPath))
	if !filepath.IsAbs(cleanRetained) || filepath.Dir(cleanRetained) != filepath.Dir(binPath) {
		return "", fmt.Errorf("retained previous binary %q is outside install directory %s", retainedPath, filepath.Dir(binPath))
	}
	st, err := os.Lstat(cleanRetained)
	if err != nil {
		return "", fmt.Errorf("retained previous binary %s is unavailable: %w", cleanRetained, err)
	}
	if st.Mode()&os.ModeSymlink != 0 || !st.Mode().IsRegular() {
		return "", fmt.Errorf("retained previous binary %s must be a regular file", cleanRetained)
	}
	return cleanRetained, nil
}

func fileSHA256Hex(path string) (string, error) {
	st, err := os.Lstat(path)
	if err != nil {
		return "", err
	}
	if st.Mode()&os.ModeSymlink != 0 || !st.Mode().IsRegular() {
		return "", fmt.Errorf("orchestrator binary %s must be a regular file", path)
	}
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer func() { _ = f.Close() }()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func completePendingPreviousPublish(cfg config.Config, binPath string, pending *pendingDeployState) (string, error) {
	if err := checkPendingService(binPath, pending, cfg.OrchestratorService); err != nil {
		return "", err
	}
	if pending.CandidateSHA == "" {
		return "", fmt.Errorf("pending .previous state %s is missing candidate SHA-256", deployStatePath(binPath))
	}
	liveSHA, err := fileSHA256Hex(binPath)
	if err != nil {
		return "", fmt.Errorf("verify running orchestrator binary %s before publishing .previous: %w", binPath, err)
	}
	if liveSHA != pending.CandidateSHA {
		return "", fmt.Errorf("live orchestrator binary %s changed since %s was verified; refusing to publish .previous", binPath, pending.ReleaseTag)
	}
	st, err := serviceStatus(pending.Service)
	if err != nil {
		return "", fmt.Errorf("verify %s is active before publishing .previous: %w", pending.Service, err)
	}
	if st != service.Active {
		return "", fmt.Errorf("%s is %s (expected active); refusing to publish .previous", pending.Service, st)
	}

	previousPath := binPath + ".previous"
	alreadyPublished := false
	if _, err := os.Lstat(pending.PreviousStaged); errors.Is(err, os.ErrNotExist) {
		if pending.PreviousSHA == "" {
			return "", fmt.Errorf("retained previous binary %s is missing and pending state lacks previous SHA-256", pending.PreviousStaged)
		}
		prevSHA, prevErr := fileSHA256Hex(previousPath)
		if prevErr != nil || prevSHA != pending.PreviousSHA {
			return "", fmt.Errorf("retained previous binary %s is missing and %s does not match the expected previous binary", pending.PreviousStaged, previousPath)
		}
		alreadyPublished = true
	}
	if !alreadyPublished {
		retained, err := validateRetainedPreviousPath(binPath, pending.PreviousStaged)
		if err != nil {
			return "", err
		}
		if pending.PreviousSHA != "" {
			stagedSHA, err := fileSHA256Hex(retained)
			if err != nil {
				return "", fmt.Errorf("verify retained previous binary %s: %w", retained, err)
			}
			if stagedSHA != pending.PreviousSHA {
				return "", fmt.Errorf("retained previous binary %s checksum mismatch; refusing to publish .previous", retained)
			}
		}
		if err := os.Rename(retained, previousPath); err != nil {
			return "", fmt.Errorf("finish publishing previous binary to %s (retained at %s): %w", previousPath, retained, err)
		}
	}
	if err := removePendingDeployState(binPath); err != nil {
		return previousPath, fmt.Errorf("published %s, but clearing pending state %s failed: %w", previousPath, deployStatePath(binPath), err)
	}
	return previousPath, nil
}

// CompletePendingPreviousPublish finishes publishing a retained previous binary
// to .previous after a verified release started, without restaging or
// restarting the running orchestrator.
func CompletePendingPreviousPublish(cfg config.Config) (string, error) {
	binPath, err := ValidateBinaryPath(cfg.OrchestratorBinaryPath())
	if err != nil {
		return "", err
	}
	pending, err := loadPendingDeployState(binPath)
	if err != nil {
		return "", err
	}
	if pending == nil || pending.Phase != phasePublishPrevious {
		return "", fmt.Errorf("no pending .previous publication found for %s", binPath)
	}
	return completePendingPreviousPublish(cfg, binPath, pending)
}

// RecoverPendingRollback completes an unresolved orchestrator rollback recorded
// in <binary>.pending.json. If the previous binary is not yet restored, it
// confirms the original unit stopped before restoring; if the previous binary
// is already at the live path (including retries after state-cleanup or restart
// failures), it resumes only the remaining restart and state cleanup steps.
func RecoverPendingRollback(cfg config.Config) error {
	binPath, err := ValidateBinaryPath(cfg.OrchestratorBinaryPath())
	if err != nil {
		return err
	}
	pending, err := loadPendingDeployState(binPath)
	if err != nil {
		return err
	}
	if pending == nil || (pending.Phase != phaseCutover && pending.Phase != phaseRestored) {
		return fmt.Errorf("no pending rollback found for %s", binPath)
	}
	if err := checkPendingService(binPath, pending, cfg.OrchestratorService); err != nil {
		return err
	}

	alreadyRestored := false
	switch pending.Phase {
	case phaseRestored:
		if pending.PreviousSHA != "" {
			liveSHA, err := fileSHA256Hex(binPath)
			if err != nil || liveSHA != pending.PreviousSHA {
				return fmt.Errorf("pending state %s marks rollback restored, but %s does not match the expected previous binary", deployStatePath(binPath), binPath)
			}
		} else if _, err := os.Lstat(binPath); !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("pending state %s marks failed install removed, but %s still exists", deployStatePath(binPath), binPath)
		}
		alreadyRestored = true
	case phaseCutover:
		if pending.PreviousStaged != "" {
			if _, err := os.Lstat(pending.PreviousStaged); errors.Is(err, os.ErrNotExist) {
				if pending.PreviousSHA != "" {
					if liveSHA, shaErr := fileSHA256Hex(binPath); shaErr == nil && liveSHA == pending.PreviousSHA {
						alreadyRestored = true
					}
				}
				if !alreadyRestored {
					return fmt.Errorf("retained previous binary %s is missing and %s does not match the expected previous binary", pending.PreviousStaged, binPath)
				}
			}
		} else if _, err := os.Lstat(binPath); errors.Is(err, os.ErrNotExist) {
			alreadyRestored = true
		}
	}

	var stopErr error
	if !alreadyRestored {
		// Verify binPath still holds the failed candidate before overwriting or
		// removing it, so recovery never clobbers a manually reconciled binary.
		if _, statErr := os.Lstat(binPath); statErr == nil {
			if pending.CandidateSHA == "" {
				return fmt.Errorf("pending rollback state %s is missing candidate SHA-256; refusing to modify %s", deployStatePath(binPath), binPath)
			}
			liveSHA, shaErr := fileSHA256Hex(binPath)
			if shaErr != nil {
				return fmt.Errorf("inspect live orchestrator binary %s before rollback: %w", binPath, shaErr)
			}
			if liveSHA != pending.CandidateSHA {
				return fmt.Errorf("live orchestrator binary %s no longer matches failed candidate %s; refusing to overwrite or remove manually changed binary (remove %s if you already reconciled manually)", binPath, pending.ReleaseTag, deployStatePath(binPath))
			}
		} else if !errors.Is(statErr, os.ErrNotExist) {
			return fmt.Errorf("inspect live orchestrator binary %s before rollback: %w", binPath, statErr)
		}
		retained := ""
		if pending.PreviousStaged != "" {
			retained, err = validateRetainedPreviousPath(binPath, pending.PreviousStaged)
			if err != nil {
				return err
			}
			if pending.PreviousSHA != "" {
				stagedSHA, err := fileSHA256Hex(retained)
				if err != nil {
					return fmt.Errorf("verify retained previous binary %s: %w", retained, err)
				}
				if stagedSHA != pending.PreviousSHA {
					return fmt.Errorf("retained previous binary %s checksum mismatch; refusing rollback", retained)
				}
			}
		}
		if err := stopService(pending.Service); err != nil {
			stopErr = fmt.Errorf("stop %s during rollback recovery: %w", pending.Service, err)
		}
		st, stErr := serviceStatus(pending.Service)
		if stErr != nil || !st.Stopped() {
			if stErr != nil {
				return errors.Join(stopErr, fmt.Errorf("confirm %s stopped during rollback recovery: %w", pending.Service, stErr))
			}
			return errors.Join(stopErr, fmt.Errorf("%s is still %s; refusing to restore %s", pending.Service, st, binPath))
		}
		if retained != "" {
			if err := os.Rename(retained, binPath); err != nil {
				return errors.Join(stopErr, fmt.Errorf("restore previous orchestrator binary at %s from %s: %w", binPath, retained, err))
			}
		} else if err := os.Remove(binPath); err != nil && !errors.Is(err, os.ErrNotExist) {
			return errors.Join(stopErr, fmt.Errorf("remove failed orchestrator binary %s: %w", binPath, err))
		}
		pending.Phase = phaseRestored
		_ = writePendingState(binPath, *pending)
	}

	if pending.WasRunning && pending.PreviousSHA != "" {
		st, stErr := serviceStatus(pending.Service)
		if stErr != nil || st != service.Active {
			if err := startService(pending.Service); err != nil {
				return errors.Join(stopErr, fmt.Errorf("restart recovered %s (binary restored; re-run \"sudo nomctl orchestrator recover\" to retry starting): %w", pending.Service, err))
			}
			if st, stErr = serviceStatus(pending.Service); stErr != nil || st != service.Active {
				if stErr != nil {
					return errors.Join(stopErr, fmt.Errorf("verify recovered %s is active: %w", pending.Service, stErr))
				}
				return errors.Join(stopErr, fmt.Errorf("recovered %s is %s after restart", pending.Service, st))
			}
		}
	}

	if err := removePendingDeployState(binPath); err != nil {
		return errors.Join(stopErr, fmt.Errorf("rollback complete, but clearing pending state %s failed: %w", deployStatePath(binPath), err))
	}
	if stopErr != nil {
		slog.Warn("Rollback recovery succeeded after transient stop error: " + stopErr.Error())
	}
	return nil
}

// RecoverPendingDeploy completes whichever pending deployment transaction is
// recorded in <binary>.pending.json (an unresolved rollback or a pending
// .previous binary publication).
func RecoverPendingDeploy(cfg config.Config) (string, error) {
	binPath, err := ValidateBinaryPath(cfg.OrchestratorBinaryPath())
	if err != nil {
		return "", err
	}
	pending, err := loadPendingDeployState(binPath)
	if err != nil {
		return "", err
	}
	if pending == nil {
		return "", fmt.Errorf("no pending orchestrator deployment recovery found for %s", binPath)
	}
	if err := checkPendingService(binPath, pending, cfg.OrchestratorService); err != nil {
		return "", err
	}
	switch pending.Phase {
	case phaseCutover:
		if pending.PreviousStaged != "" && pending.CandidateSHA != "" && pending.PreviousSHA != "" {
			if _, statErr := os.Lstat(pending.PreviousStaged); errors.Is(statErr, os.ErrNotExist) {
				liveSHA, liveErr := fileSHA256Hex(binPath)
				prevSHA, prevErr := fileSHA256Hex(binPath + ".previous")
				if liveErr == nil && liveSHA == pending.CandidateSHA && prevErr == nil && prevSHA == pending.PreviousSHA {
					prevPath, err := completePendingPreviousPublish(cfg, binPath, pending)
					if err != nil {
						return "", err
					}
					return fmt.Sprintf("Published previous orchestrator binary to %s", prevPath), nil
				}
			}
		}
		if err := RecoverPendingRollback(cfg); err != nil {
			return "", err
		}
		return fmt.Sprintf("Recovered pending rollback for %s (%s)", pending.Service, binPath), nil
	case phaseRestored:
		if err := RecoverPendingRollback(cfg); err != nil {
			return "", err
		}
		return fmt.Sprintf("Recovered pending rollback for %s (%s)", pending.Service, binPath), nil
	case phasePublishPrevious:
		prevPath, err := CompletePendingPreviousPublish(cfg)
		if err != nil {
			return "", err
		}
		return fmt.Sprintf("Published previous orchestrator binary to %s", prevPath), nil
	default:
		return "", fmt.Errorf("unknown pending orchestrator deploy state %q in %s", pending.Phase, deployStatePath(binPath))
	}
}
