package config

import (
	"strings"
	"testing"
)

func envOf(m map[string]string) func(string) (string, bool) {
	return func(k string) (string, bool) {
		v, ok := m[k]
		return v, ok
	}
}

func TestDefaults(t *testing.T) {
	c, err := LoadFrom(envOf(nil))
	if err != nil {
		t.Fatal(err)
	}
	if c.RepoURL != DefaultRepoURL || c.BranchName != "master" || c.ServiceName != "go-zenon" || c.BinaryName != "znnd" {
		t.Errorf("unexpected node defaults: %+v", c)
	}
	if c.BackupDir != "/backup" || c.MaxBackups != 7 || c.BackupCadenceDays != 0 || c.BackupHour != -1 {
		t.Errorf("unexpected backup defaults: %+v", c)
	}
	if c.MinFreeSpaceKB != 15728640 {
		t.Errorf("min free space = %d, want 15 GB in KB", c.MinFreeSpaceKB)
	}
	if c.Debug || c.SkipPreflight {
		t.Errorf("debug/skip-preflight should default to false")
	}
}

func TestEnvOverrides(t *testing.T) {
	c, err := LoadFrom(envOf(map[string]string{
		"NOMCTL_DEBUG":               "true",
		"NOMCTL_REPO_URL":            " https://example.com/x.git ",
		"NOMCTL_MAX_BACKUPS":         "3",
		"NOMCTL_BACKUP_CADENCE_DAYS": "7",
		"NOMCTL_BACKUP_HOUR":         "5",
		"NOMCTL_MIN_FREE_SPACE_KB":   "1024",
		"NOMCTL_ZNN_DIR":             "/data/.znn",
		"NOMCTL_BACKUP_DIR":          "",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if !c.Debug || c.RepoURL != "https://example.com/x.git" || c.MaxBackups != 3 || c.BackupCadenceDays != 7 || c.BackupHour != 5 || c.MinFreeSpaceKB != 1024 || c.ZnnDir != "/data/.znn" {
		t.Errorf("overrides not applied: %+v", c)
	}
	if c.BackupDir != DefaultBackupDir {
		t.Errorf("empty env value should keep default, got %q", c.BackupDir)
	}
}

func TestInvalidValues(t *testing.T) {
	unparsable := map[string]map[string]string{
		"bad bool": {"NOMCTL_DEBUG": "maybe"},
		"bad int":  {"NOMCTL_MAX_BACKUPS": "seven"},
	}
	for name, env := range unparsable {
		if _, err := LoadFrom(envOf(env)); err == nil {
			t.Errorf("%s: expected error", name)
		}
	}
	outOfRange := map[string]map[string]string{
		"zero max":    {"NOMCTL_MAX_BACKUPS": "0"},
		"neg cadence": {"NOMCTL_BACKUP_CADENCE_DAYS": "-1"},
		"hour 24":     {"NOMCTL_BACKUP_HOUR": "24"},
	}
	for name, env := range outOfRange {
		c, err := LoadFrom(envOf(env))
		if err != nil {
			t.Errorf("%s: load must not fail, flags may still override: %v", name, err)
		}
		if err := c.Validate(); err == nil {
			t.Errorf("%s: Validate should reject", name)
		}
	}
	// A flag override repairs an out-of-range environment value.
	c, _ := LoadFrom(envOf(map[string]string{"NOMCTL_MAX_BACKUPS": "0"}))
	c.MaxBackups = 3
	if err := c.Validate(); err != nil {
		t.Errorf("override should validate: %v", err)
	}
}

func TestRedacted(t *testing.T) {
	c := Default()
	c.GrafanaAdminPassword = "hunter2"
	if s := c.Redacted(); strings.Contains(s, "hunter2") || !strings.Contains(s, "***") {
		t.Errorf("password not redacted: %s", s)
	}
}

func TestDerivedPaths(t *testing.T) {
	c := Default()
	if c.GoBinary() != "/opt/nomctl/go/bin/go" {
		t.Errorf("GoBinary = %q", c.GoBinary())
	}
	if c.SourceDir() != "/opt/nomctl/go-zenon" {
		t.Errorf("SourceDir = %q", c.SourceDir())
	}
	if c.ServiceUnitPath() != "/etc/systemd/system/go-zenon.service" {
		t.Errorf("ServiceUnitPath = %q", c.ServiceUnitPath())
	}
	if c.BinaryPath() != "/usr/local/bin/znnd" {
		t.Errorf("BinaryPath = %q", c.BinaryPath())
	}
	u, err := c.GoURL()
	if err != nil {
		t.Skip("unsupported host arch")
	}
	if !strings.HasPrefix(u, "https://go.dev/dl/go1.23.0.linux-") || !strings.HasSuffix(u, ".tar.gz") {
		t.Errorf("GoURL = %q", u)
	}
}

func TestVarsCoverEveryField(t *testing.T) {
	names := map[string]bool{}
	for _, v := range Vars() {
		if !strings.HasPrefix(v.Name, EnvPrefix) {
			t.Errorf("%s lacks prefix", v.Name)
		}
		names[v.Name] = true
	}
	if len(names) != 22 {
		t.Errorf("expected 22 documented variables, got %d", len(names))
	}
}
