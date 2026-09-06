package bootstrap

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/0x3639/nomctl/internal/config"
)

func TestHashURL(t *testing.T) {
	cases := map[string]string{
		"https://h.example/bootstrap/2023-12-02/bootstrap-20231202111921.zip": "https://h.example/bootstrap/2023-12-02/bootstrap-20231202111921.hash",
		"http://h.example/a.b/snap.zip?x=1":                                   "http://h.example/a.b/snap.hash?x=1",
		"https://h.example/noext":                                             "https://h.example/noext.hash",
	}
	for in, want := range cases {
		if got := HashURL(in); got != want {
			t.Errorf("HashURL(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestParseHash(t *testing.T) {
	digest := strings.Repeat("ab", 32)
	for _, in := range []string{digest, digest + "\n", strings.ToUpper(digest), digest + "  bootstrap.zip\n"} {
		got, err := ParseHash([]byte(in))
		if err != nil || got != digest {
			t.Errorf("ParseHash(%q) = %q, %v", in, got, err)
		}
	}
	for _, in := range []string{"", "  \n", "abc", strings.Repeat("zz", 32), "<html>"} {
		if _, err := ParseHash([]byte(in)); err == nil {
			t.Errorf("ParseHash(%q) accepted", in)
		}
	}
}

func TestValidateURL(t *testing.T) {
	if err := ValidateURL(""); !errors.Is(err, ErrNoURL) {
		t.Errorf("empty: %v", err)
	}
	for _, bad := range []string{"ftp://h/x.zip", "https:///x.zip", "https://h/x.tar.gz", "/root/x.zip", "not a url"} {
		if ValidateURL(bad) == nil {
			t.Errorf("%q accepted", bad)
		}
	}
	if err := ValidateURL("https://h.example/b/snap.ZIP"); err != nil {
		t.Errorf("valid rejected: %v", err)
	}
}

// makeZip builds a snapshot archive with the given entries (name -> content).
func makeZip(t *testing.T, entries map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	w := zip.NewWriter(&buf)
	for name, content := range entries {
		if strings.HasSuffix(name, "/") {
			if _, err := w.Create(name); err != nil {
				t.Fatal(err)
			}
			continue
		}
		f, err := w.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := f.Write([]byte(content)); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func goodEntries() map[string]string {
	return map[string]string{
		"backup/":                          "",
		"backup/nom.bak/":                  "",
		"backup/nom.bak/000001.log":        "nom-data",
		"backup/nom.bak/sub/CURRENT":       "cur",
		"backup/network.bak/nodes.json":    "[]",
		"backup/consensus.bak/000002.ldb":  "consensus",
		"backup/consensus.bak/sub/":        "",
		"backup/consensus.bak/sub/000.log": "x",
	}
}

func writeZip(t *testing.T, dir string, data []byte) string {
	t.Helper()
	p := filepath.Join(dir, "snap.zip")
	if err := os.WriteFile(p, data, 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestInspectAndExtract(t *testing.T) {
	dir := t.TempDir()
	zp := writeZip(t, dir, makeZip(t, goodEntries()))
	m, err := Inspect(zp)
	if err != nil {
		t.Fatal(err)
	}
	if m.Files["nom"] != 2 || m.Files["network"] != 1 || m.Files["consensus"] != 2 {
		t.Errorf("files = %v", m.Files)
	}
	if m.Uncompressed != int64(len("nom-data")+len("cur")+len("[]")+len("consensus")+len("x")) {
		t.Errorf("uncompressed = %d", m.Uncompressed)
	}
	dst := filepath.Join(dir, "out")
	if err := Extract(zp, dst); err != nil {
		t.Fatal(err)
	}
	for rel, want := range map[string]string{
		"nom/000001.log":        "nom-data",
		"nom/sub/CURRENT":       "cur",
		"network/nodes.json":    "[]",
		"consensus/sub/000.log": "x",
	} {
		got, err := os.ReadFile(filepath.Join(dst, rel))
		if err != nil || string(got) != want {
			t.Errorf("%s = %q, %v", rel, got, err)
		}
	}
	if _, err := os.Stat(filepath.Join(dst, "backup")); err == nil {
		t.Error("the backup/ wrapper must not be reproduced")
	}
}

func TestInspectRejectsBadArchives(t *testing.T) {
	dir := t.TempDir()
	cases := map[string]map[string]string{
		"missing dir":     {"backup/nom.bak/a": "x", "backup/network.bak/b": "y"},
		"traversal":       merge(goodEntries(), map[string]string{"backup/nom.bak/../../etc/passwd": "x"}),
		"absolute":        merge(goodEntries(), map[string]string{"/etc/passwd": "x"}),
		"stray top-level": merge(goodEntries(), map[string]string{"README": "x"}),
		"unknown dir":     merge(goodEntries(), map[string]string{"backup/wallet.bak/keys": "x"}),
		"not .bak":        merge(goodEntries(), map[string]string{"backup/nom/keys": "x"}),
	}
	for name, entries := range cases {
		zp := writeZip(t, dir, makeZip(t, entries))
		if _, err := Inspect(zp); err == nil {
			t.Errorf("%s: accepted", name)
		}
		if name == "missing dir" {
			continue // completeness is Inspect's job; Extract only guards paths
		}
		if err := Extract(zp, filepath.Join(dir, "out-"+strings.ReplaceAll(name, " ", "-"))); err == nil {
			t.Errorf("%s: extracted", name)
		}
	}
	if _, err := Inspect(writeZip(t, dir, []byte("not a zip"))); err == nil {
		t.Error("garbage accepted")
	}
}

func merge(a, b map[string]string) map[string]string {
	out := map[string]string{}
	for k, v := range a {
		out[k] = v
	}
	for k, v := range b {
		out[k] = v
	}
	return out
}

func sum(b []byte) string {
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

// host serves a snapshot and its sidecar; hash may be overridden.
func host(t *testing.T, archive []byte, hash string) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/b/snap.zip", func(w http.ResponseWriter, _ *http.Request) {
		http.ServeContent(w, &http.Request{}, "snap.zip", time.Time{}, bytes.NewReader(archive))
	})
	mux.HandleFunc("/b/snap.hash", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(hash + "\n"))
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func TestDownloadReportsAndCleansUp(t *testing.T) {
	data := bytes.Repeat([]byte("z"), 1<<16)
	srv := host(t, data, sum(data))
	dest := filepath.Join(t.TempDir(), "d", "snap.zip")
	var last [2]int64
	if err := Download(context.Background(), srv.URL+"/b/snap.zip", dest, func(done, total int64) { last = [2]int64{done, total} }); err != nil {
		t.Fatal(err)
	}
	if last != [2]int64{int64(len(data)), int64(len(data))} {
		t.Errorf("final report = %v", last)
	}
	if err := Download(context.Background(), srv.URL+"/missing.zip", dest+".2", nil); err == nil {
		t.Error("404 accepted")
	}
	if _, err := os.Stat(dest + ".2"); err == nil {
		t.Error("partial file left behind")
	}
	if got := RemoteSize(context.Background(), srv.URL+"/b/snap.zip"); got != int64(len(data)) {
		t.Errorf("RemoteSize = %d", got)
	}
	if got := RemoteSize(context.Background(), srv.URL+"/missing.zip"); got != -1 {
		t.Errorf("RemoteSize missing = %d", got)
	}
}

type fakeHost struct {
	calls   []string
	stopEr  error
	startEr error
}

func (f *fakeHost) install(t *testing.T) {
	t.Helper()
	oldStop, oldStart, oldFree, oldInstall := stopService, startService, diskFree, installDirs
	stopService = func(name string) error { f.calls = append(f.calls, "stop "+name); return f.stopEr }
	startService = func(name string) error { f.calls = append(f.calls, "start "+name); return f.startEr }
	diskFree = func(string) (int64, int, error) { return 1 << 40, 10, nil }
	t.Cleanup(func() { stopService, startService, diskFree, installDirs = oldStop, oldStart, oldFree, oldInstall })
}

func testConfig(t *testing.T) config.Config {
	t.Helper()
	root := t.TempDir()
	cfg := config.Config{ServiceName: "go-zenon", ZnnDir: filepath.Join(root, "znn"), BackupDir: filepath.Join(root, "backup"), MinFreeSpaceKB: 1}
	for _, d := range []string{"nom", "network", "consensus", "wallet", "cache"} {
		if err := os.MkdirAll(filepath.Join(cfg.ZnnDir, d), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(cfg.ZnnDir, d, "old"), []byte("old-"+d), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(cfg.ZnnDir, "config.json"), []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	return cfg
}

func TestRunKeepsPreviousData(t *testing.T) {
	data := makeZip(t, goodEntries())
	srv := host(t, data, sum(data))
	cfg := testConfig(t)
	h := &fakeHost{}
	h.install(t)
	now := time.Unix(1_700_000_000, 0)
	if err := Run(context.Background(), cfg, Options{URL: srv.URL + "/b/snap.zip", Now: func() time.Time { return now }}); err != nil {
		t.Fatal(err)
	}
	if strings.Join(h.calls, ",") != "stop go-zenon,start go-zenon" {
		t.Errorf("calls = %v", h.calls)
	}
	if got, _ := os.ReadFile(filepath.Join(cfg.ZnnDir, "nom", "000001.log")); string(got) != "nom-data" {
		t.Errorf("nom not replaced: %q", got)
	}
	if got, _ := os.ReadFile(filepath.Join(cfg.ZnnDir, "wallet", "old")); string(got) != "old-wallet" {
		t.Error("wallet must be untouched")
	}
	if _, err := os.Stat(filepath.Join(cfg.ZnnDir, "config.json")); err != nil {
		t.Error("config.json must be untouched")
	}
	kept := filepath.Join(cfg.BackupDir, "restore", "nom.bak.1700000000", "old")
	if got, _ := os.ReadFile(kept); string(got) != "old-nom" {
		t.Errorf("previous data not kept at %s: %q", kept, got)
	}
	if _, err := os.Stat(filepath.Join(cfg.BackupDir, "bootstrap", "snap.zip")); err == nil {
		t.Error("archive must be removed on success")
	}
	if entries, _ := os.ReadDir(cfg.ZnnDir); len(entries) != 6 { // nom network consensus wallet cache config.json
		t.Errorf("staging left behind: %v", entries)
	}
	if got, _ := os.ReadFile(filepath.Join(cfg.ZnnDir, "cache", "old")); string(got) != "old-cache" {
		t.Error("cache is not part of the snapshot and must stay in place")
	}
}

func TestRunRollsBackWhenInstallFails(t *testing.T) {
	data := makeZip(t, goodEntries())
	srv := host(t, data, sum(data))
	cfg := testConfig(t)
	h := &fakeHost{}
	h.install(t)
	installDirs = func(config.Config, string) error { return errors.New("rename exploded") }
	err := Run(context.Background(), cfg, Options{URL: srv.URL + "/b/snap.zip"})
	if err == nil || !strings.Contains(err.Error(), "rename exploded") || !strings.Contains(err.Error(), "put back") {
		t.Fatalf("err = %v", err)
	}
	if strings.Join(h.calls, ",") != "stop go-zenon,start go-zenon" {
		t.Errorf("calls = %v", h.calls)
	}
	for _, d := range Dirs {
		if got, _ := os.ReadFile(filepath.Join(cfg.ZnnDir, d, "old")); string(got) != "old-"+d {
			t.Errorf("%s not put back: %q", d, got)
		}
	}
	if entries, _ := os.ReadDir(filepath.Join(cfg.BackupDir, "restore")); len(entries) != 0 {
		t.Errorf("restore dir should be empty after rollback: %v", entries)
	}
	if _, err := os.Stat(filepath.Join(cfg.BackupDir, "bootstrap", "snap.zip")); err != nil {
		t.Error("archive must be kept for a retry")
	}
}

func TestRunKeepsArchiveWhenStartFails(t *testing.T) {
	data := makeZip(t, goodEntries())
	srv := host(t, data, sum(data))
	cfg := testConfig(t)
	h := &fakeHost{startEr: errors.New("unit failed")}
	h.install(t)
	err := Run(context.Background(), cfg, Options{URL: srv.URL + "/b/snap.zip"})
	if err == nil || !strings.Contains(err.Error(), "unit failed") {
		t.Fatalf("err = %v", err)
	}
	if _, err := os.Stat(filepath.Join(cfg.BackupDir, "bootstrap", "snap.zip")); err != nil {
		t.Error("archive must be kept when the service does not start")
	}
	if got, _ := os.ReadFile(filepath.Join(cfg.ZnnDir, "nom", "000001.log")); string(got) != "nom-data" {
		t.Error("snapshot should stay installed")
	}
}

func TestRunDiscardCountsReclaimableSpace(t *testing.T) {
	data := makeZip(t, goodEntries())
	srv := host(t, data, sum(data))
	cfg := testConfig(t)
	h := &fakeHost{}
	h.install(t)
	// Make the old data far larger than the snapshot and leave almost no
	// free space: discard must still go ahead because the delete frees it.
	if err := os.WriteFile(filepath.Join(cfg.ZnnDir, "nom", "big"), bytes.Repeat([]byte("b"), 1<<20), 0o644); err != nil {
		t.Fatal(err)
	}
	diskFree = func(string) (int64, int, error) { return 2, 99, nil } // KB: covers the 1 KB margin only
	if err := Run(context.Background(), cfg, Options{URL: srv.URL + "/b/snap.zip", Discard: true}); err != nil {
		t.Fatal(err)
	}
	h2 := &fakeHost{}
	h2.install(t)
	diskFree = func(string) (int64, int, error) { return 0, 99, nil }
	cfg2 := testConfig(t)
	if err := Run(context.Background(), cfg2, Options{URL: srv.URL + "/b/snap.zip", Discard: true}); err == nil {
		t.Fatal("no space at all must still be refused")
	} else if len(h2.calls) != 0 {
		t.Errorf("service touched before the space check: %v", h2.calls)
	}
}

func TestRunDiscardsPreviousData(t *testing.T) {
	data := makeZip(t, goodEntries())
	srv := host(t, data, sum(data))
	cfg := testConfig(t)
	h := &fakeHost{}
	h.install(t)
	if err := Run(context.Background(), cfg, Options{URL: srv.URL + "/b/snap.zip", Discard: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(cfg.BackupDir, "restore")); err == nil {
		t.Error("discard must not keep a copy")
	}
	if got, _ := os.ReadFile(filepath.Join(cfg.ZnnDir, "consensus", "000002.ldb")); string(got) != "consensus" {
		t.Errorf("consensus not replaced: %q", got)
	}
	if got, _ := os.ReadFile(filepath.Join(cfg.ZnnDir, "wallet", "old")); string(got) != "old-wallet" {
		t.Error("wallet must be untouched")
	}
	if got, _ := os.ReadFile(filepath.Join(cfg.ZnnDir, "cache", "old")); string(got) != "old-cache" {
		t.Error("cache must not be discarded; the snapshot does not replace it")
	}
}

func TestRunRefusesBadChecksumBeforeTouchingNode(t *testing.T) {
	data := makeZip(t, goodEntries())
	srv := host(t, data, strings.Repeat("00", 32))
	cfg := testConfig(t)
	h := &fakeHost{}
	h.install(t)
	err := Run(context.Background(), cfg, Options{URL: srv.URL + "/b/snap.zip"})
	if err == nil || !strings.Contains(err.Error(), "checksum") {
		t.Fatalf("err = %v", err)
	}
	if len(h.calls) != 0 {
		t.Errorf("service touched: %v", h.calls)
	}
	if got, _ := os.ReadFile(filepath.Join(cfg.ZnnDir, "nom", "old")); string(got) != "old-nom" {
		t.Error("data touched")
	}
	if _, err := os.Stat(filepath.Join(cfg.BackupDir, "bootstrap", "snap.zip")); err == nil {
		t.Error("mismatching archive must be removed")
	}
}

func TestRunSkipsDownloadWhenArchivePresent(t *testing.T) {
	data := makeZip(t, goodEntries())
	hits := 0
	mux := http.NewServeMux()
	mux.HandleFunc("/b/snap.zip", func(w http.ResponseWriter, _ *http.Request) { hits++; _, _ = w.Write(data) })
	mux.HandleFunc("/b/snap.hash", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(sum(data))) })
	srv := httptest.NewServer(mux)
	defer srv.Close()
	cfg := testConfig(t)
	h := &fakeHost{}
	h.install(t)
	pre := filepath.Join(cfg.BackupDir, "bootstrap", "snap.zip")
	if err := os.MkdirAll(filepath.Dir(pre), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(pre, data, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := Run(context.Background(), cfg, Options{URL: srv.URL + "/b/snap.zip"}); err != nil {
		t.Fatal(err)
	}
	if hits != 0 {
		t.Errorf("archive downloaded %d times despite a verified local copy", hits)
	}
}

func TestRunMissingSidecar(t *testing.T) {
	data := makeZip(t, goodEntries())
	mux := http.NewServeMux()
	mux.HandleFunc("/b/snap.zip", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write(data) })
	srv := httptest.NewServer(mux)
	defer srv.Close()
	cfg := testConfig(t)
	h := &fakeHost{}
	h.install(t)
	err := Run(context.Background(), cfg, Options{URL: srv.URL + "/b/snap.zip"})
	if err == nil || !strings.Contains(err.Error(), ".hash") {
		t.Fatalf("err = %v", err)
	}
}

func TestRunNoSpace(t *testing.T) {
	data := makeZip(t, goodEntries())
	srv := host(t, data, sum(data))
	cfg := testConfig(t)
	h := &fakeHost{}
	h.install(t)
	diskFree = func(string) (int64, int, error) { return 0, 99, nil }
	err := Run(context.Background(), cfg, Options{URL: srv.URL + "/b/snap.zip"})
	if err == nil || !strings.Contains(err.Error(), "disk space") {
		t.Fatalf("err = %v", err)
	}
	if len(h.calls) != 0 {
		t.Errorf("service touched: %v", h.calls)
	}
}
