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
	unitExecPath = func(string) (string, error) { return "", nil }
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
