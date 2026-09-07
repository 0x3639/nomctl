package alerts

import (
	"errors"
	"fmt"
	"os"
	"os/user"
	"path/filepath"
	"strings"
	"testing"

	"github.com/0x3639/nomctl/internal/config"
)

func TestExecPathQuoting(t *testing.T) {
	if got := execStartPath(`/opt/my "tools"/nomctl`); got != `"/opt/my \"tools\"/nomctl"` {
		t.Errorf("quoted path = %s", got)
	}
	if got := execStartPath("/usr/local/bin/nomctl"); got != "/usr/local/bin/nomctl" {
		t.Errorf("plain path = %s", got)
	}
}

func TestUnitText(t *testing.T) {
	cfg := config.Default()
	cfg.ServiceName, cfg.ZnnDir = "custom-node", "/srv/zenon"
	u := UnitText("/usr/local/bin/nomctl", cfg)
	for _, want := range []string{"ExecStart=/usr/local/bin/nomctl alerts run", "Restart=always", "RuntimeDirectory=nomctl", "WantedBy=multi-user.target", "NOMCTL_SKIP_PREFLIGHT=true",
		`Environment="NOMCTL_SERVICE_NAME=custom-node"`, `Environment="NOMCTL_ZNN_DIR=/srv/zenon"`, `Environment="NOMCTL_BACKUP_DIR=/backup"`} {
		if !strings.Contains(u, want) {
			t.Errorf("unit missing %q:\n%s", want, u)
		}
	}
}

func TestUnitIsUnprivilegedAndHardened(t *testing.T) {
	u := UnitText("/usr/local/bin/nomctl", config.Default())
	for _, want := range []string{"User=nomctl", "Group=nomctl", "NoNewPrivileges=true", "CapabilityBoundingSet=\n", "AmbientCapabilities=\n",
		"ProtectSystem=strict", "ProtectHome=true", "ReadWritePaths=/run/nomctl", "Environment=NOMCTL_LOG_FILE=\n"} {
		if !strings.Contains(u, want) {
			t.Errorf("unit missing %q", want)
		}
	}
	if strings.Contains(u, "CAP_SYS_PTRACE") {
		t.Error("the daemon must not get ptrace")
	}
	p := ProbeText("/usr/local/bin/nomctl", config.Default())
	for _, want := range []string{"Type=oneshot", "ExecStart=/usr/local/bin/nomctl alerts probe", "PrivateNetwork=true", "ReadWritePaths=/run/nomctl", "ProtectHome=true"} {
		if !strings.Contains(p, want) {
			t.Errorf("probe unit missing %q", want)
		}
	}
	if strings.Contains(p, "User=") {
		t.Error("the probe runs as root by design")
	}
	tm := ProbeTimerText()
	if !strings.Contains(tm, "OnUnitActiveSec=30s") || !strings.Contains(tm, "WantedBy=timers.target") {
		t.Errorf("timer: %s", tm)
	}
}

func TestEnsureUserCreatesOnce(t *testing.T) {
	oldLookup, oldAdd := lookupUser, runUseradd
	defer func() { lookupUser, runUseradd = oldLookup, oldAdd }()
	exists := false
	created := 0
	lookupUser = func(name string) (*user.User, error) {
		if !exists {
			return nil, user.UnknownUserError(name)
		}
		return &user.User{Uid: "998", Gid: "997", Username: name}, nil
	}
	runUseradd = func(name string) error { created++; exists = true; return nil }
	uid, gid, err := EnsureUser()
	if err != nil || uid != 998 || gid != 997 || created != 1 {
		t.Fatalf("first: uid=%d gid=%d created=%d err=%v", uid, gid, created, err)
	}
	if _, _, err := EnsureUser(); err != nil || created != 1 {
		t.Fatalf("second: created=%d err=%v", created, err)
	}
	// A lookup failure that is not "unknown user" is an error, not a create.
	lookupUser = func(string) (*user.User, error) { return nil, errors.New("nss down") }
	if _, _, err := EnsureUser(); err == nil || created != 1 {
		t.Fatalf("nss failure: created=%d err=%v", created, err)
	}
}

func TestSecureConfigAndRuntimeDir(t *testing.T) {
	oldChown := chown
	defer func() { chown = oldChown }()
	var chowns []string
	chown = func(path string, uid, gid int) error {
		chowns = append(chowns, fmt.Sprintf("%s %d:%d", filepath.Base(path), uid, gid))
		return nil
	}
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "alerts.json")
	if err := os.WriteFile(cfgPath, []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := SecureConfig(cfgPath, 997); err != nil {
		t.Fatal(err)
	}
	if info, _ := os.Stat(cfgPath); info.Mode().Perm() != 0o640 {
		t.Errorf("mode = %v", info.Mode())
	}
	run := filepath.Join(dir, "run")
	if err := os.MkdirAll(run, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(run, filepath.Base(DefaultStatePath)), []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := SecureRuntimeDir(run, 998, 997); err != nil {
		t.Fatal(err)
	}
	want := "alerts.json 0:997,run 998:997,alerts-state.json 998:997"
	if got := strings.Join(chowns, ","); got != want {
		t.Errorf("chowns = %s, want %s", got, want)
	}
}
