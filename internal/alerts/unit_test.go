package alerts

import (
	"strings"
	"testing"

	"github.com/0x3639/nomctl/internal/config"
)

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
