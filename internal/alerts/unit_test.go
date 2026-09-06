package alerts

import (
	"strings"
	"testing"
)

func TestUnitText(t *testing.T) {
	u := UnitText("/usr/local/bin/nomctl")
	for _, want := range []string{"ExecStart=/usr/local/bin/nomctl alerts run", "Restart=always", "RuntimeDirectory=nomctl", "WantedBy=multi-user.target", "NOMCTL_SKIP_PREFLIGHT=true"} {
		if !strings.Contains(u, want) {
			t.Errorf("unit missing %q:\n%s", want, u)
		}
	}
}
