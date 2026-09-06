package support

import (
	"testing"

	"github.com/0x3639/nomctl/internal/metrics"
)

func TestRestartDetector(t *testing.T) {
	d := &restartDetector{initialRestarts: 2, initialPID: 100}
	if ok, _ := d.restarted(metrics.ServiceSample{NRestarts: 2, MainPID: 100}); ok {
		t.Error("unchanged state is not a restart")
	}
	if ok, why := d.restarted(metrics.ServiceSample{NRestarts: 3, MainPID: 100}); !ok || why == "" {
		t.Error("NRestarts growing is a restart")
	}
	d = &restartDetector{initialRestarts: 0, initialPID: 100}
	if ok, _ := d.restarted(metrics.ServiceSample{NRestarts: 0, MainPID: 0}); ok {
		t.Error("process down alone is not yet a restart")
	}
	if ok, _ := d.restarted(metrics.ServiceSample{NRestarts: 0, MainPID: 200}); !ok {
		t.Error("new pid after being down is a restart")
	}
	d = &restartDetector{initialRestarts: 0, initialPID: 100}
	if ok, _ := d.restarted(metrics.ServiceSample{NRestarts: 0, MainPID: 200}); ok {
		t.Error("pid change without seeing it down is ignored (matches the script)")
	}
}
