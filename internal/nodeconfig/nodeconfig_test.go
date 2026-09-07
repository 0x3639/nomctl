package nodeconfig

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const sample = `{
  "Net": {"ListenPort": 35995, "Seeders": ["enode://a", "enode://b"]},
  "RPC": {"EnableHTTP": true, "HTTPHost": "127.0.0.1"},
  "Producer": {"Index": 0, "KeyFilePath": "producer", "Password": "pw", "Address": "z1abc"},
  "LogLevel": "info"
}`

func TestGetSetUnsetKeys(t *testing.T) {
	d, err := Parse([]byte(sample))
	if err != nil {
		t.Fatal(err)
	}
	want := "LogLevel,Net.ListenPort,Net.Seeders,Producer.Address,Producer.Index,Producer.KeyFilePath,Producer.Password,RPC.EnableHTTP,RPC.HTTPHost"
	if got := strings.Join(d.Keys(), ","); got != want {
		t.Errorf("keys = %s", got)
	}
	if v, ok, _ := d.Get("RPC.HTTPHost"); !ok || v != "127.0.0.1" {
		t.Errorf("get host = %v %v", v, ok)
	}
	if _, ok, _ := d.Get("RPC.WSPort"); ok {
		t.Error("absent key reported present")
	}
	if _, ok, _ := d.Get("Nope.X"); ok {
		t.Error("absent section reported present")
	}
	if err := d.Set("RPC.WSPort", 1234); err != nil {
		t.Fatal(err)
	}
	if err := d.Set("Net.MaxPeers", 80); err != nil {
		t.Fatal(err)
	}
	if err := d.Set("Name", "pillar-1"); err != nil {
		t.Fatal(err)
	}
	if v, _, _ := d.Get("RPC.WSPort"); v != float64(1234) {
		t.Errorf("set/get = %v", v)
	}
	if err := d.Unset("RPC.HTTPHost"); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := d.Get("RPC.HTTPHost"); ok {
		t.Error("unset key still present")
	}
	// Unsetting the last key of a section removes the section.
	if err := d.Unset("RPC.EnableHTTP"); err != nil {
		t.Fatal(err)
	}
	if err := d.Unset("RPC.WSPort"); err != nil {
		t.Fatal(err)
	}
	if _, ok := d.root["RPC"]; ok {
		t.Error("empty section kept")
	}
	// Unknown sections survive untouched.
	d2, _ := Parse([]byte(`{"Custom": {"x": [1, 2]}, "LogLevel": "warn"}`))
	_ = d2.Set("LogLevel", "info")
	out, _ := d2.Bytes()
	var back map[string]json.RawMessage
	_ = json.Unmarshal(out, &back)
	if string(back["Custom"]) == "" {
		t.Error("unknown section dropped")
	}
}

func TestParseValue(t *testing.T) {
	boolS, _ := Lookup("RPC.EnableHTTP")
	intS, _ := Lookup("Net.MaxPeers")
	listS, _ := Lookup("Net.Seeders")
	strS, _ := Lookup("LogLevel")
	for text, want := range map[string]bool{"true": true, "yes": true, "on": true, "1": true, "false": false, "NO": false, "0": false} {
		if v, err := ParseValue(boolS, text); err != nil || v != want {
			t.Errorf("bool %q = %v, %v", text, v, err)
		}
	}
	if _, err := ParseValue(boolS, "maybe"); err == nil {
		t.Error("bad bool accepted")
	}
	if v, err := ParseValue(intS, " 42 "); err != nil || v != 42 {
		t.Errorf("int = %v, %v", v, err)
	}
	if _, err := ParseValue(intS, "4x"); err == nil {
		t.Error("bad int accepted")
	}
	if v, err := ParseValue(listS, "a, b ,c"); err != nil || strings.Join(v.([]string), "|") != "a|b|c" {
		t.Errorf("csv list = %v, %v", v, err)
	}
	if v, err := ParseValue(listS, `["x","y"]`); err != nil || strings.Join(v.([]string), "|") != "x|y" {
		t.Errorf("json list = %v, %v", v, err)
	}
	if v, err := ParseValue(listS, ""); err != nil || len(v.([]string)) != 0 {
		t.Errorf("empty list = %v, %v", v, err)
	}
	if _, err := ParseValue(listS, `[1]`); err == nil {
		t.Error("non-string json list accepted")
	}
	if v, err := ParseValue(strS, "warn"); err != nil || v != "warn" {
		t.Errorf("string = %v, %v", v, err)
	}
}

func TestValidate(t *testing.T) {
	good, _ := Parse([]byte(sample))
	if err := Validate(good); err != nil {
		t.Fatalf("sample rejected: %v", err)
	}
	for name, text := range map[string]string{"unknown key": `{"RPC": {"HttpPort": 1}}`, "unknown section": `{"Rpc": {"HTTPPort": 1}}`} {
		d, _ := Parse([]byte(text))
		if err := Validate(d); err != nil {
			t.Errorf("%s: Validate must tolerate it (%v)", name, err)
		}
		if err := ValidateStrict(d); err == nil || len(UnknownKeys(d)) != 1 {
			t.Errorf("%s: strict must reject it", name)
		}
	}
	cases := map[string]string{
		"port range":       `{"RPC": {"HTTPPort": 70000}}`,
		"port zero":        `{"Net": {"ListenPort": 0}}`,
		"non-integer":      `{"Net": {"MaxPeers": 1.5}}`,
		"negative peers":   `{"Net": {"MinPeers": -1}}`,
		"max below min":    `{"Net": {"MinPeers": 20, "MaxPeers": 10}}`,
		"bad log level":    `{"LogLevel": "loud"}`,
		"bool as string":   `{"RPC": {"EnableHTTP": "true"}}`,
		"empty host":       `{"RPC": {"HTTPHost": " "}}`,
		"list of ints":     `{"Net": {"Seeders": [1, 2]}}`,
		"section not obj":  `{"RPC": 5}`,
		"top-level string": `{"Name": 5}`,
	}
	for name, text := range cases {
		d, err := Parse([]byte(text))
		if err != nil {
			if name == "section not obj" {
				continue
			}
			t.Fatalf("%s: parse: %v", name, err)
		}
		if err := Validate(d); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	// All problems reported together.
	d, _ := Parse([]byte(`{"LogLevel": "loud", "RPC": {"HTTPPort": 0}, "Bogus": 1}`))
	err := ValidateStrict(d)
	for _, want := range []string{"LogLevel", "HTTPPort", "Bogus"} {
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("missing %s in %v", want, err)
		}
	}
	if _, err := Parse([]byte("null")); err == nil {
		t.Error("null accepted")
	}
	if _, err := Parse([]byte("[1]")); err == nil {
		t.Error("array accepted")
	}
}

func TestSaveAndEffective(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	if err := os.WriteFile(path, []byte(sample), 0o644); err != nil {
		t.Fatal(err)
	}
	d, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	_ = d.Set("RPC.HTTPPort", 36000)
	now := time.Unix(1_800_000_000, 0)
	backup, err := Save(path, d, now)
	if err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(backup); string(b) != sample {
		t.Error("backup does not hold the previous file")
	}
	if info, _ := os.Stat(path); info.Mode().Perm() != 0o600 {
		t.Errorf("mode = %v", info.Mode())
	}
	again, _ := Load(path)
	if v, _, _ := again.Get("RPC.HTTPPort"); v != float64(36000) {
		t.Errorf("saved value = %v", v)
	}
	if v, _, _ := again.Get("Producer.Password"); v != "pw" {
		t.Error("producer section lost")
	}
	// An invalid document is refused before anything is written.
	_ = again.Set("LogLevel", "loud")
	if _, err := Save(path, again, now); err == nil {
		t.Error("invalid document saved")
	}
	// Effective values: file wins, else default.
	s, _ := Lookup("RPC.HTTPPort")
	if v, from := Effective(d, s); v != float64(36000) || !from {
		t.Errorf("effective port = %v %v", v, from)
	}
	s, _ = Lookup("Net.MaxPeers")
	if v, from := Effective(d, s); v != 60 || from {
		t.Errorf("effective default = %v %v", v, from)
	}
	if Format([]any{"a", "b"}) != "a,b" || Format(float64(35995)) != "35995" || Format(true) != "true" {
		t.Error("Format")
	}
	// Two saves in the same second keep both backups.
	_ = d.Set("Name", "a")
	b1, err := Save(path, d, now)
	if err != nil {
		t.Fatal(err)
	}
	_ = d.Set("Name", "b")
	b2, err := Save(path, d, now)
	if err != nil || b2 == b1 || !strings.HasPrefix(b2, path+".bak.1800000000-") {
		t.Errorf("same-second backups: %q %q %v", b1, b2, err)
	}
	for _, b := range []string{b1, b2} {
		if _, err := os.Stat(b); err != nil {
			t.Errorf("backup %s missing", b)
		}
	}
	// Missing file loads empty and saves without a backup.
	empty, err := Load(filepath.Join(dir, "none.json"))
	if err != nil || len(empty.Keys()) != 0 {
		t.Fatalf("missing: %v %v", empty.Keys(), err)
	}
	if backup, err := Save(filepath.Join(dir, "none.json"), empty, now); err != nil || backup != "" {
		t.Errorf("save new: %q %v", backup, err)
	}
}
