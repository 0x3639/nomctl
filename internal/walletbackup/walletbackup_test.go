package walletbackup

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/0x3639/nomctl/internal/backup"
	"github.com/0x3639/nomctl/internal/config"
	"github.com/0x3639/nomctl/internal/producer"
)

func node(t *testing.T) (config.Config, string) {
	t.Helper()
	root := t.TempDir()
	cfg := config.Config{ServiceName: "go-zenon", ZnnDir: filepath.Join(root, "znn"), BackupDir: filepath.Join(root, "backup")}
	addr, err := producer.Create(producer.KeyFilePath(cfg.ZnnDir), "kpw")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := producer.WriteConfig(producer.ConfigPath(cfg.ZnnDir), producer.Config{KeyFilePath: "producer", Password: "kpw", Address: addr}, time.Unix(1, 0)); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cfg.ZnnDir, "wallet", "other"), []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	// Chain data and a stray non-regular entry must not be archived.
	if err := os.MkdirAll(filepath.Join(cfg.ZnnDir, "nom"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(cfg.ZnnDir, "wallet", "subdir"), 0o755); err != nil {
		t.Fatal(err)
	}
	return cfg, addr
}

func TestCreateListOpenInspect(t *testing.T) {
	cfg, addr := node(t)
	now := time.Unix(1_800_000_000, 0)
	res, err := Create(cfg, Options{Passphrase: "secret", Now: func() time.Time { return now }})
	if err != nil {
		t.Fatal(err)
	}
	if !res.Encrypted || !strings.HasSuffix(res.Path, EncryptedSuffix) || strings.Join(res.Files, ",") != "config.json,wallet/other,wallet/producer" {
		t.Fatalf("result = %+v", res)
	}
	if st, _ := os.Stat(res.Path); st.Mode().Perm() != 0o600 {
		t.Errorf("mode = %v", st.Mode())
	}
	if !strings.HasPrefix(res.Path, Dir(cfg)) {
		t.Errorf("wrote outside the wallet dir: %s", res.Path)
	}
	raw, _ := os.ReadFile(res.Path)
	if !bytes.HasPrefix(raw, []byte("age-encryption.org/v1")) {
		t.Error("not an age file")
	}
	if bytes.Contains(raw, []byte("kpw")) {
		t.Error("password visible in the encrypted archive")
	}
	infos, err := List(cfg)
	if err != nil || len(infos) != 1 || !infos[0].Encrypted {
		t.Fatalf("list = %+v, %v", infos, err)
	}
	if _, err := Open(res.Path, "nope"); !errors.Is(err, ErrWrongPassphrase) {
		t.Errorf("wrong passphrase: %v", err)
	}
	if _, err := Open(res.Path, ""); err == nil {
		t.Error("empty passphrase accepted for an encrypted archive")
	}
	archive, err := Open(res.Path, "secret")
	if err != nil {
		t.Fatal(err)
	}
	c, err := Inspect(archive)
	if err != nil || c.Producer == nil || c.Producer.Address != addr || c.KeyFile != "wallet/producer" {
		t.Fatalf("contents = %+v, %v", c, err)
	}
	// A tampered archive fails the sidecar check.
	if err := os.WriteFile(res.Path, append(raw, 'x'), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(res.Path, "secret"); err == nil || !strings.Contains(err.Error(), "sha256") {
		t.Errorf("tampered archive: %v", err)
	}
	// Plain archives work and are marked as such.
	plain, err := Create(cfg, Options{Plain: true, Now: func() time.Time { return now.Add(time.Second) }})
	if err != nil || plain.Encrypted || !strings.HasSuffix(plain.Path, PlainSuffix) {
		t.Fatalf("plain = %+v, %v", plain, err)
	}
	if _, err := Create(cfg, Options{}); err == nil {
		t.Error("no passphrase and not plain must fail")
	}
	// The chain-data retention rule never sees wallet archives.
	if chain, _ := backup.List(cfg); len(chain) != 0 {
		t.Errorf("chain backup list sees wallet archives: %+v", chain)
	}
}

func TestInspectRejectsHostileEntries(t *testing.T) {
	mk := func(entries map[string]string, typ byte) []byte {
		var buf bytes.Buffer
		gz := gzip.NewWriter(&buf)
		tw := tar.NewWriter(gz)
		for name, content := range entries {
			h := &tar.Header{Name: name, Mode: 0o600, Size: int64(len(content)), Typeflag: typ}
			_ = tw.WriteHeader(h)
			_, _ = tw.Write([]byte(content))
		}
		_ = tw.Close()
		_ = gz.Close()
		return buf.Bytes()
	}
	for name, entries := range map[string]map[string]string{
		"traversal":  {"wallet/../../etc/passwd": "x"},
		"absolute":   {"/root/.znn/wallet/producer": "x"},
		"chain data": {"nom/000001.log": "x"},
		"nested":     {"wallet/sub/key": "x"},
		"stray":      {"notes.txt": "x"},
	} {
		if _, err := Inspect(mk(entries, tar.TypeReg)); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	if _, err := Inspect(mk(map[string]string{"wallet/link": ""}, tar.TypeSymlink)); err == nil {
		t.Error("symlink accepted")
	}
	if _, err := Inspect(mk(map[string]string{}, tar.TypeReg)); err == nil {
		t.Error("empty archive accepted")
	}
	if _, err := Inspect([]byte("garbage")); err == nil {
		t.Error("garbage accepted")
	}
	// Accepted shapes, with or without "./".
	if c, err := Inspect(mk(map[string]string{"./wallet/producer": "{}", "./config.json": "{}"}, tar.TypeReg)); err != nil || len(c.Files) != 2 {
		t.Errorf("dot-prefixed: %+v, %v", c, err)
	}
}

func TestRestore(t *testing.T) {
	cfg, addr := node(t)
	res, err := Create(cfg, Options{Passphrase: "secret"})
	if err != nil {
		t.Fatal(err)
	}
	archive, err := Open(res.Path, "secret")
	if err != nil {
		t.Fatal(err)
	}
	// Simulate a fresh node: wipe wallet and config, keep chain data.
	_ = os.RemoveAll(filepath.Join(cfg.ZnnDir, "wallet"))
	_ = os.Remove(producer.ConfigPath(cfg.ZnnDir))
	if err := os.WriteFile(producer.ConfigPath(cfg.ZnnDir), []byte(`{"LogLevel": "warn"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	calls := 0
	SetServiceHooks(func(string) bool { return true }, func(string) error { calls++; return nil })
	t.Cleanup(func() { SetServiceHooks(func(string) bool { return false }, func(string) error { return nil }) })
	now := time.Unix(1_800_000_000, 0)
	rr, err := Restore(cfg, archive, true, now)
	if err != nil {
		t.Fatal(err)
	}
	if rr.Address != addr || !rr.Restarted || !rr.WasRunning || calls != 1 {
		t.Fatalf("result = %+v calls=%d", rr, calls)
	}
	if got, err := producer.Verify(producer.KeyFilePath(cfg.ZnnDir), "kpw"); err != nil || got != addr {
		t.Errorf("restored key: %v %v", got, err)
	}
	pc, _ := producer.ReadConfig(producer.ConfigPath(cfg.ZnnDir))
	if pc == nil || pc.Address != addr {
		t.Errorf("restored config: %+v", pc)
	}
	if b, _ := os.ReadFile(filepath.Join(rr.SafetyDir, "config.json")); !strings.Contains(string(b), "warn") {
		t.Error("previous config.json not kept aside")
	}
	if _, err := os.Stat(filepath.Join(cfg.ZnnDir, "nom")); err != nil {
		t.Error("chain data touched")
	}
	entries, _ := os.ReadDir(cfg.ZnnDir)
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".wallet-restore-") {
			t.Error("staging left behind")
		}
	}
	// An archive whose config names a key that is missing, or whose key does
	// not match, is refused before anything moves.
	broken := tarOf(t, map[string]string{"config.json": `{"Producer": {"Index": 0, "KeyFilePath": "producer", "Password": "kpw", "Address": "z1other"}}`})
	if _, err := Restore(cfg, broken, false, now); err == nil || !strings.Contains(err.Error(), "does not contain") {
		t.Errorf("missing key: %v", err)
	}
	keyData, _ := os.ReadFile(producer.KeyFilePath(cfg.ZnnDir))
	mismatch := tarOf(t, map[string]string{"wallet/producer": string(keyData), "config.json": `{"Producer": {"Index": 0, "KeyFilePath": "producer", "Password": "kpw", "Address": "z1other"}}`})
	if _, err := Restore(cfg, mismatch, false, now); err == nil || !strings.Contains(err.Error(), "expects z1other") {
		t.Errorf("address mismatch: %v", err)
	}
	if pc, _ := producer.ReadConfig(producer.ConfigPath(cfg.ZnnDir)); pc == nil || pc.Address != addr {
		t.Error("live config changed by a refused restore")
	}
}

func tarOf(t *testing.T, entries map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for name, content := range entries {
		if err := tw.WriteHeader(&tar.Header{Name: name, Mode: 0o600, Size: int64(len(content)), Typeflag: tar.TypeReg}); err != nil {
			t.Fatal(err)
		}
		_, _ = tw.Write([]byte(content))
	}
	_ = tw.Close()
	_ = gz.Close()
	return buf.Bytes()
}

func TestCheckMissing(t *testing.T) {
	cfg, _ := node(t)
	s := Check(cfg)
	if !s.ProducerConfigured || !s.Missing() {
		t.Fatalf("fresh producer should be missing a backup: %+v", s)
	}
	if _, err := Create(cfg, Options{Passphrase: "x"}); err != nil {
		t.Fatal(err)
	}
	if s := Check(cfg); s.Missing() {
		t.Errorf("backed up but still missing: %+v", s)
	}
	// A key rotated after the backup is missing again.
	future := time.Now().Add(time.Hour)
	if err := os.Chtimes(producer.KeyFilePath(cfg.ZnnDir), future, future); err != nil {
		t.Fatal(err)
	}
	if s := Check(cfg); !s.Missing() {
		t.Errorf("rotated key should be missing a backup: %+v", s)
	}
	// No producer: nothing to report.
	if s := Check(config.Config{ZnnDir: t.TempDir(), BackupDir: t.TempDir()}); s.ProducerConfigured || s.Missing() {
		t.Errorf("no producer: %+v", s)
	}
}
