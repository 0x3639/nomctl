package update

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestVersions(t *testing.T) {
	if _, ok := ParseVersion("dev"); ok {
		t.Error("dev is not a version")
	}
	if p, ok := ParseVersion("v1.2.3-rc1"); !ok || p != [3]int{1, 2, 3} {
		t.Errorf("parse = %v %v", p, ok)
	}
	cases := []struct {
		cand, run string
		newer     bool
	}{
		{"v0.5.0", "0.4.0", true}, {"v0.4.0", "0.4.0", false}, {"v0.4.1", "0.4.0", true}, {"v1.0.0", "0.9.9", true},
		{"v0.3.9", "0.4.0", false}, {"v0.5.0", "dev", false}, {"v0.5.0", "v0.4.0-13-gabc", true},
	}
	for _, c := range cases {
		if got := Newer(c.cand, c.run); got != c.newer {
			t.Errorf("Newer(%s, %s) = %v", c.cand, c.run, got)
		}
	}
	if ArchiveName("v0.4.0", "arm64") != "nomctl_0.4.0_linux_arm64.tar.gz" {
		t.Error("archive name")
	}
	if !SameCommit("abc1234", "abc1234def5678") || SameCommit("abc", "abc1234") || SameCommit("zzz1234", "abc1234def") {
		t.Error("SameCommit")
	}
}

func tarball(t *testing.T, name string, content []byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	_ = tw.WriteHeader(&tar.Header{Name: "LICENSE", Mode: 0o644, Size: 3, Typeflag: tar.TypeReg})
	_, _ = tw.Write([]byte("GPL"))
	_ = tw.WriteHeader(&tar.Header{Name: name, Mode: 0o755, Size: int64(len(content)), Typeflag: tar.TypeReg})
	_, _ = tw.Write(content)
	_ = tw.Close()
	_ = gz.Close()
	return buf.Bytes()
}

func releaseServer(t *testing.T, archive []byte, sumOverride string) *httptest.Server {
	t.Helper()
	name := ArchiveName("v9.9.9", runtime.GOARCH)
	sum := sha256.Sum256(archive)
	sumHex := hex.EncodeToString(sum[:])
	if sumOverride != "" {
		sumHex = sumOverride
	}
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/owner/repo/releases/latest":
			w.Header().Set("Location", "https://github.com/owner/repo/releases/tag/v9.9.9")
			w.WriteHeader(http.StatusFound)
		case strings.HasSuffix(r.URL.Path, "/checksums.txt"):
			_, _ = w.Write([]byte(sumHex + "  " + name + "\n" + "deadbeef  other.tar.gz\n"))
		case strings.HasSuffix(r.URL.Path, "/"+name):
			_, _ = w.Write(archive)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
}

func TestFetchVerifiesAndExtracts(t *testing.T) {
	archive := tarball(t, "nomctl", []byte("#!/bin/sh\necho new\n"))
	srv := releaseServer(t, archive, "")
	defer srv.Close()
	// Point the package at the fake GitHub by rewriting URLs through the transport.
	HTTP = &http.Client{Transport: rewriteHost(srv.URL)}
	defer func() { HTTP = &http.Client{Timeout: 30 * time.Second} }()

	tag, err := LatestTag(context.Background(), "owner/repo")
	if err != nil || tag != "v9.9.9" {
		t.Fatalf("LatestTag = %q, %v", tag, err)
	}
	dir := t.TempDir()
	bin, err := Fetch(context.Background(), "owner/repo", tag, dir)
	if err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(bin)
	if !strings.Contains(string(data), "echo new") {
		t.Errorf("extracted binary wrong: %q", data)
	}
	if st, _ := os.Stat(bin); st.Mode().Perm()&0o111 == 0 {
		t.Error("binary must be executable")
	}

	bad := releaseServer(t, archive, strings.Repeat("0", 64))
	defer bad.Close()
	HTTP = &http.Client{Transport: rewriteHost(bad.URL)}
	if _, err := Fetch(context.Background(), "owner/repo", tag, dir); err == nil || !strings.Contains(err.Error(), "checksum mismatch") {
		t.Errorf("bad checksum must fail: %v", err)
	}
}

type rewriteHost string

func (h rewriteHost) RoundTrip(r *http.Request) (*http.Response, error) {
	u := *r.URL
	u.Scheme = "http"
	u.Host = strings.TrimPrefix(string(h), "http://")
	req := r.Clone(r.Context())
	req.URL = &u
	req.Host = u.Host
	return http.DefaultTransport.RoundTrip(req)
}

func TestReplaceAndRollback(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "nomctl")
	if err := os.WriteFile(target, []byte("old"), 0o755); err != nil {
		t.Fatal(err)
	}
	newBin := filepath.Join(dir, "nomctl.new")
	if err := os.WriteFile(newBin, []byte("new"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := Replace(newBin, target); err != nil {
		t.Fatal(err)
	}
	if data, _ := os.ReadFile(target); string(data) != "new" {
		t.Errorf("target = %q", data)
	}
	if data, _ := os.ReadFile(target + ".previous"); string(data) != "old" {
		t.Errorf("previous = %q", data)
	}
	if _, err := os.Stat(target + ".staged"); !os.IsNotExist(err) {
		t.Error("staged file must be gone")
	}
	if err := Rollback(target); err != nil {
		t.Fatal(err)
	}
	if data, _ := os.ReadFile(target); string(data) != "old" {
		t.Errorf("after rollback = %q", data)
	}
	if err := Rollback(filepath.Join(dir, "nothing")); err == nil {
		t.Error("rollback without a previous binary must fail")
	}
}

func TestCachedCheckAndLines(t *testing.T) {
	archive := tarball(t, "nomctl", []byte("x"))
	srv := releaseServer(t, archive, "")
	defer srv.Close()
	HTTP = &http.Client{Transport: rewriteHost(srv.URL)}
	defer func() { HTTP = &http.Client{Timeout: 30 * time.Second} }()

	now := time.Date(2026, 9, 6, 12, 0, 0, 0, time.UTC)
	cache := filepath.Join(t.TempDir(), "check.json")
	opts := Options{Repo: "owner/repo", CachePath: cache, Now: func() time.Time { return now }}
	c := Run(context.Background(), opts)
	if c.NomctlLatest != "v9.9.9" || c.NomctlErr != "" {
		t.Fatalf("check = %+v", c)
	}
	// Server gone: the cache must answer within the TTL.
	srv.Close()
	now = now.Add(CacheTTL / 2)
	if c2 := Run(context.Background(), opts); c2.NomctlLatest != "v9.9.9" || !c2.CheckedAt.Equal(c.CheckedAt) {
		t.Errorf("cache not used: %+v", c2)
	}
	// A different release repository must not reuse the entry.
	other := opts
	other.Repo = "someone/else"
	if c4 := Run(context.Background(), other); c4.NomctlErr == "" {
		t.Error("cache keyed on the wrong repository was reused")
	}
	now = now.Add(CacheTTL)
	if c3 := Run(context.Background(), opts); c3.NomctlErr == "" {
		t.Error("expired cache must re-check and record the failure")
	}

	lines := Lines(Check{NomctlLatest: "v9.9.9", NodeBranch: "master", NodeRemote: "abcdef1234567"}, "0.4.0", "1234567")
	if len(lines) != 2 || !strings.Contains(lines[0], "nomctl 9.9.9 available (running 0.4.0)") || !strings.Contains(lines[1], "go-zenon master has new commits (deployed 1234567, remote abcdef1)") {
		t.Errorf("lines = %v", lines)
	}
	if got := Lines(Check{NomctlLatest: "v0.4.0", NodeRemote: "abcdef1234567"}, "0.4.0", "abcdef1"); len(got) != 0 {
		t.Errorf("up to date must print nothing: %v", got)
	}
	if got := Lines(Check{NomctlLatest: "v9.9.9"}, "dev", ""); len(got) != 0 {
		t.Errorf("dev build must not claim an update: %v", got)
	}
}

func TestSaveUsesIndependentTemporaryFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "update.json")
	existing := filepath.Join(t.TempDir(), "example.txt")
	if err := os.WriteFile(existing, []byte("example content"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(existing, path+".tmp"); err != nil {
		t.Fatal(err)
	}
	want := Check{CheckedAt: time.Now().UTC(), NomctlLatest: "v1.2.3"}
	save(path, want)
	got, ok := load(path)
	if !ok || got.NomctlLatest != want.NomctlLatest {
		t.Fatalf("cache was not published: %+v", got)
	}
	if data, _ := os.ReadFile(existing); string(data) != "example content" {
		t.Fatal("unrelated temporary-path target changed")
	}
}
