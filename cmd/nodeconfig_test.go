package cmd

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/0x3639/nomctl/internal/nodeconfig"
	"github.com/0x3639/nomctl/internal/producer"
)

func TestSetConfigValue(t *testing.T) {
	dir := t.TempDir()
	path := producer.ConfigPath(dir)
	if err := os.WriteFile(path, []byte(`{"Producer": {"Address": "z1", "Index": 0, "KeyFilePath": "producer", "Password": "pw"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := setConfigValue(dir, "RPC.HTTPPort", "36000", false); err != nil {
		t.Fatal(err)
	}
	d, _ := nodeconfig.Load(path)
	if v, _, _ := d.Get("RPC.HTTPPort"); v != float64(36000) {
		t.Errorf("port = %v", v)
	}
	if v, _, _ := d.Get("Producer.Password"); v != "pw" {
		t.Error("producer section lost")
	}
	if _, err := setConfigValue(dir, "RPC.HTTPPort", "99999", false); err == nil || !strings.Contains(err.Error(), "port") {
		t.Errorf("out-of-range port: %v", err)
	}
	if _, err := setConfigValue(dir, "Producer.Password", "x", false); err == nil || !strings.Contains(err.Error(), "pillar setup") {
		t.Errorf("reserved key: %v", err)
	}
	if _, err := setConfigValue(dir, "RPC.HttpPort", "1", false); err == nil || !strings.Contains(err.Error(), "--force") {
		t.Errorf("unknown key: %v", err)
	}
	if _, err := setConfigValue(dir, "RPC.HttpPort", "1", true); err != nil {
		t.Errorf("forced unknown key: %v", err)
	}
	if _, err := setConfigValue(dir, "Producer.Custom", "x", true); err == nil || !strings.Contains(err.Error(), "pillar setup") {
		t.Errorf("--force must not enter the Producer section: %v", err)
	}
	if _, err := setConfigValue(dir, "Net.Seeders", "enode://a,enode://b", false); err != nil {
		t.Fatal(err)
	}
	d, _ = nodeconfig.Load(path)
	if v, _, _ := d.Get("Net.Seeders"); nodeconfig.Format(v) != "enode://a,enode://b" {
		t.Errorf("seeders = %v", v)
	}
	backups, _ := filepath.Glob(path + ".bak.*")
	if len(backups) == 0 {
		t.Error("no backup written")
	}
}
