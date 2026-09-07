package producer

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"
)

func TestGeneratePassword(t *testing.T) {
	seen := map[string]bool{}
	for range 20 {
		p, err := GeneratePassword()
		if err != nil || len(p) != PasswordLength || !regexp.MustCompile(`^[A-Za-z0-9]+$`).MatchString(p) {
			t.Fatalf("password %q, %v", p, err)
		}
		seen[p] = true
	}
	if len(seen) != 20 {
		t.Error("passwords repeat")
	}
}

func TestCreateVerifyAddress(t *testing.T) {
	dir := t.TempDir()
	path := KeyFilePath(dir)
	addr, err := Create(path, "s3cret")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(addr, "z1") || len(addr) != 40 {
		t.Errorf("address = %q", addr)
	}
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Errorf("key file mode = %v, %v", info.Mode(), err)
	}
	var kf map[string]any
	data, _ := os.ReadFile(path)
	if err := json.Unmarshal(data, &kf); err != nil || kf["baseAddress"] != addr {
		t.Errorf("key file content: %v %v", kf["baseAddress"], err)
	}
	if got, err := Verify(path, "s3cret"); err != nil || got != addr {
		t.Errorf("Verify = %q, %v", got, err)
	}
	if _, err := Verify(path, "nope"); !errors.Is(err, ErrWrongPassword) {
		t.Errorf("wrong password: %v", err)
	}
	if got, err := Address(path); err != nil || got != addr {
		t.Errorf("Address = %q, %v", got, err)
	}
	if _, err := Create(path, "again"); err == nil {
		t.Error("overwrite allowed")
	}
	if _, err := Verify(filepath.Join(dir, "missing"), "x"); err == nil {
		t.Error("missing file accepted")
	}
}

func TestConfigRoundTrip(t *testing.T) {
	dir := t.TempDir()
	cfgPath := ConfigPath(dir)
	// Absent file: no producer, and writing creates it with only Producer.
	if c, err := ReadConfig(cfgPath); err != nil || c != nil {
		t.Fatalf("absent: %+v, %v", c, err)
	}
	now := time.Unix(1_800_000_000, 0)
	c := Config{Index: 0, KeyFilePath: "producer", Password: "pw", Address: "z1abc"}
	backup, err := WriteConfig(cfgPath, c, now)
	if err != nil || backup != "" {
		t.Fatalf("first write: backup=%q err=%v", backup, err)
	}
	if info, _ := os.Stat(cfgPath); info.Mode().Perm() != 0o600 {
		t.Errorf("config mode = %v", info.Mode())
	}
	got, err := ReadConfig(cfgPath)
	if err != nil || got == nil || *got != c {
		t.Fatalf("read back: %+v, %v", got, err)
	}
	// Existing file with other sections and odd formatting: preserved.
	original := "{\n  \"Net\": {\"ListenPort\": 35995, \"Seeders\": [\"a\", \"b\"]},\n  \"RPC\": {\"EnableHTTP\": true},\n  \"Producer\": null\n}\n"
	if err := os.WriteFile(cfgPath, []byte(original), 0o644); err != nil {
		t.Fatal(err)
	}
	if c0, err := ReadConfig(cfgPath); err != nil || c0 != nil {
		t.Fatalf("null section: %+v, %v", c0, err)
	}
	backup, err = WriteConfig(cfgPath, c, now)
	if err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(backup); string(b) != original {
		t.Errorf("backup %s does not hold the previous file", backup)
	}
	data, _ := os.ReadFile(cfgPath)
	var all map[string]json.RawMessage
	if err := json.Unmarshal(data, &all); err != nil {
		t.Fatal(err)
	}
	var net struct {
		ListenPort int
		Seeders    []string
	}
	var rpc struct{ EnableHTTP bool }
	if json.Unmarshal(all["Net"], &net) != nil || net.ListenPort != 35995 || len(net.Seeders) != 2 || json.Unmarshal(all["RPC"], &rpc) != nil || !rpc.EnableHTTP {
		t.Errorf("other sections changed: %s", data)
	}
	if got, _ := ReadConfig(cfgPath); got == nil || got.Address != "z1abc" {
		t.Errorf("producer not written: %+v", got)
	}
	// Garbage is refused, not clobbered.
	if err := os.WriteFile(cfgPath, []byte("not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := WriteConfig(cfgPath, c, now); err == nil {
		t.Error("garbage config overwritten")
	}
}
