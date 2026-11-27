//go:build bendlaws

package bend_test

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/0x3639/nomctl/internal/alertproto"
	"github.com/0x3639/nomctl/internal/backup"
	"github.com/0x3639/nomctl/internal/health"
	"github.com/0x3639/nomctl/internal/nodeconfig"
	"github.com/0x3639/nomctl/internal/orchestrator"
	"github.com/0x3639/nomctl/internal/resync"
	"github.com/0x3639/nomctl/internal/service"
)

type vector struct {
	Family string `json:"family"`
	Args   []int  `json:"args"`
	Want   int    `json:"want"`
}

func vectors(t *testing.T, family string, arity, minimum int) []vector {
	t.Helper()
	data, err := os.ReadFile(os.Getenv("BEND_VECTORS"))
	if err != nil {
		t.Fatalf("run make laws to generate fresh Bend results: %v", err)
	}
	var all, selected []vector
	if err := json.Unmarshal(data, &all); err != nil {
		t.Fatal(err)
	}
	for _, v := range all {
		if v.Family == family {
			if len(v.Args) != arity {
				t.Fatalf("malformed %s vector: %+v", family, v)
			}
			selected = append(selected, v)
		}
	}
	if len(selected) < minimum {
		t.Fatalf("only %d %s comparisons; require at least %d", len(selected), family, minimum)
	}
	return selected
}

func compare(t *testing.T, v vector, got int) {
	t.Helper()
	if got != v.Want {
		t.Fatalf("BEND_MISMATCH %s args=%v Go=%d Bend=%d", v.Family, v.Args, got, v.Want)
	}
}

func bit(b bool) int {
	if b {
		return 1
	}
	return 0
}

func TestBendRetention(t *testing.T) {
	t.Parallel()
	for _, v := range vectors(t, "retention", 2, 1000) {
		var infos []backup.Info
		for i := 0; i < v.Args[0]; i++ {
			infos = append(infos, backup.Info{Path: fmt.Sprint(i), ModTime: time.Unix(int64(i), 0)})
		}
		pruned := backup.SelectForPruning(infos, v.Args[1])
		compare(t, v, len(pruned))
		for i, info := range pruned {
			if info.Path != fmt.Sprint(len(pruned)-i-1) {
				t.Fatalf("pruned wrong archive: %+v", pruned)
			}
		}
		for i, info := range infos {
			if info.Path != fmt.Sprint(i) {
				t.Fatal("pruning mutated the caller's archive list")
			}
		}
	}
}

func TestBendServiceStates(t *testing.T) {
	t.Parallel()
	states := []service.State{service.Active, service.Inactive, service.Failed, service.NotFound,
		service.Activating, service.Deactivating, service.Reloading, service.Maintenance, service.Refreshing, service.Unknown}
	for _, v := range vectors(t, "service", 1, len(states)) {
		compare(t, v, bit(states[v.Args[0]].Stopped()))
	}
}

func TestBendDataScope(t *testing.T) {
	t.Parallel()
	names := []string{"nom", "network", "consensus", "cache", "log", "wallet", "config.json", "other"}
	root := t.TempDir()
	for _, name := range names {
		if err := os.Mkdir(filepath.Join(root, name), 0700); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := resync.Wipe(root); err != nil {
		t.Fatal(err)
	}
	for _, v := range vectors(t, "paths", 2, 16) {
		name := names[v.Args[0]]
		if v.Args[1] == 0 {
			included := false
			for _, folder := range backup.Folders {
				included = included || name == folder
			}
			compare(t, v, bit(included))
		} else {
			_, err := os.Stat(filepath.Join(root, name))
			if err != nil && !os.IsNotExist(err) {
				t.Fatal(err)
			}
			compare(t, v, bit(os.IsNotExist(err)))
		}
	}
}

func TestBendHealthOutcomes(t *testing.T) {
	t.Parallel()
	outcomes := []string{"healthy", "syncing", "stalled", "unknown", "failed", "", "unexpected"}
	for _, v := range vectors(t, "health", 1, len(outcomes)) {
		compare(t, v, bit((health.Report{Outcome: outcomes[v.Args[0]]}).Passed()))
	}
}

func TestBendAlertValidation(t *testing.T) {
	t.Parallel()
	names := []string{"service_down", "started", "test", "unknown"}
	states := []alertproto.State{alertproto.Firing, alertproto.OK, alertproto.Info, "unknown"}
	severities := []alertproto.Severity{alertproto.Critical, alertproto.Warning, alertproto.InfoSev, "unknown"}
	lengths := [][2]int{{0, 0}, {200, 2000}, {201, 0}, {0, 2001}, {201, 2001}, {199, 1999}}
	for _, v := range vectors(t, "alerts", 4, 384) {
		a := v.Args
		size := lengths[a[3]]
		request := alertproto.AlertRequest{Alert: names[a[0]], State: states[a[1]], Severity: severities[a[2]],
			Title: strings.Repeat("t", size[0]), Detail: strings.Repeat("d", size[1])}
		compare(t, v, bit(alertproto.ValidAlert(request)))
	}
}

func TestBendConfigDefaults(t *testing.T) {
	t.Parallel()
	for _, v := range vectors(t, "config", 3, 20) {
		a := v.Args
		raw := `{}`
		if a[0] == 1 {
			raw = fmt.Sprintf(`{"RPC":{"HTTPPort":%d}}`, a[1])
		}
		doc, err := nodeconfig.Parse([]byte(raw))
		if err != nil {
			t.Fatal(err)
		}
		setting := nodeconfig.Setting{Key: "RPC.HTTPPort", Kind: nodeconfig.Int, Default: float64(a[2])}
		value, fromFile := nodeconfig.Effective(doc, setting)
		compare(t, v, int(value.(float64)))
		if fromFile != (a[0] == 1) {
			t.Fatal("configuration provenance disagrees with presence")
		}
	}
}

func TestBendOrchestratorSyncScope(t *testing.T) {
	t.Parallel()
	names := []string{"events", "queues", "config.json", "tss", "producer", "unwrap-pool-votes", "other"}
	for _, v := range vectors(t, "orchestrator_sync", 1, len(names)) {
		compare(t, v, bit(orchestrator.IsSyncDir(names[v.Args[0]])))
	}
}
