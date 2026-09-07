package restore

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/0x3639/nomctl/internal/backup"
	"github.com/0x3639/nomctl/internal/config"
)

type entry struct {
	name, content, link string
	typ                 byte
}

func file(name, content string) entry { return entry{name: name, content: content, typ: tar.TypeReg} }
func dir(name string) entry           { return entry{name: name, typ: tar.TypeDir} }

// tgz builds an archive the way `tar -czf x .` does: names start with "./".
func tgz(t *testing.T, entries ...entry) string {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for _, e := range entries {
		h := &tar.Header{Name: e.name, Typeflag: e.typ, Mode: 0o644, Size: int64(len(e.content)), Linkname: e.link}
		if e.typ == tar.TypeDir {
			h.Mode = 0o755
		}
		if err := tw.WriteHeader(h); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write([]byte(e.content)); err != nil {
			t.Fatal(err)
		}
	}
	_ = tw.Close()
	_ = gz.Close()
	p := filepath.Join(t.TempDir(), "b.tar.gz")
	if err := os.WriteFile(p, buf.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
	sum, err := backup.SHA256File(p)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(backup.HashPath(p), []byte(sum+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func good() []entry {
	return []entry{dir("./"), dir("./nom/"), file("./nom/000001.log", "nom"), dir("./nom/sub/"), file("./nom/sub/CURRENT", "c"),
		dir("./network/"), file("./network/nodes.json", "[]"), dir("./consensus/"), file("./consensus/x.ldb", "cs")}
}

func TestInspectAndExtract(t *testing.T) {
	a := tgz(t, good()...)
	m, err := Inspect(a)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(m.Folders, ",") != "nom,network,consensus" || m.Files != 4 || m.Uncompressed != int64(len("nom")+1+2+2) {
		t.Errorf("manifest = %+v", m)
	}
	dst := t.TempDir()
	if err := Extract(a, dst); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(filepath.Join(dst, "nom", "sub", "CURRENT")); string(got) != "c" {
		t.Errorf("nested file = %q", got)
	}
	// Names without the leading "./" are accepted too.
	if _, err := Inspect(tgz(t, dir("nom/"), file("nom/a", "x"))); err != nil {
		t.Errorf("plain names: %v", err)
	}
}

func TestInspectRefusesEverythingOutsideChainData(t *testing.T) {
	cases := map[string][]entry{
		"config.json":  append(good(), file("./config.json", `{"evil":1}`)),
		"wallet":       append(good(), dir("./wallet/"), file("./wallet/keys", "k")),
		"symlink":      append(good(), entry{name: "./nom/link", typ: tar.TypeSymlink, link: "/etc/passwd"}),
		"hardlink":     append(good(), entry{name: "./nom/hl", typ: tar.TypeLink, link: "nom/000001.log"}),
		"device":       append(good(), entry{name: "./nom/dev", typ: tar.TypeChar}),
		"traversal":    append(good(), file("./nom/../../etc/cron.d/x", "*")),
		"absolute":     append(good(), file("/root/.znn/config.json", "{}")),
		"root file":    append(good(), file("./notes.txt", "x")),
		"empty":        {dir("./")},
		"unknown root": {dir("./stuff/"), file("./stuff/a", "x")},
	}
	for name, entries := range cases {
		a := tgz(t, entries...)
		if _, err := Inspect(a); err == nil {
			t.Errorf("%s: accepted", name)
		}
		if name != "empty" && name != "unknown root" {
			if err := Extract(a, t.TempDir()); err == nil {
				t.Errorf("%s: extracted", name)
			}
		}
	}
	if _, err := Inspect(tgz(t)); err == nil {
		t.Error("archive with no entries accepted")
	}
	if err := os.WriteFile(filepath.Join(t.TempDir(), "x"), []byte("nope"), 0o644); err != nil {
		t.Fatal(err)
	}
}

func testConfig(t *testing.T) config.Config {
	t.Helper()
	root := t.TempDir()
	cfg := config.Config{ServiceName: "go-zenon", ZnnDir: filepath.Join(root, "znn"), BackupDir: filepath.Join(root, "backup")}
	for _, d := range []string{"nom", "network", "consensus", "cache", "wallet"} {
		if err := os.MkdirAll(filepath.Join(cfg.ZnnDir, d), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(cfg.ZnnDir, d, "old"), []byte("old-"+d), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(cfg.ZnnDir, "config.json"), []byte(`{"mine":true}`), 0o644); err != nil {
		t.Fatal(err)
	}
	return cfg
}

func stubService(t *testing.T) *[]string {
	t.Helper()
	var calls []string
	oldStop, oldStart := stopService, startService
	stopService = func(n string) error { calls = append(calls, "stop"); return nil }
	startService = func(n string) error { calls = append(calls, "start"); return nil }
	t.Cleanup(func() { stopService, startService = oldStop, oldStart })
	return &calls
}

func TestRunReplacesOnlyArchivedFolders(t *testing.T) {
	cfg := testConfig(t)
	calls := stubService(t)
	a := tgz(t, good()...)
	if err := Run(cfg, a); err != nil {
		t.Fatal(err)
	}
	if strings.Join(*calls, ",") != "stop,start" {
		t.Errorf("calls = %v", *calls)
	}
	if got, _ := os.ReadFile(filepath.Join(cfg.ZnnDir, "nom", "000001.log")); string(got) != "nom" {
		t.Errorf("nom not restored: %q", got)
	}
	// cache was not in the archive: left in place, not moved aside.
	if got, _ := os.ReadFile(filepath.Join(cfg.ZnnDir, "cache", "old")); string(got) != "old-cache" {
		t.Error("cache must stay when the archive lacks it")
	}
	for _, keep := range []string{"wallet/old", "config.json"} {
		if _, err := os.Stat(filepath.Join(cfg.ZnnDir, keep)); err != nil {
			t.Errorf("%s must be untouched", keep)
		}
	}
	entries, _ := os.ReadDir(filepath.Join(cfg.BackupDir, "restore"))
	if len(entries) != 3 {
		t.Errorf("expected 3 safety copies, got %d", len(entries))
	}
	znn, _ := os.ReadDir(cfg.ZnnDir)
	for _, e := range znn {
		if strings.HasPrefix(e.Name(), ".restore-") {
			t.Error("staging left behind")
		}
	}
}

func TestRunRefusesHostileArchiveBeforeStopping(t *testing.T) {
	cfg := testConfig(t)
	calls := stubService(t)
	a := tgz(t, append(good(), file("./config.json", `{"evil":1}`))...)
	err := Run(cfg, a)
	if err == nil || !strings.Contains(err.Error(), "outside the chain-data folders") {
		t.Fatalf("err = %v", err)
	}
	if len(*calls) != 0 {
		t.Errorf("service touched: %v", *calls)
	}
	if got, _ := os.ReadFile(filepath.Join(cfg.ZnnDir, "config.json")); string(got) != `{"mine":true}` {
		t.Errorf("config.json changed: %s", got)
	}
	if got, _ := os.ReadFile(filepath.Join(cfg.ZnnDir, "nom", "old")); string(got) != "old-nom" {
		t.Error("chain data changed")
	}
	if _, err := os.Stat(filepath.Join(cfg.BackupDir, "restore")); err == nil {
		if entries, _ := os.ReadDir(filepath.Join(cfg.BackupDir, "restore")); len(entries) != 0 {
			t.Error("nothing should have been moved aside")
		}
	}
	if !errors.Is(Run(cfg, filepath.Join(t.TempDir(), "missing.tar.gz")), os.ErrNotExist) {
		// Verify reports its own message; just make sure the service is untouched.
		if len(*calls) != 0 {
			t.Errorf("service touched: %v", *calls)
		}
	}
}
