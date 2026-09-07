// Package update checks GitHub for newer nomctl and go-zenon versions and
// replaces the nomctl binary in place. It does what install.sh does, in Go,
// plus an atomic swap and a rollback copy.
package update

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/0x3639/nomctl/internal/execx"
)

// DefaultRepo is the GitHub repository releases are fetched from.
const DefaultRepo = "0x3639/nomctl"

// CachePath stores the last check so status/top/alerts do not hit GitHub
// on every sample. /run is tmpfs, cleared on reboot.
const CachePath = "/run/nomctl/update-check.json"

// CacheTTL is how long a check result is reused.
const CacheTTL = time.Hour

// HTTP is the client used for GitHub requests.
var HTTP = &http.Client{Timeout: 30 * time.Second}

// LatestTag resolves the latest release tag by following GitHub's
// releases/latest redirect, which needs no API token and has no rate limit.
func LatestTag(ctx context.Context, repo string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodHead, fmt.Sprintf("https://github.com/%s/releases/latest", repo), nil)
	if err != nil {
		return "", err
	}
	client := *HTTP
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	res, err := client.Do(req)
	if err != nil {
		return "", err
	}
	_ = res.Body.Close()
	loc := res.Header.Get("Location")
	if res.StatusCode < 300 || res.StatusCode > 399 || loc == "" {
		return "", fmt.Errorf("no release found for %s (HTTP %d)", repo, res.StatusCode)
	}
	tag := loc[strings.LastIndex(loc, "/")+1:]
	if !strings.HasPrefix(tag, "v") {
		return "", fmt.Errorf("unexpected release URL %s", loc)
	}
	return tag, nil
}

// ParseVersion turns "v1.2.3", "1.2.3" or "1.2.3-rc1" into comparable parts.
// ok is false for non-release strings such as "dev" or a git describe.
func ParseVersion(s string) (parts [3]int, ok bool) {
	s = strings.TrimPrefix(strings.TrimSpace(s), "v")
	if i := strings.IndexAny(s, "-+"); i >= 0 {
		s = s[:i]
	}
	fields := strings.Split(s, ".")
	if len(fields) != 3 {
		return parts, false
	}
	for i, f := range fields {
		n, err := strconv.Atoi(f)
		if err != nil || n < 0 {
			return parts, false
		}
		parts[i] = n
	}
	return parts, true
}

// Newer reports whether candidate is a higher release than running. A
// running version that is not a release ("dev") never has an update.
func Newer(candidate, running string) bool {
	c, ok1 := ParseVersion(candidate)
	r, ok2 := ParseVersion(running)
	if !ok1 || !ok2 {
		return false
	}
	for i := range c {
		if c[i] != r[i] {
			return c[i] > r[i]
		}
	}
	return false
}

// ArchiveName is the release asset for a version and architecture.
func ArchiveName(tag, goarch string) string {
	return fmt.Sprintf("nomctl_%s_linux_%s.tar.gz", strings.TrimPrefix(tag, "v"), goarch)
}

// Fetch downloads the release archive and checksums.txt for tag, verifies
// the archive, and extracts the nomctl binary into dir. It returns the path
// of the extracted binary.
func Fetch(ctx context.Context, repo, tag, dir string) (string, error) {
	base := fmt.Sprintf("https://github.com/%s/releases/download/%s/", repo, tag)
	name := ArchiveName(tag, runtime.GOARCH)
	sums, err := get(ctx, base+"checksums.txt")
	if err != nil {
		return "", err
	}
	want := ""
	for _, line := range strings.Split(string(sums), "\n") {
		fields := strings.Fields(line)
		if len(fields) == 2 && fields[1] == name {
			want = fields[0]
		}
	}
	if want == "" {
		return "", fmt.Errorf("%s is not listed in checksums.txt for %s", name, tag)
	}
	archive, err := get(ctx, base+name)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(archive)
	if got := hex.EncodeToString(sum[:]); got != want {
		return "", fmt.Errorf("checksum mismatch for %s: got %s, want %s", name, got, want)
	}
	return extractBinary(archive, dir)
}

func get(ctx context.Context, url string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	res, err := HTTP.Do(req)
	if err != nil {
		return nil, fmt.Errorf("download %s: %w", url, err)
	}
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("download %s: HTTP %d", url, res.StatusCode)
	}
	return io.ReadAll(io.LimitReader(res.Body, 200<<20))
}

// extractBinary pulls the "nomctl" entry out of a tar.gz.
func extractBinary(archive []byte, dir string) (string, error) {
	gz, err := gzip.NewReader(bytes.NewReader(archive))
	if err != nil {
		return "", err
	}
	tr := tar.NewReader(gz)
	for {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return "", errors.New("archive does not contain a nomctl binary")
		}
		if err != nil {
			return "", err
		}
		if filepath.Base(h.Name) != "nomctl" || !h.FileInfo().Mode().IsRegular() {
			continue
		}
		out := filepath.Join(dir, "nomctl.new")
		f, err := os.OpenFile(out, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o755)
		if err != nil {
			return "", err
		}
		if _, err := io.Copy(f, tr); err != nil {
			_ = f.Close()
			return "", err
		}
		if err := f.Close(); err != nil {
			return "", err
		}
		return out, nil
	}
}

// Replace installs newBinary at target atomically, keeping the previous
// binary as target+".previous" for Rollback.
func Replace(newBinary, target string) error {
	previous := target + ".previous"
	if _, err := os.Stat(target); err == nil {
		_ = os.Remove(previous)
		if err := os.Link(target, previous); err != nil {
			// Cross-device or unsupported: fall back to a copy.
			if err := copyFile(target, previous); err != nil {
				return fmt.Errorf("keep previous binary: %w", err)
			}
		}
	}
	staged := target + ".staged"
	if err := copyFile(newBinary, staged); err != nil {
		return err
	}
	if err := os.Rename(staged, target); err != nil {
		_ = os.Remove(staged)
		return fmt.Errorf("install %s: %w", target, err)
	}
	return nil
}

// Rollback restores target+".previous" over target.
func Rollback(target string) error {
	previous := target + ".previous"
	if _, err := os.Stat(previous); err != nil {
		return fmt.Errorf("no previous binary at %s", previous)
	}
	staged := target + ".staged"
	if err := copyFile(previous, staged); err != nil {
		return err
	}
	return os.Rename(staged, target)
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer func() { _ = in.Close() }()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o755)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		_ = out.Close()
		return err
	}
	return out.Close()
}

// --- node (go-zenon) update check --------------------------------------------

// RemoteHead returns the commit at the head of branch in repoURL, bounded
// by ctx.
func RemoteHead(ctx context.Context, repoURL, branch string) (string, error) {
	out, err := execx.New("git", "ls-remote", repoURL, "refs/heads/"+branch).Context(ctx).Output()
	if err != nil {
		return "", err
	}
	fields := strings.Fields(out)
	if len(fields) < 1 || len(fields[0]) < 7 {
		return "", fmt.Errorf("branch %s not found in %s", branch, repoURL)
	}
	return fields[0], nil
}

// SameCommit compares a short or full commit id with a full one.
func SameCommit(short, full string) bool {
	short, full = strings.ToLower(strings.TrimSpace(short)), strings.ToLower(strings.TrimSpace(full))
	return short != "" && len(short) >= 7 && strings.HasPrefix(full, short)
}

// --- cached check ------------------------------------------------------------

// Check is the cached result of the last update check.
type Check struct {
	CheckedAt     time.Time `json:"checked_at"`
	Repo          string    `json:"repo,omitempty"`
	NomctlLatest  string    `json:"nomctl_latest,omitempty"`
	NodeRepo      string    `json:"node_repo,omitempty"`
	NodeBranch    string    `json:"node_branch,omitempty"`
	NodeRemote    string    `json:"node_remote,omitempty"`
	NomctlErr     string    `json:"nomctl_err,omitempty"`
	NodeRemoteErr string    `json:"node_remote_err,omitempty"`
}

// Options for Checker.
type Options struct {
	Repo       string // GitHub slug for nomctl releases
	NodeRepo   string // go-zenon git URL
	NodeBranch string
	CachePath  string
	TTL        time.Duration
	Now        func() time.Time
}

// Run performs (or reuses) a check. Failures are recorded, not returned, so
// callers can show what is known.
func Run(ctx context.Context, opts Options) Check {
	if opts.Now == nil {
		opts.Now = time.Now
	}
	if opts.TTL == 0 {
		opts.TTL = CacheTTL
	}
	if opts.CachePath == "" {
		opts.CachePath = CachePath
	}
	if c, ok := load(opts.CachePath); ok && opts.Now().Sub(c.CheckedAt) < opts.TTL && c.Repo == opts.Repo && c.NodeRepo == opts.NodeRepo && c.NodeBranch == opts.NodeBranch {
		return c
	}
	c := Check{CheckedAt: opts.Now(), Repo: opts.Repo, NodeRepo: opts.NodeRepo, NodeBranch: opts.NodeBranch}
	if tag, err := LatestTag(ctx, opts.Repo); err != nil {
		c.NomctlErr = err.Error()
	} else {
		c.NomctlLatest = tag
	}
	if opts.NodeRepo != "" {
		if head, err := RemoteHead(ctx, opts.NodeRepo, opts.NodeBranch); err != nil {
			c.NodeRemoteErr = err.Error()
		} else {
			c.NodeRemote = head
		}
	}
	save(opts.CachePath, c)
	return c
}

func load(path string) (Check, bool) {
	var c Check
	data, err := os.ReadFile(path)
	if err != nil {
		return c, false
	}
	if err := json.Unmarshal(data, &c); err != nil {
		return c, false
	}
	return c, true
}

func save(path string, c Check) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return
	}
	data, err := json.Marshal(c)
	if err != nil {
		return
	}
	tmp := path + ".tmp"
	if os.WriteFile(tmp, data, 0o644) == nil {
		_ = os.Rename(tmp, path)
	}
}

// Lines renders the "Update" lines for status/top: nothing when up to date
// or unknown.
func Lines(c Check, runningVersion, nodeCommit string) []string {
	var out []string
	if c.NomctlLatest != "" && Newer(c.NomctlLatest, runningVersion) {
		out = append(out, fmt.Sprintf("nomctl %s available (running %s): sudo nomctl upgrade", strings.TrimPrefix(c.NomctlLatest, "v"), runningVersion))
	}
	if c.NodeRemote != "" && nodeCommit != "" && !SameCommit(nodeCommit, c.NodeRemote) {
		out = append(out, fmt.Sprintf("go-zenon %s has new commits (deployed %s, remote %s): sudo nomctl deploy", c.NodeBranch, shortCommit(nodeCommit), shortCommit(c.NodeRemote)))
	}
	return out
}

func shortCommit(c string) string {
	if len(c) > 7 {
		return c[:7]
	}
	return c
}
