package orchestrator

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/0x3639/nomctl/internal/config"
	"github.com/0x3639/nomctl/internal/service"
)

func TestNormalizeRepo(t *testing.T) {
	cases := []struct {
		in      string
		want    string
		wantErr bool
	}{
		{"", "0x3639/orchestrator", false},
		{"0x3639", "0x3639/orchestrator", false},
		{"HyperCore-Team", "HyperCore-Team/orchestrator", false},
		{"0x3639/orchestrator", "0x3639/orchestrator", false},
		{"https://github.com/0x3639/orchestrator/", "0x3639/orchestrator", false},
		{"https://github.com/HyperCore-Team/orchestrator.git", "HyperCore-Team/orchestrator", false},
		{"github.com/edgepillar/orchestrator", "edgepillar/orchestrator", false},
		{"https://gitlab.com/0x3639/orchestrator", "", true},
		{"https://github.com/0x3639/orchestrator/extra", "", true},
		{"../evil", "", true},
		{"0x3639/orch?foo=1", "", true},
		{"0x3639/orch bar", "", true},
	}
	for _, tc := range cases {
		got, err := NormalizeRepo(tc.in)
		if (err != nil) != tc.wantErr {
			t.Errorf("NormalizeRepo(%q): err=%v, wantErr=%v", tc.in, err, tc.wantErr)
			continue
		}
		if got != tc.want {
			t.Errorf("NormalizeRepo(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestParseChecksumsBothFormats(t *testing.T) {
	h1 := strings.Repeat("a", 64)
	h2 := strings.Repeat("b", 64)

	multiLine := h1 + "  orchestrator-linux-amd64.zip\n" + h2 + "  orchestrator-linux-arm64.zip\n"
	sums, err := ParseChecksums([]byte(multiLine))
	if err != nil {
		t.Fatalf("multi-line parse failed: %v", err)
	}
	if sums["orchestrator-linux-amd64.zip"] != h1 || sums["orchestrator-linux-arm64.zip"] != h2 {
		t.Fatalf("unexpected sums: %+v", sums)
	}

	singleLine := h1 + " orchestrator-linux-amd64.zip " + h2 + " orchestrator-linux-arm64.zip\n"
	sums, err = ParseChecksums([]byte(singleLine))
	if err != nil {
		t.Fatalf("legacy single-line parse failed: %v", err)
	}
	if sums["orchestrator-linux-amd64.zip"] != h1 || sums["orchestrator-linux-arm64.zip"] != h2 {
		t.Fatalf("unexpected legacy sums: %+v", sums)
	}

	for _, bad := range []string{"", "not-a-hash file.zip", h1 + " a.zip " + h1 + " a.zip", h1} {
		if _, err := ParseChecksums([]byte(bad)); err == nil {
			t.Errorf("expected error for %q", bad)
		}
	}
}

func makeZip(t *testing.T, files map[string]struct {
	body []byte
	mode os.FileMode
}) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for name, entry := range files {
		hdr := &zip.FileHeader{Name: name, Method: zip.Deflate}
		hdr.SetMode(entry.mode)
		w, err := zw.CreateHeader(hdr)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write(entry.body); err != nil {
			t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func shaHex(b []byte) string {
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}

type mockReleaseServer struct {
	server   *httptest.Server
	releases []Release
	assets   map[int64][]byte
}

func newMockReleaseServer(t *testing.T, releases []Release, assets map[int64][]byte) *mockReleaseServer {
	t.Helper()
	m := &mockReleaseServer{releases: releases, assets: assets}
	m.server = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/releases"):
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(m.releases)
		case strings.Contains(r.URL.Path, "/releases/assets/"):
			for id, body := range m.assets {
				if strings.HasSuffix(r.URL.Path, "/releases/assets/"+strings.TrimSpace(filepath.Base(r.URL.Path))) {
					if filepath.Base(r.URL.Path) == itoa64(id) {
						_, _ = w.Write(body)
						return
					}
				}
			}
			http.NotFound(w, r)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(m.server.Close)
	return m
}

func itoa64(v int64) string {
	b, _ := json.Marshal(v)
	return string(b)
}

func (m *mockReleaseServer) client(t *testing.T, repo string) *Client {
	t.Helper()
	c, err := NewClient(repo, "")
	if err != nil {
		t.Fatal(err)
	}
	c.apiBase = m.server.URL
	c.http = m.server.Client()
	return c
}

func stubDeployHost(t *testing.T, installed bool, status service.State, unitPath string) *[]string {
	t.Helper()
	calls := &[]string{}
	oldStop, oldStart, oldExists, oldLoaded, oldStatus, oldExecPath := stopService, startService, unitExists, unitLoaded, serviceStatus, unitExecPath
	oldWrite, oldReload, oldEnable, oldUnitPath := writeUnit, daemonReload, enableUnit, unitPathFor
	oldGOOS, oldGOARCH := hostGOOS, hostGOARCH

	hostGOOS, hostGOARCH = "linux", "amd64"
	curInstalled := installed
	curStatus := status

	unitExists = func(string) (bool, error) { return curInstalled, nil }
	unitLoaded = func(string) (bool, error) { return curInstalled, nil }
	serviceStatus = func(string) (service.State, error) { return curStatus, nil }
	rootDir := filepath.Dir(unitPath)
	if filepath.Base(rootDir) == "systemd" {
		rootDir = filepath.Dir(rootDir)
	}
	defaultExec := filepath.Join(rootDir, "bin", "orchestrator")
	unitExecPath = func(string) (string, error) { return defaultExec, nil }
	stopService = func(name string) error {
		*calls = append(*calls, "stop "+name)
		curStatus = service.Inactive
		return nil
	}
	startService = func(name string) error {
		*calls = append(*calls, "start "+name)
		curStatus = service.Active
		return nil
	}
	writeUnit = func(path, content string) error {
		*calls = append(*calls, "write-unit "+path)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			return err
		}
		curInstalled = true
		return nil
	}
	daemonReload = func() error { *calls = append(*calls, "daemon-reload"); return nil }
	enableUnit = func(unit string) error { *calls = append(*calls, "enable "+unit); return nil }
	unitPathFor = func(config.Config) string { return unitPath }

	t.Cleanup(func() {
		stopService, startService, unitExists, unitLoaded, serviceStatus, unitExecPath = oldStop, oldStart, oldExists, oldLoaded, oldStatus, oldExecPath
		writeUnit, daemonReload, enableUnit, unitPathFor = oldWrite, oldReload, oldEnable, oldUnitPath
		hostGOOS, hostGOARCH = oldGOOS, oldGOARCH
	})
	return calls
}

func TestListAndSelectReleasesDefaultsToMostRecent(t *testing.T) {
	older := time.Date(2026, 3, 7, 18, 0, 0, 0, time.UTC)
	newer := time.Date(2026, 11, 26, 11, 55, 0, 0, time.UTC)
	srv := newMockReleaseServer(t, []Release{
		{Tag: "v0.0.9a-alphanet", PublishedAt: older},
		{Tag: "v0.0.10-draft", PublishedAt: newer.Add(time.Hour), Draft: true},
		{Tag: "v0.0.10-rc1", PublishedAt: newer, Prerelease: true},
	}, nil)
	c := srv.client(t, "0x3639")
	if c.Repo() != "0x3639/orchestrator" {
		t.Fatalf("repo = %q", c.Repo())
	}
	rels, err := c.List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(rels) != 2 || rels[0].Tag != "v0.0.10-rc1" || rels[1].Tag != "v0.0.9a-alphanet" {
		t.Fatalf("unexpected release order: %+v", rels)
	}
	picked, err := SelectRelease(rels, "")
	if err != nil || picked.Tag != "v0.0.10-rc1" {
		t.Fatalf("SelectRelease default = %+v, %v", picked, err)
	}
	picked, err = SelectRelease(rels, "v0.0.9a-alphanet")
	if err != nil || picked.Tag != "v0.0.9a-alphanet" {
		t.Fatalf("SelectRelease explicit = %+v, %v", picked, err)
	}
	if _, err := SelectRelease(rels, "v9.9.9"); err == nil {
		t.Fatal("expected error for unknown release tag")
	}
}

func TestInstallFreshCreatesBinaryConfigAndUnit(t *testing.T) {
	tmp := t.TempDir()
	binDir := filepath.Join(tmp, "bin")
	orchDir := filepath.Join(tmp, ".orchestrator")
	unitPath := filepath.Join(tmp, "systemd", "orchestrator.service")

	binPayload := []byte("#!/bin/sh\necho v0.0.10-rc1\n")
	zipBytes := makeZip(t, map[string]struct {
		body []byte
		mode os.FileMode
	}{
		"orchestrator-linux-amd64": {body: binPayload, mode: 0o755},
	})
	sums := shaHex(zipBytes) + "  orchestrator-linux-amd64.zip\n"
	rel := Release{
		Tag: "v0.0.10-rc1",
		Assets: []Asset{
			{ID: 10, Name: "orchestrator-linux-amd64.zip", Digest: "sha256:" + shaHex(zipBytes)},
			{ID: 11, Name: "SHA256CHECKSUMS.txt"},
		},
	}
	srv := newMockReleaseServer(t, []Release{rel}, map[int64][]byte{10: zipBytes, 11: []byte(sums)})
	calls := stubDeployHost(t, false, service.NotFound, unitPath)

	cfg := config.Default()
	cfg.InstallDir = binDir
	cfg.OrchestratorDir = orchDir

	res, err := Install(context.Background(), cfg, srv.client(t, "0x3639/orchestrator"), rel)
	if err != nil {
		t.Fatal(err)
	}
	if !res.UnitCreated || !res.ConfigCreated {
		t.Fatalf("expected unit and config creation: %+v", res)
	}
	gotBin, err := os.ReadFile(filepath.Join(binDir, "orchestrator"))
	if err != nil || !bytes.Equal(gotBin, binPayload) {
		t.Fatalf("installed binary mismatch: %v", err)
	}
	unitBytes, err := os.ReadFile(unitPath)
	if err != nil || !strings.Contains(string(unitBytes), "ExecStart=:\""+filepath.Join(binDir, "orchestrator")+"\"") || !strings.Contains(string(unitBytes), "Environment=\"HOME="+tmp+"\"") {
		t.Fatalf("unit content unexpected: %s (%v)", string(unitBytes), err)
	}
	cfgInfo, err := os.Stat(ConfigPath(orchDir))
	if err != nil || cfgInfo.Mode().Perm() != 0o600 {
		t.Fatalf("config.json mode = %v, err = %v", cfgInfo, err)
	}
	dirInfo, err := os.Stat(orchDir)
	if err != nil || dirInfo.Mode().Perm() != 0o700 {
		t.Fatalf("orchDir mode = %v, err = %v", dirInfo, err)
	}
	wantCalls := "write-unit " + unitPath + ",daemon-reload,enable orchestrator.service,start orchestrator"
	if strings.Join(*calls, ",") != wantCalls {
		t.Fatalf("calls = %v, want %s", *calls, wantCalls)
	}
}

func TestUpgradeStopsReplacesAndPreservesPreviousAndUnit(t *testing.T) {
	tmp := t.TempDir()
	binDir := filepath.Join(tmp, "bin")
	orchDir := filepath.Join(tmp, "orchestrator")
	unitPath := filepath.Join(tmp, "systemd", "orchestrator.service")

	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatal(err)
	}
	oldBin := []byte("old-orchestrator-binary")
	binPath := filepath.Join(binDir, "orchestrator")
	if err := os.WriteFile(binPath, oldBin, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(unitPath), 0o755); err != nil {
		t.Fatal(err)
	}
	customUnit := "[Service]\nEnvironment=ORCHESTRATOR_PRODUCER_PASSPHRASE=custom\n"
	if err := os.WriteFile(unitPath, []byte(customUnit), 0o644); err != nil {
		t.Fatal(err)
	}

	newBin := []byte("new-orchestrator-binary")
	zipBytes := makeZip(t, map[string]struct {
		body []byte
		mode os.FileMode
	}{
		"orchestrator-linux-amd64": {body: newBin, mode: 0o755},
	})
	sums := shaHex(zipBytes) + " orchestrator-linux-amd64.zip"
	rel := Release{
		Tag: "v0.0.10-rc1",
		Assets: []Asset{
			{ID: 20, Name: "orchestrator-linux-amd64.zip"},
			{ID: 21, Name: "SHA256CHECKSUMS.txt"},
		},
	}
	srv := newMockReleaseServer(t, []Release{rel}, map[int64][]byte{20: zipBytes, 21: []byte(sums)})
	calls := stubDeployHost(t, true, service.Active, unitPath)

	cfg := config.Default()
	cfg.InstallDir = binDir
	cfg.OrchestratorDir = orchDir

	res, err := Upgrade(context.Background(), cfg, srv.client(t, "0x3639/orchestrator"), rel)
	if err != nil {
		t.Fatal(err)
	}
	if res.PreviousBinary != binPath+".previous" {
		t.Fatalf("PreviousBinary = %q", res.PreviousBinary)
	}
	if got, _ := os.ReadFile(binPath); !bytes.Equal(got, newBin) {
		t.Fatalf("new binary not installed")
	}
	if prev, _ := os.ReadFile(binPath + ".previous"); !bytes.Equal(prev, oldBin) {
		t.Fatalf("previous binary not preserved")
	}
	if u, _ := os.ReadFile(unitPath); string(u) != customUnit {
		t.Fatalf("existing unit file was modified")
	}
	if strings.Join(*calls, ",") != "stop orchestrator,start orchestrator" {
		t.Fatalf("calls = %v", *calls)
	}
}

func TestDeployRefusesBadChecksumBeforeStoppingService(t *testing.T) {
	tmp := t.TempDir()
	binDir := filepath.Join(tmp, "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatal(err)
	}
	oldBin := []byte("original-binary")
	binPath := filepath.Join(binDir, "orchestrator")
	if err := os.WriteFile(binPath, oldBin, 0o755); err != nil {
		t.Fatal(err)
	}
	zipBytes := makeZip(t, map[string]struct {
		body []byte
		mode os.FileMode
	}{
		"orchestrator-linux-amd64": {body: []byte("tampered"), mode: 0o755},
	})
	rel := Release{
		Tag: "v0.0.10-rc1",
		Assets: []Asset{
			{ID: 30, Name: "orchestrator-linux-amd64.zip"},
			{ID: 31, Name: "SHA256CHECKSUMS.txt"},
		},
	}
	badSums := strings.Repeat("0", 64) + "  orchestrator-linux-amd64.zip\n"
	srv := newMockReleaseServer(t, []Release{rel}, map[int64][]byte{30: zipBytes, 31: []byte(badSums)})
	calls := stubDeployHost(t, true, service.Active, filepath.Join(tmp, "orchestrator.service"))

	cfg := config.Default()
	cfg.InstallDir = binDir
	cfg.OrchestratorDir = filepath.Join(tmp, "orch")

	if _, err := Upgrade(context.Background(), cfg, srv.client(t, "0x3639/orchestrator"), rel); err == nil || !strings.Contains(err.Error(), "SHA-256 mismatch") {
		t.Fatalf("expected checksum mismatch error, got %v", err)
	}
	if len(*calls) != 0 {
		t.Fatalf("service must not be touched on checksum mismatch: %v", *calls)
	}
	if got, _ := os.ReadFile(binPath); !bytes.Equal(got, oldBin) {
		t.Fatal("existing binary was overwritten after bad checksum")
	}
}

func TestExtractZipRejectsTraversalAndSymlinks(t *testing.T) {
	tmp := t.TempDir()
	traversalZip := filepath.Join(tmp, "traversal.zip")
	if err := os.WriteFile(traversalZip, makeZip(t, map[string]struct {
		body []byte
		mode os.FileMode
	}{
		"../orchestrator-linux-amd64": {body: []byte("evil"), mode: 0o755},
	}), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := extractZipBinary(traversalZip, tmp, "amd64"); err == nil {
		t.Fatal("expected traversal zip to be rejected")
	}

	symlinkZip := filepath.Join(tmp, "symlink.zip")
	if err := os.WriteFile(symlinkZip, makeZip(t, map[string]struct {
		body []byte
		mode os.FileMode
	}{
		"orchestrator-linux-amd64": {body: []byte("/etc/passwd"), mode: os.ModeSymlink | 0o777},
	}), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := extractZipBinary(symlinkZip, tmp, "amd64"); err == nil {
		t.Fatal("expected symlink zip entry to be rejected")
	}
}

func TestUpgradeRefusesWhenNotInstalled(t *testing.T) {
	tmp := t.TempDir()
	srv := newMockReleaseServer(t, nil, nil)
	calls := stubDeployHost(t, false, service.NotFound, filepath.Join(tmp, "orchestrator.service"))
	cfg := config.Default()
	cfg.InstallDir = filepath.Join(tmp, "bin")
	cfg.OrchestratorDir = filepath.Join(tmp, "orch")
	if _, err := Upgrade(context.Background(), cfg, srv.client(t, "0x3639/orchestrator"), Release{Tag: "v1"}); !errors.Is(err, ErrNotInstalled) {
		t.Fatalf("expected ErrNotInstalled, got %v", err)
	}
	if len(*calls) != 0 {
		t.Fatalf("unexpected service calls: %v", *calls)
	}
}

func TestUpgradeRejectsMismatchedUnitBinaryBeforeStopping(t *testing.T) {
	tmp := t.TempDir()
	binDir := filepath.Join(tmp, "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatal(err)
	}
	binPath := filepath.Join(binDir, "orchestrator")
	if err := os.WriteFile(binPath, []byte("v1"), 0o755); err != nil {
		t.Fatal(err)
	}
	srv := newMockReleaseServer(t, nil, nil)
	calls := stubDeployHost(t, true, service.Active, filepath.Join(tmp, "orchestrator.service"))
	unitExecPath = func(string) (string, error) { return "/opt/custom/orchestrator", nil }

	cfg := config.Default()
	cfg.InstallDir = binDir
	cfg.OrchestratorDir = filepath.Join(tmp, ".orchestrator")
	if _, err := Upgrade(context.Background(), cfg, srv.client(t, "0x3639/orchestrator"), Release{Tag: "v2"}); err == nil || !strings.Contains(err.Error(), "/opt/custom/orchestrator") {
		t.Fatalf("expected ExecStart mismatch error, got %v", err)
	}
	if len(*calls) != 0 {
		t.Fatalf("service must not be touched on binary mismatch: %v", *calls)
	}

	// Empty ExecStart must also be rejected before touching the service or binary.
	unitExecPath = func(string) (string, error) { return "", nil }
	if _, err := Upgrade(context.Background(), cfg, srv.client(t, "0x3639/orchestrator"), Release{Tag: "v2"}); err == nil || !strings.Contains(err.Error(), "empty ExecStart") {
		t.Fatalf("expected empty ExecStart error, got %v", err)
	}
	if len(*calls) != 0 {
		t.Fatalf("service must not be touched on empty ExecStart: %v", *calls)
	}

	// Verify defaultUnitExecPath itself fails closed when systemctl reports empty ExecStart.
	fakeBin := filepath.Join(tmp, "fake-systemctl")
	if err := os.MkdirAll(fakeBin, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(fakeBin, "systemctl"), []byte("#!/bin/sh\necho \"\"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", fakeBin+string(os.PathListSeparator)+os.Getenv("PATH"))
	if _, err := defaultUnitExecPath("orchestrator"); err == nil || !strings.Contains(err.Error(), "empty ExecStart") {
		t.Fatalf("expected defaultUnitExecPath to reject empty ExecStart, got %v", err)
	}
}

func TestUpgradeStartFailureRestoresBinaryAndPreservesPreviousOnRetry(t *testing.T) {
	tmp := t.TempDir()
	binDir := filepath.Join(tmp, "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatal(err)
	}
	binPath := filepath.Join(binDir, "orchestrator")
	prevPath := binPath + ".previous"
	if err := os.WriteFile(binPath, []byte("v1-live"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(prevPath, []byte("v0-backup"), 0o755); err != nil {
		t.Fatal(err)
	}

	zipBytes := makeZip(t, map[string]struct {
		body []byte
		mode os.FileMode
	}{
		"orchestrator-linux-amd64": {body: []byte("v2-broken"), mode: 0o755},
	})
	sums := shaHex(zipBytes) + "  orchestrator-linux-amd64.zip\n"
	rel := Release{
		Tag: "v2",
		Assets: []Asset{
			{ID: 40, Name: "orchestrator-linux-amd64.zip"},
			{ID: 41, Name: "SHA256CHECKSUMS.txt"},
		},
	}
	srv := newMockReleaseServer(t, []Release{rel}, map[int64][]byte{40: zipBytes, 41: []byte(sums)})
	calls := stubDeployHost(t, true, service.Active, filepath.Join(tmp, "orchestrator.service"))
	startAttempts := 0
	startService = func(name string) error {
		startAttempts++
		*calls = append(*calls, "start "+name)
		if startAttempts == 1 {
			return errors.New("v2 crashed on start")
		}
		return nil
	}

	cfg := config.Default()
	cfg.InstallDir = binDir
	cfg.OrchestratorDir = filepath.Join(tmp, ".orchestrator")
	if _, err := Upgrade(context.Background(), cfg, srv.client(t, "0x3639/orchestrator"), rel); err == nil {
		t.Fatal("expected start failure")
	}
	if got, _ := os.ReadFile(binPath); string(got) != "v1-live" {
		t.Fatalf("live binary = %q, want v1-live restored", string(got))
	}
	if prev, _ := os.ReadFile(prevPath); string(prev) != "v0-backup" {
		t.Fatalf(".previous = %q, want v0-backup preserved", string(prev))
	}
	if strings.Join(*calls, ",") != "stop orchestrator,start orchestrator,stop orchestrator,start orchestrator" {
		t.Fatalf("calls = %v, want stop, failed start, stop before rollback, recovery start", *calls)
	}
}

func TestUpgradeStopErrorRecoversActiveServiceOnly(t *testing.T) {
	tmp := t.TempDir()
	binDir := filepath.Join(tmp, "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(binDir, "orchestrator"), []byte("v1"), 0o755); err != nil {
		t.Fatal(err)
	}
	zipBytes := makeZip(t, map[string]struct {
		body []byte
		mode os.FileMode
	}{
		"orchestrator-linux-amd64": {body: []byte("v2"), mode: 0o755},
	})
	sums := shaHex(zipBytes) + "  orchestrator-linux-amd64.zip\n"
	rel := Release{
		Tag: "v2",
		Assets: []Asset{
			{ID: 50, Name: "orchestrator-linux-amd64.zip"},
			{ID: 51, Name: "SHA256CHECKSUMS.txt"},
		},
	}
	srv := newMockReleaseServer(t, []Release{rel}, map[int64][]byte{50: zipBytes, 51: []byte(sums)})

	cfg := config.Default()
	cfg.InstallDir = binDir
	cfg.OrchestratorDir = filepath.Join(tmp, ".orchestrator")

	// Previously active: stop failure triggers recovery start.
	calls := stubDeployHost(t, true, service.Active, filepath.Join(tmp, "orchestrator.service"))
	stopService = func(name string) error {
		*calls = append(*calls, "stop "+name)
		return errors.New("status query failed after stop")
	}
	if _, err := Upgrade(context.Background(), cfg, srv.client(t, "0x3639/orchestrator"), rel); err == nil {
		t.Fatal("expected stop error")
	}
	if strings.Join(*calls, ",") != "stop orchestrator,start orchestrator" {
		t.Fatalf("active unit must recover on stop error: %v", *calls)
	}

	// Previously stopped: stop failure must NOT start the unit.
	calls = stubDeployHost(t, true, service.Inactive, filepath.Join(tmp, "orchestrator.service"))
	stopService = func(name string) error {
		*calls = append(*calls, "stop "+name)
		return errors.New("status query failed")
	}
	if _, err := Upgrade(context.Background(), cfg, srv.client(t, "0x3639/orchestrator"), rel); err == nil {
		t.Fatal("expected stop error")
	}
	if strings.Join(*calls, ",") != "stop orchestrator" {
		t.Fatalf("stopped unit must not be started on stop error: %v", *calls)
	}
}

func TestInstallPreflightsConfigSymlinkBeforeStoppingActiveService(t *testing.T) {
	tmp := t.TempDir()
	binDir := filepath.Join(tmp, "bin")
	orchDir := filepath.Join(tmp, ".orchestrator")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(orchDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(binDir, "orchestrator"), []byte("v1"), 0o755); err != nil {
		t.Fatal(err)
	}
	other := filepath.Join(tmp, "other.json")
	if err := os.WriteFile(other, []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(other, ConfigPath(orchDir)); err != nil {
		t.Fatal(err)
	}

	srv := newMockReleaseServer(t, nil, nil)
	calls := stubDeployHost(t, true, service.Active, filepath.Join(tmp, "orchestrator.service"))

	cfg := config.Default()
	cfg.InstallDir = binDir
	cfg.OrchestratorDir = orchDir
	if _, err := Install(context.Background(), cfg, srv.client(t, "0x3639/orchestrator"), Release{Tag: "v2"}); err == nil {
		t.Fatal("expected symlinked config.json to be rejected")
	}
	if len(*calls) != 0 {
		t.Fatalf("service must not be stopped when config.json is a symlink: %v", *calls)
	}
}

func TestUpgradeRollbackRefusesRestoreWhenStopFailsOrUnitStaysActive(t *testing.T) {
	makeRelease := func(t *testing.T) (*mockReleaseServer, Release) {
		t.Helper()
		zipBytes := makeZip(t, map[string]struct {
			body []byte
			mode os.FileMode
		}{
			"orchestrator-linux-amd64": {body: []byte("v2-broken"), mode: 0o755},
		})
		sums := shaHex(zipBytes) + "  orchestrator-linux-amd64.zip\n"
		rel := Release{
			Tag: "v2",
			Assets: []Asset{
				{ID: 60, Name: "orchestrator-linux-amd64.zip"},
				{ID: 61, Name: "SHA256CHECKSUMS.txt"},
			},
		}
		return newMockReleaseServer(t, []Release{rel}, map[int64][]byte{60: zipBytes, 61: []byte(sums)}), rel
	}

	t.Run("rollback stop error", func(t *testing.T) {
		tmp := t.TempDir()
		binDir := filepath.Join(tmp, "bin")
		if err := os.MkdirAll(binDir, 0o755); err != nil {
			t.Fatal(err)
		}
		binPath := filepath.Join(binDir, "orchestrator")
		if err := os.WriteFile(binPath, []byte("v1-live"), 0o755); err != nil {
			t.Fatal(err)
		}
		srv, rel := makeRelease(t)
		calls := stubDeployHost(t, true, service.Active, filepath.Join(tmp, "orchestrator.service"))
		stopAttempts := 0
		stopService = func(name string) error {
			stopAttempts++
			*calls = append(*calls, "stop "+name)
			if stopAttempts == 2 {
				return errors.New("systemctl stop timed out")
			}
			return nil
		}
		startService = func(name string) error {
			*calls = append(*calls, "start "+name)
			return errors.New("candidate start failed")
		}

		cfg := config.Default()
		cfg.InstallDir = binDir
		cfg.OrchestratorDir = filepath.Join(tmp, ".orchestrator")
		_, err := Upgrade(context.Background(), cfg, srv.client(t, "0x3639/orchestrator"), rel)
		if err == nil {
			t.Fatal("expected upgrade error")
		}
		for _, want := range []string{"candidate start failed", "systemctl stop timed out", "refusing to replace running candidate binary", "retained at"} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("error %q missing %q", err.Error(), want)
			}
		}
		// Candidate must not be overwritten while stop failed, and recovery start must not run.
		if got, _ := os.ReadFile(binPath); string(got) != "v2-broken" {
			t.Fatalf("live binary = %q, want v2-broken untouched while candidate may run", string(got))
		}
		if strings.Join(*calls, ",") != "stop orchestrator,start orchestrator,stop orchestrator" {
			t.Fatalf("calls = %v, must not call recovery start when rollback stop failed", *calls)
		}
		stagedFiles, err := filepath.Glob(filepath.Join(binDir, ".nomctl-orchestrator-*"))
		if err != nil || len(stagedFiles) != 1 {
			t.Fatalf("expected 1 retained staged previous binary, got %v (err=%v)", stagedFiles, err)
		}
		if got, _ := os.ReadFile(stagedFiles[0]); string(got) != "v1-live" {
			t.Fatalf("retained staged binary = %q, want v1-live", string(got))
		}

		// Subsequent upgrade must refuse to stage unresolved V2 as rollback until recovered.
		*calls = nil
		relV3 := Release{Tag: "v3", Assets: rel.Assets}
		if _, err := Upgrade(context.Background(), cfg, srv.client(t, "0x3639/orchestrator"), relV3); err == nil || !strings.Contains(err.Error(), "unresolved orchestrator rollback") {
			t.Fatalf("expected unresolved rollback guard on V3 upgrade, got %v", err)
		}
		if len(*calls) != 0 {
			t.Fatalf("V3 upgrade must not touch service while rollback is unresolved: %v", *calls)
		}

		// Explicit rollback recovery stops V2, restores V1, clears pending state, and restarts V1.
		curState := service.Active
		stopService = func(name string) error {
			*calls = append(*calls, "stop "+name)
			curState = service.Inactive
			return nil
		}
		startService = func(name string) error {
			*calls = append(*calls, "start "+name)
			curState = service.Active
			return nil
		}
		serviceStatus = func(string) (service.State, error) { return curState, nil }
		if err := RecoverPendingRollback(cfg); err != nil {
			t.Fatalf("RecoverPendingRollback failed: %v", err)
		}
		if got, _ := os.ReadFile(binPath); string(got) != "v1-live" {
			t.Fatalf("live binary after recovery = %q, want v1-live", string(got))
		}
		if _, err := os.Stat(deployStatePath(binPath)); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("pending state file should be removed after recovery, stat err=%v", err)
		}
		if strings.Join(*calls, ",") != "stop orchestrator,start orchestrator" {
			t.Fatalf("recovery calls = %v", *calls)
		}
	})

	t.Run("rollback stop error recovers when status recheck confirms stopped", func(t *testing.T) {
		tmp := t.TempDir()
		binDir := filepath.Join(tmp, "bin")
		if err := os.MkdirAll(binDir, 0o755); err != nil {
			t.Fatal(err)
		}
		binPath := filepath.Join(binDir, "orchestrator")
		if err := os.WriteFile(binPath, []byte("v1-live"), 0o755); err != nil {
			t.Fatal(err)
		}
		srv, rel := makeRelease(t)
		calls := stubDeployHost(t, true, service.Active, filepath.Join(tmp, "orchestrator.service"))
		curState := service.Active
		stopAttempts := 0
		stopService = func(name string) error {
			stopAttempts++
			*calls = append(*calls, "stop "+name)
			curState = service.Inactive
			if stopAttempts == 2 {
				return errors.New("transient post-stop status query error")
			}
			return nil
		}
		startAttempts := 0
		startService = func(name string) error {
			startAttempts++
			*calls = append(*calls, "start "+name)
			if startAttempts == 1 {
				return errors.New("candidate start failed")
			}
			curState = service.Active
			return nil
		}
		serviceStatus = func(string) (service.State, error) { return curState, nil }

		cfg := config.Default()
		cfg.InstallDir = binDir
		cfg.OrchestratorDir = filepath.Join(tmp, ".orchestrator")
		_, err := Upgrade(context.Background(), cfg, srv.client(t, "0x3639/orchestrator"), rel)
		if err == nil {
			t.Fatal("expected upgrade error")
		}
		for _, want := range []string{"candidate start failed", "transient post-stop status query error"} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("error %q missing %q", err.Error(), want)
			}
		}
		if got, _ := os.ReadFile(binPath); string(got) != "v1-live" {
			t.Fatalf("live binary = %q, want v1-live restored after status recheck confirmed stopped", string(got))
		}
		if _, err := os.Stat(deployStatePath(binPath)); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("pending rollback state must not exist after successful recovery, err=%v", err)
		}
		if strings.Join(*calls, ",") != "stop orchestrator,start orchestrator,stop orchestrator,start orchestrator" {
			t.Fatalf("calls = %v", *calls)
		}
	})

	t.Run("unit remains active after rollback stop", func(t *testing.T) {
		tmp := t.TempDir()
		binDir := filepath.Join(tmp, "bin")
		if err := os.MkdirAll(binDir, 0o755); err != nil {
			t.Fatal(err)
		}
		binPath := filepath.Join(binDir, "orchestrator")
		if err := os.WriteFile(binPath, []byte("v1-live"), 0o755); err != nil {
			t.Fatal(err)
		}
		srv, rel := makeRelease(t)
		calls := stubDeployHost(t, true, service.Active, filepath.Join(tmp, "orchestrator.service"))
		stopAttempts := 0
		stopService = func(name string) error {
			stopAttempts++
			*calls = append(*calls, "stop "+name)
			return nil
		}
		startService = func(name string) error {
			*calls = append(*calls, "start "+name)
			return errors.New("candidate start failed")
		}
		serviceStatus = func(string) (service.State, error) {
			return service.Active, nil
		}

		cfg := config.Default()
		cfg.InstallDir = binDir
		cfg.OrchestratorDir = filepath.Join(tmp, ".orchestrator")
		_, err := Upgrade(context.Background(), cfg, srv.client(t, "0x3639/orchestrator"), rel)
		if err == nil {
			t.Fatal("expected upgrade error")
		}
		if !strings.Contains(err.Error(), "refusing to replace running candidate binary") || !strings.Contains(err.Error(), "retained at") {
			t.Fatalf("unexpected error: %v", err)
		}
		if got, _ := os.ReadFile(binPath); string(got) != "v2-broken" {
			t.Fatalf("live binary = %q, want v2-broken untouched while unit stays active", string(got))
		}
		if strings.Join(*calls, ",") != "stop orchestrator,start orchestrator,stop orchestrator" {
			t.Fatalf("calls = %v, must not call recovery start when unit stays active", *calls)
		}
		stagedFiles, err := filepath.Glob(filepath.Join(binDir, ".nomctl-orchestrator-*"))
		if err != nil || len(stagedFiles) != 1 {
			t.Fatalf("expected 1 retained staged previous binary, got %v (err=%v)", stagedFiles, err)
		}
		if got, _ := os.ReadFile(stagedFiles[0]); string(got) != "v1-live" {
			t.Fatalf("retained staged binary = %q, want v1-live", string(got))
		}
	})
}

func TestUpgradeRetainsStagedPreviousWhenPublishFails(t *testing.T) {
	setupFailedPublish := func(t *testing.T) (config.Config, *mockReleaseServer, Release, *[]string, string, string) {
		t.Helper()
		tmp := t.TempDir()
		binDir := filepath.Join(tmp, "bin")
		if err := os.MkdirAll(binDir, 0o755); err != nil {
			t.Fatal(err)
		}
		binPath := filepath.Join(binDir, "orchestrator")
		if err := os.WriteFile(binPath, []byte("v1-live"), 0o755); err != nil {
			t.Fatal(err)
		}
		// Make .previous a non-empty directory so os.Rename(previousStaged, previousPath) fails.
		prevDir := binPath + ".previous"
		if err := os.MkdirAll(filepath.Join(prevDir, "blocker"), 0o755); err != nil {
			t.Fatal(err)
		}

		zipBytes := makeZip(t, map[string]struct {
			body []byte
			mode os.FileMode
		}{
			"orchestrator-linux-amd64": {body: []byte("v2-good"), mode: 0o755},
		})
		sums := shaHex(zipBytes) + "  orchestrator-linux-amd64.zip\n"
		rel := Release{
			Tag: "v2",
			Assets: []Asset{
				{ID: 70, Name: "orchestrator-linux-amd64.zip"},
				{ID: 71, Name: "SHA256CHECKSUMS.txt"},
			},
		}
		srv := newMockReleaseServer(t, []Release{rel}, map[int64][]byte{70: zipBytes, 71: []byte(sums)})
		calls := stubDeployHost(t, true, service.Active, filepath.Join(tmp, "orchestrator.service"))

		cfg := config.Default()
		cfg.InstallDir = binDir
		cfg.OrchestratorDir = filepath.Join(tmp, ".orchestrator")
		_, err := Upgrade(context.Background(), cfg, srv.client(t, "0x3639/orchestrator"), rel)
		if err == nil {
			t.Fatal("expected .previous publish failure")
		}
		if !strings.Contains(err.Error(), "previous binary retained at") {
			t.Fatalf("error should include retained staged binary path: %v", err)
		}
		stagedFiles, globErr := filepath.Glob(filepath.Join(binDir, ".nomctl-orchestrator-*"))
		if globErr != nil || len(stagedFiles) != 1 {
			t.Fatalf("expected 1 retained staged previous binary, got %v (err=%v)", stagedFiles, globErr)
		}
		if !strings.Contains(err.Error(), stagedFiles[0]) {
			t.Fatalf("error %q did not include exact retained path %q", err.Error(), stagedFiles[0])
		}
		if got, _ := os.ReadFile(stagedFiles[0]); string(got) != "v1-live" {
			t.Fatalf("retained staged binary = %q, want v1-live", string(got))
		}
		if got, _ := os.ReadFile(binPath); string(got) != "v2-good" {
			t.Fatalf("running binary = %q, want v2-good", string(got))
		}
		return cfg, srv, rel, calls, binPath, prevDir
	}

	t.Run("rerun same upgrade finishes .previous publication without redeploying V2", func(t *testing.T) {
		cfg, srv, rel, calls, binPath, prevDir := setupFailedPublish(t)
		if err := os.RemoveAll(prevDir); err != nil {
			t.Fatal(err)
		}
		*calls = nil
		res, err := Upgrade(context.Background(), cfg, srv.client(t, "0x3639/orchestrator"), rel)
		if err != nil {
			t.Fatalf("retry upgrade failed: %v", err)
		}
		if len(*calls) != 0 {
			t.Fatalf("retry must not stop or restart already-running V2, calls = %v", *calls)
		}
		if res.PreviousBinary != prevDir {
			t.Fatalf("PreviousBinary = %q, want %q", res.PreviousBinary, prevDir)
		}
		if got, _ := os.ReadFile(prevDir); string(got) != "v1-live" {
			t.Fatalf(".previous = %q, want v1-live (must not overwrite with V2)", string(got))
		}
		if got, _ := os.ReadFile(binPath); string(got) != "v2-good" {
			t.Fatalf("live binary = %q, want v2-good", string(got))
		}
		if _, err := os.Stat(deployStatePath(binPath)); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("pending state should be cleared after publish, err=%v", err)
		}
	})

	t.Run("CompletePendingPreviousPublish finishes publication explicitly", func(t *testing.T) {
		cfg, _, _, calls, binPath, prevDir := setupFailedPublish(t)
		if err := os.RemoveAll(prevDir); err != nil {
			t.Fatal(err)
		}
		*calls = nil
		prevPath, err := CompletePendingPreviousPublish(cfg)
		if err != nil {
			t.Fatalf("CompletePendingPreviousPublish failed: %v", err)
		}
		if prevPath != prevDir {
			t.Fatalf("prevPath = %q, want %q", prevPath, prevDir)
		}
		if len(*calls) != 0 {
			t.Fatalf("CompletePendingPreviousPublish must not touch running service, calls = %v", *calls)
		}
		if got, _ := os.ReadFile(prevDir); string(got) != "v1-live" {
			t.Fatalf(".previous = %q, want v1-live", string(got))
		}
		if got, _ := os.ReadFile(binPath); string(got) != "v2-good" {
			t.Fatalf("live binary = %q, want v2-good", string(got))
		}
		if _, err := os.Stat(deployStatePath(binPath)); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("pending state should be cleared after publish, err=%v", err)
		}
	})
}

func TestDeployPendingStateProtectionsAndRecovery(t *testing.T) {
	makeRelServer := func(t *testing.T, tag string, relID, assetID int64, payload string) (*mockReleaseServer, Release) {
		t.Helper()
		zipBytes := makeZip(t, map[string]struct {
			body []byte
			mode os.FileMode
		}{
			"orchestrator-linux-amd64": {body: []byte(payload), mode: 0o755},
		})
		sums := shaHex(zipBytes) + "  orchestrator-linux-amd64.zip\n"
		rel := Release{
			ID:  relID,
			Tag: tag,
			Assets: []Asset{
				{ID: assetID, Name: "orchestrator-linux-amd64.zip", Digest: "sha256:" + shaHex(zipBytes)},
				{ID: assetID + 1, Name: "SHA256CHECKSUMS.txt"},
			},
		}
		return newMockReleaseServer(t, []Release{rel}, map[int64][]byte{assetID: zipBytes, assetID + 1: []byte(sums)}), rel
	}

	t.Run("WAL write failure before cutover leaves service and binary untouched", func(t *testing.T) {
		tmp := t.TempDir()
		binDir := filepath.Join(tmp, "bin")
		if err := os.MkdirAll(binDir, 0o755); err != nil {
			t.Fatal(err)
		}
		binPath := filepath.Join(binDir, "orchestrator")
		if err := os.WriteFile(binPath, []byte("v1-live"), 0o755); err != nil {
			t.Fatal(err)
		}
		srv, rel := makeRelServer(t, "v2", 1, 100, "v2-new")
		calls := stubDeployHost(t, true, service.Active, filepath.Join(tmp, "orchestrator.service"))
		oldWrite := writePendingState
		t.Cleanup(func() { writePendingState = oldWrite })
		writePendingState = func(string, pendingDeployState) error {
			return errors.New("no space left on device")
		}

		cfg := config.Default()
		cfg.InstallDir = binDir
		cfg.OrchestratorDir = filepath.Join(tmp, ".orchestrator")
		if _, err := Upgrade(context.Background(), cfg, srv.client(t, "0x3639/orchestrator"), rel); err == nil || !strings.Contains(err.Error(), "no space left on device") {
			t.Fatalf("expected pre-cutover WAL failure, got %v", err)
		}
		if len(*calls) != 0 {
			t.Fatalf("service must not be stopped if pre-cutover WAL write fails: %v", *calls)
		}
		if got, _ := os.ReadFile(binPath); string(got) != "v1-live" {
			t.Fatalf("live binary changed on WAL failure: %q", string(got))
		}
	})

	t.Run("post-cutover disk full reaches restored WAL write and recovers restart from original WAL", func(t *testing.T) {
		tmp := t.TempDir()
		binDir := filepath.Join(tmp, "bin")
		if err := os.MkdirAll(binDir, 0o755); err != nil {
			t.Fatal(err)
		}
		binPath := filepath.Join(binDir, "orchestrator")
		if err := os.WriteFile(binPath, []byte("v1-live"), 0o755); err != nil {
			t.Fatal(err)
		}
		srv, rel := makeRelServer(t, "v2", 2, 110, "v2-broken")
		stubDeployHost(t, true, service.Active, filepath.Join(tmp, "orchestrator.service"))
		curState := service.Active
		stopService = func(string) error {
			curState = service.Inactive
			return nil
		}
		startService = func(string) error { return errors.New("start failed") }
		serviceStatus = func(string) (service.State, error) { return curState, nil }

		writeCalls := 0
		oldWrite := writePendingState
		t.Cleanup(func() { writePendingState = oldWrite })
		writePendingState = func(p string, st pendingDeployState) error {
			writeCalls++
			if writeCalls > 1 {
				return errors.New("no space left on device")
			}
			return defaultWritePendingDeployState(p, st)
		}

		cfg := config.Default()
		cfg.InstallDir = binDir
		cfg.OrchestratorDir = filepath.Join(tmp, ".orchestrator")
		_, err := Upgrade(context.Background(), cfg, srv.client(t, "0x3639/orchestrator"), rel)
		if err == nil {
			t.Fatal("expected upgrade failure")
		}
		if writeCalls < 2 {
			t.Fatalf("expected rollback to attempt phaseRestored WAL write, writeCalls=%d", writeCalls)
		}
		if !strings.Contains(err.Error(), "no space left on device") {
			t.Fatalf("expected phaseRestored WAL write failure to be propagated, got %v", err)
		}
		if got, _ := os.ReadFile(binPath); string(got) != "v1-live" {
			t.Fatalf("live binary = %q, want v1-live restored on disk", string(got))
		}
		// Original pre-cutover WAL stays on disk, blocking V3 until recovery finishes restarting V1.
		if _, err := Upgrade(context.Background(), cfg, srv.client(t, "0x3639/orchestrator"), Release{Tag: "v3", Assets: rel.Assets}); err == nil || !strings.Contains(err.Error(), "nomctl orchestrator recover") {
			t.Fatalf("expected recover guidance error, got %v", err)
		}

		// Recovery recognizes restored V1 from the original cutover WAL, restarts V1, and clears state.
		writePendingState = defaultWritePendingDeployState
		startCalled := false
		startService = func(string) error {
			startCalled = true
			curState = service.Active
			return nil
		}
		if _, err := RecoverPendingDeploy(cfg); err != nil {
			t.Fatalf("RecoverPendingDeploy failed: %v", err)
		}
		if !startCalled || curState != service.Active {
			t.Fatalf("expected recovery to restart V1 (startCalled=%v state=%v)", startCalled, curState)
		}
		if _, err := os.Stat(deployStatePath(binPath)); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("pending WAL should be cleared after recovery, err=%v", err)
		}
	})

	t.Run("pre-cutover restart failure keeps WAL pending until recovered", func(t *testing.T) {
		tmp := t.TempDir()
		binDir := filepath.Join(tmp, "bin")
		if err := os.MkdirAll(binDir, 0o755); err != nil {
			t.Fatal(err)
		}
		binPath := filepath.Join(binDir, "orchestrator")
		if err := os.WriteFile(binPath, []byte("v1-live"), 0o755); err != nil {
			t.Fatal(err)
		}
		srv, rel := makeRelServer(t, "v2", 21, 112, "v2-new")
		stubDeployHost(t, true, service.Active, filepath.Join(tmp, "orchestrator.service"))
		curState := service.Active
		stopService = func(string) error {
			curState = service.Inactive
			return errors.New("post-stop status query failed")
		}
		startService = func(string) error { return errors.New("systemd busy") }
		serviceStatus = func(string) (service.State, error) { return curState, nil }

		cfg := config.Default()
		cfg.InstallDir = binDir
		cfg.OrchestratorDir = filepath.Join(tmp, ".orchestrator")
		if _, err := Upgrade(context.Background(), cfg, srv.client(t, "0x3639/orchestrator"), rel); err == nil || !strings.Contains(err.Error(), "nomctl orchestrator recover") {
			t.Fatalf("expected pending recovery guidance on restart failure, got %v", err)
		}
		if _, err := os.Stat(deployStatePath(binPath)); err != nil {
			t.Fatalf("WAL must stay pending when V1 restart fails: %v", err)
		}
		startService = func(string) error {
			curState = service.Active
			return nil
		}
		if _, err := RecoverPendingDeploy(cfg); err != nil {
			t.Fatalf("RecoverPendingDeploy failed: %v", err)
		}
		if curState != service.Active {
			t.Fatalf("expected V1 active after recovery, got %v", curState)
		}
		if _, err := os.Stat(deployStatePath(binPath)); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("WAL should be cleared after recovery, err=%v", err)
		}
	})

	t.Run("failed publish-phase write skips rename and leaves staged V1 for rollback", func(t *testing.T) {
		tmp := t.TempDir()
		binDir := filepath.Join(tmp, "bin")
		if err := os.MkdirAll(binDir, 0o755); err != nil {
			t.Fatal(err)
		}
		binPath := filepath.Join(binDir, "orchestrator")
		if err := os.WriteFile(binPath, []byte("v1-live"), 0o755); err != nil {
			t.Fatal(err)
		}
		srv, rel := makeRelServer(t, "v2", 22, 114, "v2-good")
		stubDeployHost(t, true, service.Active, filepath.Join(tmp, "orchestrator.service"))
		writeCalls := 0
		oldWrite := writePendingState
		t.Cleanup(func() { writePendingState = oldWrite })
		writePendingState = func(p string, st pendingDeployState) error {
			writeCalls++
			if st.Phase == phasePublishPrevious {
				return errors.New("disk full writing publish phase")
			}
			return defaultWritePendingDeployState(p, st)
		}

		cfg := config.Default()
		cfg.InstallDir = binDir
		cfg.OrchestratorDir = filepath.Join(tmp, ".orchestrator")
		_, err := Upgrade(context.Background(), cfg, srv.client(t, "0x3639/orchestrator"), rel)
		if err == nil || !strings.Contains(err.Error(), "reconcile the recorded pending state") {
			t.Fatalf("expected reconciliation guidance when publish-phase write fails, got %v", err)
		}
		if writeCalls < 2 {
			t.Fatalf("expected publish-phase write to be attempted, writeCalls=%d", writeCalls)
		}
		if _, err := os.Stat(binPath + ".previous"); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf(".previous must not be created if publish phase was not recorded, err=%v", err)
		}
		stagedFiles, _ := filepath.Glob(filepath.Join(binDir, ".nomctl-orchestrator-*"))
		if len(stagedFiles) != 1 {
			t.Fatalf("expected retained staged V1 file, got %v", stagedFiles)
		}
	})

	t.Run("sync failure after publish-phase state rename leaves visible publish-previous recoverable", func(t *testing.T) {
		tmp := t.TempDir()
		binDir := filepath.Join(tmp, "bin")
		if err := os.MkdirAll(binDir, 0o755); err != nil {
			t.Fatal(err)
		}
		binPath := filepath.Join(binDir, "orchestrator")
		if err := os.WriteFile(binPath, []byte("v1-live"), 0o755); err != nil {
			t.Fatal(err)
		}
		srv, rel := makeRelServer(t, "v2", 25, 115, "v2-good")
		stubDeployHost(t, true, service.Active, filepath.Join(tmp, "orchestrator.service"))
		oldSync := syncDirPath
		t.Cleanup(func() { syncDirPath = oldSync })
		syncCalls := 0
		syncDirPath = func(dir string) error {
			syncCalls++
			// Call 1: pre-cutover WAL sync; Call 2: post-cutover binary rename sync;
			// Call 3: sync after phasePublishPrevious state-file rename -> inject failure.
			if syncCalls == 3 {
				return errors.New("dir fsync EIO")
			}
			return defaultSyncDir(dir)
		}

		cfg := config.Default()
		cfg.InstallDir = binDir
		cfg.OrchestratorDir = filepath.Join(tmp, ".orchestrator")
		_, err := Upgrade(context.Background(), cfg, srv.client(t, "0x3639/orchestrator"), rel)
		if err == nil || !strings.Contains(err.Error(), "completing publication if recorded or rolling back if cutover remains pending") {
			t.Fatalf("expected dual-phase reconciliation guidance, got %v", err)
		}
		if _, err := os.Stat(binPath + ".previous"); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf(".previous must not be renamed before publish state sync succeeds, err=%v", err)
		}
		// Once storage recovers, RecoverPendingDeploy sees visible phasePublishPrevious and finishes publication keeping V2.
		syncDirPath = defaultSyncDir
		summary, err := RecoverPendingDeploy(cfg)
		if err != nil {
			t.Fatalf("RecoverPendingDeploy failed: %v", err)
		}
		if !strings.Contains(summary, "Published previous orchestrator binary") {
			t.Fatalf("unexpected recovery summary: %q", summary)
		}
		if got, _ := os.ReadFile(binPath); string(got) != "v2-good" {
			t.Fatalf("live binary = %q, want v2-good preserved", string(got))
		}
		if prev, _ := os.ReadFile(binPath + ".previous"); string(prev) != "v1-live" {
			t.Fatalf(".previous = %q, want v1-live published", string(prev))
		}
	})

	t.Run("rollback retains WAL when dir sync fails after restoring binary (both deploy and recover paths)", func(t *testing.T) {
		// 1. Deploy-time rollbackBinary sync failure retains cutover WAL and skips phaseRestored/removal.
		tmp := t.TempDir()
		binDir := filepath.Join(tmp, "bin")
		if err := os.MkdirAll(binDir, 0o755); err != nil {
			t.Fatal(err)
		}
		binPath := filepath.Join(binDir, "orchestrator")
		if err := os.WriteFile(binPath, []byte("v1-live"), 0o755); err != nil {
			t.Fatal(err)
		}
		srv, rel := makeRelServer(t, "v2", 26, 117, "v2-broken")
		stubDeployHost(t, true, service.Active, filepath.Join(tmp, "orchestrator.service"))
		curState := service.Active
		stopService = func(string) error { curState = service.Inactive; return nil }
		startService = func(string) error { return errors.New("v2 failed") }
		serviceStatus = func(string) (service.State, error) { return curState, nil }
		oldSync := syncDirPath
		t.Cleanup(func() { syncDirPath = oldSync })
		syncCalls := 0
		syncDirPath = func(dir string) error {
			syncCalls++
			// Call 1: pre-cutover WAL; Call 2: post-cutover binary; Call 3: rollback restore sync -> fail.
			if syncCalls == 3 {
				return errors.New("rollback dir fsync failed")
			}
			return defaultSyncDir(dir)
		}

		cfg := config.Default()
		cfg.InstallDir = binDir
		cfg.OrchestratorDir = filepath.Join(tmp, ".orchestrator")
		if _, err := Upgrade(context.Background(), cfg, srv.client(t, "0x3639/orchestrator"), rel); err == nil || !strings.Contains(err.Error(), "rollback dir fsync failed") {
			t.Fatalf("expected rollback dir sync error, got %v", err)
		}
		st, err := loadPendingDeployState(binPath)
		if err != nil || st == nil || st.Phase != phaseCutover {
			t.Fatalf("WAL must remain in phaseCutover when rollback dir sync fails, got %+v (err=%v)", st, err)
		}
		// Retry after sync recovers: syncs dir, restarts V1, clears WAL.
		syncDirPath = defaultSyncDir
		startService = func(string) error { curState = service.Active; return nil }
		if _, err := RecoverPendingDeploy(cfg); err != nil {
			t.Fatalf("RecoverPendingDeploy after sync recovery failed: %v", err)
		}
		if curState != service.Active {
			t.Fatalf("expected V1 active after recovery, got %v", curState)
		}
		if _, err := os.Stat(deployStatePath(binPath)); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("WAL should be removed once sync succeeds, err=%v", err)
		}

		// 2. RecoverPendingRollback sync failure retains cutover WAL until retry succeeds.
		tmp2 := t.TempDir()
		binDir2 := filepath.Join(tmp2, "bin")
		if err := os.MkdirAll(binDir2, 0o755); err != nil {
			t.Fatal(err)
		}
		binPath2 := filepath.Join(binDir2, "orchestrator")
		staged2 := filepath.Join(binDir2, ".nomctl-orchestrator-v1")
		if err := os.WriteFile(binPath2, []byte("v2-broken"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(staged2, []byte("v1-live"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := defaultWritePendingDeployState(binPath2, pendingDeployState{
			Phase:          phaseCutover,
			Service:        "orchestrator",
			Repo:           "0x3639/orchestrator",
			ReleaseTag:     "v2",
			CandidateSHA:   shaHex([]byte("v2-broken")),
			PreviousStaged: staged2,
			PreviousSHA:    shaHex([]byte("v1-live")),
			WasRunning:     true,
		}); err != nil {
			t.Fatal(err)
		}
		stubDeployHost(t, true, service.Active, filepath.Join(tmp2, "orchestrator.service"))
		syncDirPath = func(string) error { return errors.New("recover dir fsync failed") }
		cfg2 := config.Default()
		cfg2.InstallDir = binDir2
		cfg2.OrchestratorDir = filepath.Join(tmp2, ".orchestrator")
		if err := RecoverPendingRollback(cfg2); err == nil || !strings.Contains(err.Error(), "recover dir fsync failed") {
			t.Fatalf("expected RecoverPendingRollback dir sync error, got %v", err)
		}
		st2, err := loadPendingDeployState(binPath2)
		if err != nil || st2 == nil || st2.Phase != phaseCutover {
			t.Fatalf("WAL must stay in phaseCutover when RecoverPendingRollback dir sync fails, got %+v (err=%v)", st2, err)
		}
		syncDirPath = defaultSyncDir
		if err := RecoverPendingRollback(cfg2); err != nil {
			t.Fatalf("RecoverPendingRollback retry failed: %v", err)
		}
		if _, err := os.Stat(deployStatePath(binPath2)); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("WAL should be removed after retry sync succeeds, err=%v", err)
		}
	})

	t.Run("rollbackBinary keeps WAL if restored service is not Active after startService returns nil", func(t *testing.T) {
		tmp := t.TempDir()
		binDir := filepath.Join(tmp, "bin")
		if err := os.MkdirAll(binDir, 0o755); err != nil {
			t.Fatal(err)
		}
		binPath := filepath.Join(binDir, "orchestrator")
		if err := os.WriteFile(binPath, []byte("v1-live"), 0o755); err != nil {
			t.Fatal(err)
		}
		srv, rel := makeRelServer(t, "v2", 27, 119, "v2-broken")
		stubDeployHost(t, true, service.Active, filepath.Join(tmp, "orchestrator.service"))
		curState := service.Active
		stopService = func(string) error { curState = service.Inactive; return nil }
		startAttempts := 0
		startService = func(string) error {
			startAttempts++
			if startAttempts == 1 {
				return errors.New("v2 failed")
			}
			// Simulate restored V1 exiting immediately (startService returns nil, status is Failed).
			curState = service.Failed
			return nil
		}
		serviceStatus = func(string) (service.State, error) { return curState, nil }

		cfg := config.Default()
		cfg.InstallDir = binDir
		cfg.OrchestratorDir = filepath.Join(tmp, ".orchestrator")
		if _, err := Upgrade(context.Background(), cfg, srv.client(t, "0x3639/orchestrator"), rel); err == nil || !strings.Contains(err.Error(), "restored orchestrator is failed") {
			t.Fatalf("expected non-active restored service error, got %v", err)
		}
		st, err := loadPendingDeployState(binPath)
		if err != nil || st == nil || st.Phase != phaseRestored {
			t.Fatalf("WAL must stay in phaseRestored when restored service is not Active, got %+v (err=%v)", st, err)
		}
		startService = func(string) error { curState = service.Active; return nil }
		if _, err := RecoverPendingDeploy(cfg); err != nil {
			t.Fatalf("RecoverPendingDeploy retry failed: %v", err)
		}
		if curState != service.Active {
			t.Fatalf("expected V1 Active after recovery, got %v", curState)
		}
	})

	t.Run("verified first install with failed WAL unlink clears state without rolling back", func(t *testing.T) {
		tmp := t.TempDir()
		binDir := filepath.Join(tmp, "bin")
		orchDir := filepath.Join(tmp, ".orchestrator")
		unitPath := filepath.Join(tmp, "systemd", "orchestrator.service")
		srv, rel := makeRelServer(t, "v1", 23, 116, "v1-good")
		calls := stubDeployHost(t, false, service.NotFound, unitPath)
		oldRm := removePendingState
		t.Cleanup(func() { removePendingState = oldRm })
		removePendingState = func(string) error { return errors.New("unlink failed") }

		cfg := config.Default()
		cfg.InstallDir = binDir
		cfg.OrchestratorDir = orchDir
		binPath := filepath.Join(binDir, "orchestrator")
		if _, err := Install(context.Background(), cfg, srv.client(t, "0x3639/orchestrator"), rel); err == nil || !strings.Contains(err.Error(), "clearing pending state") {
			t.Fatalf("expected WAL cleanup error on first install, got %v", err)
		}

		*calls = nil
		removePendingState = os.Remove
		summary, err := RecoverPendingDeploy(cfg)
		if err != nil {
			t.Fatalf("RecoverPendingDeploy failed: %v", err)
		}
		if !strings.Contains(summary, "Cleared completed install state") {
			t.Fatalf("unexpected recovery summary: %q", summary)
		}
		if len(*calls) != 0 {
			t.Fatalf("recovery of verified first install must not stop or restart unit: %v", *calls)
		}
		if got, _ := os.ReadFile(binPath); string(got) != "v1-good" {
			t.Fatalf("verified first-install binary must be preserved, got %q", string(got))
		}
		if _, err := os.Stat(deployStatePath(binPath)); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("WAL should be cleared, err=%v", err)
		}
	})

	t.Run("interruption before candidate replaces live binary recovers V1 cleanly", func(t *testing.T) {
		tmp := t.TempDir()
		binDir := filepath.Join(tmp, "bin")
		if err := os.MkdirAll(binDir, 0o755); err != nil {
			t.Fatal(err)
		}
		binPath := filepath.Join(binDir, "orchestrator")
		stagedPath := filepath.Join(binDir, ".nomctl-orchestrator-staged-v1")
		if err := os.WriteFile(binPath, []byte("v1-live"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(stagedPath, []byte("v1-live"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := defaultWritePendingDeployState(binPath, pendingDeployState{
			Phase:          phaseCutover,
			Service:        "orchestrator",
			Repo:           "0x3639/orchestrator",
			ReleaseTag:     "v2",
			CandidateSHA:   shaHex([]byte("v2-never-installed")),
			PreviousStaged: stagedPath,
			PreviousSHA:    shaHex([]byte("v1-live")),
			WasRunning:     true,
		}); err != nil {
			t.Fatal(err)
		}
		calls := stubDeployHost(t, true, service.Inactive, filepath.Join(tmp, "orchestrator.service"))
		cfg := config.Default()
		cfg.InstallDir = binDir
		cfg.OrchestratorDir = filepath.Join(tmp, ".orchestrator")
		if err := RecoverPendingRollback(cfg); err != nil {
			t.Fatalf("RecoverPendingRollback failed: %v", err)
		}
		if got, _ := os.ReadFile(binPath); string(got) != "v1-live" {
			t.Fatalf("live binary = %q, want v1-live", string(got))
		}
		if strings.Join(*calls, ",") != "start orchestrator" {
			t.Fatalf("should only restart V1 without re-stopping or replacing, calls = %v", *calls)
		}
		if _, err := os.Stat(stagedPath); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("redundant staged copy should be removed, err=%v", err)
		}
		if _, err := os.Stat(deployStatePath(binPath)); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("WAL should be removed, err=%v", err)
		}
	})

	t.Run("same-binary redeploy rollback with failed phaseRestored write still retries restart", func(t *testing.T) {
		tmp := t.TempDir()
		binDir := filepath.Join(tmp, "bin")
		if err := os.MkdirAll(binDir, 0o755); err != nil {
			t.Fatal(err)
		}
		binPath := filepath.Join(binDir, "orchestrator")
		// Existing binary and .previous already share the exact same bytes as the redeployed release.
		if err := os.WriteFile(binPath, []byte("same-binary"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(binPath+".previous", []byte("same-binary"), 0o755); err != nil {
			t.Fatal(err)
		}
		srv, rel := makeRelServer(t, "v1", 24, 118, "same-binary")
		stubDeployHost(t, true, service.Active, filepath.Join(tmp, "orchestrator.service"))
		curState := service.Active
		stopService = func(string) error {
			curState = service.Inactive
			return nil
		}
		startService = func(string) error { return errors.New("start failed") }
		serviceStatus = func(string) (service.State, error) { return curState, nil }
		oldWrite := writePendingState
		t.Cleanup(func() { writePendingState = oldWrite })
		writePendingState = func(p string, st pendingDeployState) error {
			if st.Phase == phaseRestored {
				return errors.New("disk full")
			}
			return defaultWritePendingDeployState(p, st)
		}

		cfg := config.Default()
		cfg.InstallDir = binDir
		cfg.OrchestratorDir = filepath.Join(tmp, ".orchestrator")
		if _, err := Upgrade(context.Background(), cfg, srv.client(t, "0x3639/orchestrator"), rel); err == nil {
			t.Fatal("expected failed redeploy")
		}
		writePendingState = defaultWritePendingDeployState
		restarted := false
		startService = func(string) error {
			restarted = true
			curState = service.Active
			return nil
		}
		summary, err := RecoverPendingDeploy(cfg)
		if err != nil {
			t.Fatalf("RecoverPendingDeploy failed on same-binary rollback: %v", err)
		}
		if !restarted || !strings.Contains(summary, "Recovered pending rollback") {
			t.Fatalf("expected rollback restart, restarted=%v summary=%q", restarted, summary)
		}
	})

	t.Run("different repository or release ID does not short-circuit pending publish", func(t *testing.T) {
		tmp := t.TempDir()
		binDir := filepath.Join(tmp, "bin")
		if err := os.MkdirAll(binDir, 0o755); err != nil {
			t.Fatal(err)
		}
		binPath := filepath.Join(binDir, "orchestrator")
		if err := os.WriteFile(binPath, []byte("v1-live"), 0o755); err != nil {
			t.Fatal(err)
		}
		prevDir := binPath + ".previous"
		if err := os.MkdirAll(filepath.Join(prevDir, "blocker"), 0o755); err != nil {
			t.Fatal(err)
		}
		srvA, relA := makeRelServer(t, "v2", 10, 120, "v2-repo-a")
		srvB, relB := makeRelServer(t, "v2", 20, 130, "v2-repo-b")
		stubDeployHost(t, true, service.Active, filepath.Join(tmp, "orchestrator.service"))

		cfg := config.Default()
		cfg.InstallDir = binDir
		cfg.OrchestratorDir = filepath.Join(tmp, ".orchestrator")
		if _, err := Upgrade(context.Background(), cfg, srvA.client(t, "0x3639/orchestrator"), relA); err == nil {
			t.Fatal("expected initial .previous publish failure")
		}
		if err := os.RemoveAll(prevDir); err != nil {
			t.Fatal(err)
		}
		// Selecting repo B's v2 must finish repo A's publish AND fetch/deploy repo B's v2.
		res, err := Upgrade(context.Background(), cfg, srvB.client(t, "HyperCore-Team/orchestrator"), relB)
		if err != nil {
			t.Fatalf("repo B upgrade failed: %v", err)
		}
		if res.Repo != "HyperCore-Team/orchestrator" {
			t.Fatalf("Repo = %q, want HyperCore-Team/orchestrator", res.Repo)
		}
		if got, _ := os.ReadFile(binPath); string(got) != "v2-repo-b" {
			t.Fatalf("live binary = %q, want v2-repo-b (repo B must actually be fetched and installed)", string(got))
		}
		if prev, _ := os.ReadFile(prevDir); string(prev) != "v2-repo-a" {
			t.Fatalf(".previous = %q, want v2-repo-a", string(prev))
		}
	})

	t.Run("pending recovery binds to original systemd unit", func(t *testing.T) {
		tmp := t.TempDir()
		binDir := filepath.Join(tmp, "bin")
		if err := os.MkdirAll(binDir, 0o755); err != nil {
			t.Fatal(err)
		}
		binPath := filepath.Join(binDir, "orchestrator")
		if err := os.WriteFile(binPath, []byte("v1-live"), 0o755); err != nil {
			t.Fatal(err)
		}
		srv, rel := makeRelServer(t, "v2", 3, 140, "v2-broken")
		calls := stubDeployHost(t, true, service.Active, filepath.Join(tmp, "orchestrator.service"))
		startService = func(string) error { return errors.New("start failed") }
		serviceStatus = func(string) (service.State, error) { return service.Active, nil }

		cfg := config.Default()
		cfg.InstallDir = binDir
		cfg.OrchestratorDir = filepath.Join(tmp, ".orchestrator")
		cfg.OrchestratorService = "orchestrator-a"
		if _, err := Upgrade(context.Background(), cfg, srv.client(t, "0x3639/orchestrator"), rel); err == nil {
			t.Fatal("expected failed upgrade")
		}

		*calls = nil
		cfgB := cfg
		cfgB.OrchestratorService = "orchestrator-b"
		if err := RecoverPendingRollback(cfgB); err == nil || !strings.Contains(err.Error(), "orchestrator-a") {
			t.Fatalf("expected service mismatch rejection, got %v", err)
		}
		if len(*calls) != 0 {
			t.Fatalf("unit B must not be queried or touched on mismatch: %v", *calls)
		}
	})

	t.Run("publication retry succeeds after rename when pending-file removal fails, and rejects unrelated .previous", func(t *testing.T) {
		tmp := t.TempDir()
		binDir := filepath.Join(tmp, "bin")
		if err := os.MkdirAll(binDir, 0o755); err != nil {
			t.Fatal(err)
		}
		binPath := filepath.Join(binDir, "orchestrator")
		if err := os.WriteFile(binPath, []byte("v1-live"), 0o755); err != nil {
			t.Fatal(err)
		}
		srv, rel := makeRelServer(t, "v2", 4, 150, "v2-good")
		stubDeployHost(t, true, service.Active, filepath.Join(tmp, "orchestrator.service"))
		oldRm := removePendingState
		t.Cleanup(func() { removePendingState = oldRm })
		removePendingState = func(string) error { return errors.New("permission denied removing pending file") }

		cfg := config.Default()
		cfg.InstallDir = binDir
		cfg.OrchestratorDir = filepath.Join(tmp, ".orchestrator")
		if _, err := Upgrade(context.Background(), cfg, srv.client(t, "0x3639/orchestrator"), rel); err == nil || !strings.Contains(err.Error(), "clearing pending state") {
			t.Fatalf("expected pending state removal error, got %v", err)
		}
		// .previous now has V1, staged path was consumed, .pending.json remains.
		if prev, _ := os.ReadFile(binPath + ".previous"); string(prev) != "v1-live" {
			t.Fatalf(".previous = %q, want v1-live", string(prev))
		}
		// Tampering .previous with an unrelated file must be rejected.
		if err := os.WriteFile(binPath+".previous", []byte("unrelated-file"), 0o755); err != nil {
			t.Fatal(err)
		}
		removePendingState = os.Remove
		if _, err := CompletePendingPreviousPublish(cfg); err == nil || !strings.Contains(err.Error(), "does not match") {
			t.Fatalf("expected unrelated .previous to be rejected, got %v", err)
		}
		// Restore expected V1 at .previous -> repeated dir sync failure on alreadyPublished path must retain WAL.
		if err := os.WriteFile(binPath+".previous", []byte("v1-live"), 0o755); err != nil {
			t.Fatal(err)
		}
		serviceStatus = func(string) (service.State, error) { return service.Inactive, nil }
		oldSync := syncDirPath
		t.Cleanup(func() { syncDirPath = oldSync })
		syncDirPath = func(string) error { return errors.New("repeated dir fsync failure") }
		if _, err := CompletePendingPreviousPublish(cfg); err == nil || !strings.Contains(err.Error(), "repeated dir fsync failure") {
			t.Fatalf("expected repeated dir sync failure to retain WAL, got %v", err)
		}
		if _, err := os.Stat(deployStatePath(binPath)); err != nil {
			t.Fatalf("WAL must stay on disk when alreadyPublished dir sync fails: %v", err)
		}
		// Once sync recovers, retry clears .pending.json cleanly even while unit is stopped.
		syncDirPath = defaultSyncDir
		prevPath, err := CompletePendingPreviousPublish(cfg)
		if err != nil || prevPath != binPath+".previous" {
			t.Fatalf("retry publish cleanup failed while unit stopped: %q, %v", prevPath, err)
		}
		if _, err := os.Stat(deployStatePath(binPath)); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("pending state should be removed after retry, err=%v", err)
		}
	})

	t.Run("rollback retries after restore when startService or state removal fails, and respects WasRunning=false", func(t *testing.T) {
		tmp := t.TempDir()
		binDir := filepath.Join(tmp, "bin")
		if err := os.MkdirAll(binDir, 0o755); err != nil {
			t.Fatal(err)
		}
		binPath := filepath.Join(binDir, "orchestrator")
		if err := os.WriteFile(binPath, []byte("v1-live"), 0o755); err != nil {
			t.Fatal(err)
		}
		srv, rel := makeRelServer(t, "v2", 5, 160, "v2-broken")
		calls := stubDeployHost(t, true, service.Active, filepath.Join(tmp, "orchestrator.service"))
		curState := service.Active
		startFails := true
		stopService = func(name string) error {
			*calls = append(*calls, "stop "+name)
			if startFails {
				// First fail rollback stop so pending rollback stays unresolved.
				if len(*calls) > 2 {
					return errors.New("stop hung")
				}
			}
			curState = service.Inactive
			return nil
		}
		startService = func(name string) error {
			*calls = append(*calls, "start "+name)
			if startFails {
				curState = service.Active
				return errors.New("start failed")
			}
			curState = service.Active
			return nil
		}
		serviceStatus = func(string) (service.State, error) { return curState, nil }

		cfg := config.Default()
		cfg.InstallDir = binDir
		cfg.OrchestratorDir = filepath.Join(tmp, ".orchestrator")
		if _, err := Upgrade(context.Background(), cfg, srv.client(t, "0x3639/orchestrator"), rel); err == nil {
			t.Fatal("expected failed upgrade")
		}

		// Recovery attempt 1: stop succeeds and V1 is restored, but startService fails -> pending state stays.
		*calls = nil
		stopService = func(name string) error {
			*calls = append(*calls, "stop "+name)
			curState = service.Inactive
			return nil
		}
		startService = func(name string) error {
			*calls = append(*calls, "start "+name)
			return errors.New("transient start failure")
		}
		if err := RecoverPendingRollback(cfg); err == nil || !strings.Contains(err.Error(), "transient start failure") {
			t.Fatalf("expected restart failure during recovery, got %v", err)
		}
		if got, _ := os.ReadFile(binPath); string(got) != "v1-live" {
			t.Fatalf("V1 should be restored on disk: %q", string(got))
		}
		if _, err := os.Stat(deployStatePath(binPath)); err != nil {
			t.Fatalf("pending state must be kept until required restart succeeds: %v", err)
		}

		// Recovery attempt 2: startService succeeds, but state-file removal fails -> retryable without staging file.
		*calls = nil
		startService = func(name string) error {
			*calls = append(*calls, "start "+name)
			curState = service.Active
			return nil
		}
		oldRm := removePendingState
		t.Cleanup(func() { removePendingState = oldRm })
		removePendingState = func(string) error { return errors.New("rm failed") }
		if err := RecoverPendingRollback(cfg); err == nil || !strings.Contains(err.Error(), "clearing pending state") {
			t.Fatalf("expected pending state removal error, got %v", err)
		}
		if strings.Join(*calls, ",") != "start orchestrator" {
			t.Fatalf("retry should only start restored V1 without re-stopping: %v", *calls)
		}

		// Recovery attempt 3: state-file removal succeeds -> service already Active, no extra start/stop.
		*calls = nil
		removePendingState = os.Remove
		if _, err := RecoverPendingDeploy(cfg); err != nil {
			t.Fatalf("final recovery cleanup failed: %v", err)
		}
		if len(*calls) != 0 {
			t.Fatalf("already-active restored unit should not be restarted again: %v", *calls)
		}
		if _, err := os.Stat(deployStatePath(binPath)); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("pending state should be removed, err=%v", err)
		}
	})

	t.Run("RecoverPendingRollback refuses to overwrite manually changed binary", func(t *testing.T) {
		tmp := t.TempDir()
		binDir := filepath.Join(tmp, "bin")
		if err := os.MkdirAll(binDir, 0o755); err != nil {
			t.Fatal(err)
		}
		binPath := filepath.Join(binDir, "orchestrator")
		if err := os.WriteFile(binPath, []byte("v1-live"), 0o755); err != nil {
			t.Fatal(err)
		}
		srv, rel := makeRelServer(t, "v2", 6, 170, "v2-broken")
		calls := stubDeployHost(t, true, service.Active, filepath.Join(tmp, "orchestrator.service"))
		startService = func(string) error { return errors.New("v2 crashed") }
		serviceStatus = func(string) (service.State, error) { return service.Active, nil }

		cfg := config.Default()
		cfg.InstallDir = binDir
		cfg.OrchestratorDir = filepath.Join(tmp, ".orchestrator")
		if _, err := Upgrade(context.Background(), cfg, srv.client(t, "0x3639/orchestrator"), rel); err == nil {
			t.Fatal("expected failed upgrade")
		}
		// Operator manually installs a different binary at binPath.
		if err := os.WriteFile(binPath, []byte("manual-v4"), 0o755); err != nil {
			t.Fatal(err)
		}
		*calls = nil
		if err := RecoverPendingRollback(cfg); err == nil || !strings.Contains(err.Error(), "no longer matches failed candidate") {
			t.Fatalf("expected manual binary change protection error, got %v", err)
		}
		if got, _ := os.ReadFile(binPath); string(got) != "manual-v4" {
			t.Fatalf("manual binary was overwritten: %q", string(got))
		}
		if len(*calls) != 0 {
			t.Fatalf("service must not be stopped when live binary no longer matches candidate: %v", *calls)
		}
	})

	t.Run("fresh install rollback consistently removes failed candidate binary", func(t *testing.T) {
		tmp := t.TempDir()
		binDir := filepath.Join(tmp, "bin")
		orchDir := filepath.Join(tmp, ".orchestrator")
		unitPath := filepath.Join(tmp, "systemd", "orchestrator.service")
		srv, rel := makeRelServer(t, "v1", 7, 180, "v1-broken")
		stubDeployHost(t, false, service.NotFound, unitPath)
		startService = func(string) error { return errors.New("fresh install crashed") }

		cfg := config.Default()
		cfg.InstallDir = binDir
		cfg.OrchestratorDir = orchDir
		binPath := filepath.Join(binDir, "orchestrator")
		if _, err := Install(context.Background(), cfg, srv.client(t, "0x3639/orchestrator"), rel); err == nil {
			t.Fatal("expected install failure")
		}
		if _, err := os.Stat(binPath); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("fresh install rollback should remove failed binary %s, stat err=%v", binPath, err)
		}
		if _, err := os.Stat(deployStatePath(binPath)); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("pending state should be cleared after clean fresh-install rollback, err=%v", err)
		}
	})
}
