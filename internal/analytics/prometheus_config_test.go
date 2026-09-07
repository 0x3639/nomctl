package analytics

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"go.yaml.in/yaml/v3"
)

func TestNodeScrapeJobPreservesExistingJobs(t *testing.T) {
	for _, name := range []string{"node", "'node'", `"node"`} {
		t.Run(name, func(t *testing.T) {
			current := []byte("# existing configuration\nscrape_configs:\n  - job_name: " + name + "\n    static_configs: [{targets: ['custom:9100']}]\n")
			got, changed, err := withNodeScrapeJob(current)
			if err != nil || changed || !bytes.Equal(got, current) {
				t.Fatalf("existing job changed: changed=%v err=%v\n%s", changed, err, got)
			}
		})
	}
}

func TestNodeScrapeJobEditsOnlyScrapeSequence(t *testing.T) {
	current := []byte("# keep this comment\nglobal:\n  scrape_interval: 15s\nscrape_configs:\n  - job_name: prometheus\n    static_configs: [{targets: ['localhost:9090']}]\nremote_write:\n  - url: https://metrics.example.invalid/write\n")
	got, changed, err := withNodeScrapeJob(current)
	if err != nil || !changed {
		t.Fatalf("add job: changed=%v err=%v", changed, err)
	}
	var parsed struct {
		Scrape []struct {
			Name string `yaml:"job_name"`
		} `yaml:"scrape_configs"`
		Remote []map[string]any `yaml:"remote_write"`
	}
	if err := yaml.Unmarshal(got, &parsed); err != nil {
		t.Fatal(err)
	}
	if len(parsed.Scrape) != 2 || parsed.Scrape[0].Name != "prometheus" || parsed.Scrape[1].Name != "node" {
		t.Fatalf("scrape jobs: %+v", parsed.Scrape)
	}
	if len(parsed.Remote) != 1 || len(parsed.Remote[0]) != 1 || !bytes.Contains(got, []byte("# keep this comment")) {
		t.Fatalf("unrelated content changed:\n%s", got)
	}
	again, changed, err := withNodeScrapeJob(got)
	if err != nil || changed || !bytes.Equal(again, got) {
		t.Fatalf("second application not idempotent: %v %v", changed, err)
	}
}

func TestNodeScrapeJobMissingOrNullSequence(t *testing.T) {
	for _, current := range []string{"global: {scrape_interval: 15s}\n", "scrape_configs: null\n", "scrape_configs: []\n"} {
		got, changed, err := withNodeScrapeJob([]byte(current))
		if err != nil || !changed || !bytes.Contains(got, []byte("job_name: node")) {
			t.Fatalf("input %q: changed=%v err=%v\n%s", current, changed, err, got)
		}
	}
}

func TestNodeScrapeJobRefusesAmbiguousLayouts(t *testing.T) {
	for _, current := range []string{
		"scrape_configs: {}\n",
		"scrape_configs: []\nscrape_configs: []\n",
		"scrape_configs: []\n---\nscrape_configs: []\n",
		"shared: &jobs [{job_name: prometheus}]\nscrape_configs: *jobs\n",
		"scrape_configs: &jobs [{job_name: prometheus}]\nother: *jobs\n",
		"shared: &settings {scrape_configs: []}\n<<: *settings\n",
		"- job_name: node\n",
	} {
		if _, _, err := withNodeScrapeJob([]byte(current)); err == nil {
			t.Errorf("accepted ambiguous config %q", current)
		}
	}
}

func TestPrometheusConfigValidationAndRecovery(t *testing.T) {
	for _, scenario := range []string{"validation failure", "activation failure", "recovery failure", "success"} {
		t.Run(scenario, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "prometheus.yml")
			original := []byte("scrape_configs: []\nremote_write: []\n")
			if err := os.WriteFile(path, original, 0o640); err != nil {
				t.Fatal(err)
			}
			activated, recovered := false, false
			validate := func(candidate string) error {
				live, err := os.ReadFile(path)
				if err != nil || !bytes.Equal(live, original) || candidate == path || filepath.Dir(candidate) != dir {
					t.Fatalf("validation must precede publication: %v", err)
				}
				if scenario == "validation failure" {
					return errors.New("invalid config")
				}
				return nil
			}
			activate := func(changed bool) error {
				activated = true
				live, err := os.ReadFile(path)
				if err != nil || !changed || !bytes.Contains(live, []byte("job_name: node")) {
					t.Fatalf("activation before publication: %v", err)
				}
				if scenario != "success" {
					return errors.New("service did not start")
				}
				return nil
			}
			recoverService := func() error {
				recovered = true
				live, err := os.ReadFile(path)
				if err != nil || !bytes.Equal(live, original) {
					t.Fatalf("recovery must use original config: %v", err)
				}
				if scenario == "recovery failure" {
					return errors.New("service recovery failed")
				}
				return nil
			}
			err := configurePrometheus(path, validate, activate, recoverService)
			if (err == nil) != (scenario == "success") {
				t.Fatalf("result: %v", err)
			}
			if scenario == "validation failure" && activated {
				t.Fatal("invalid configuration activated")
			}
			if (scenario == "activation failure" || scenario == "recovery failure") && !recovered {
				t.Fatal("old service state not recovered")
			}
			if scenario == "recovery failure" && !strings.Contains(err.Error(), "service recovery failed") {
				t.Fatalf("recovery error lost: %v", err)
			}
			live, _ := os.ReadFile(path)
			if scenario != "success" && !bytes.Equal(live, original) {
				t.Fatal("original config changed on failure")
			}
			info, _ := os.Stat(path)
			if info.Mode().Perm() != 0o640 {
				t.Fatalf("mode changed: %v", info.Mode())
			}
			entries, _ := os.ReadDir(dir)
			if len(entries) != 1 {
				t.Fatalf("staging files remain: %v", entries)
			}
		})
	}
}

func TestGeneratedMonitoringUnitsMigrateToLoopback(t *testing.T) {
	for _, unit := range []struct{ name, current, old string }{
		{"exporter", NodeExporterUnit(), strings.Replace(NodeExporterUnit(), " --web.listen-address=127.0.0.1:9100", "", 1)},
		{"prometheus", PrometheusUnit(), strings.Replace(PrometheusUnit(), "  --web.listen-address=127.0.0.1:9090 \\\n", "", 1)},
	} {
		t.Run(unit.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "monitor.service")
			if err := os.WriteFile(path, []byte(unit.old), 0o644); err != nil {
				t.Fatal(err)
			}
			changed, err := ensureUnitFile(path, unit.current, unit.old)
			if err != nil || !changed {
				t.Fatalf("migration: %v %v", changed, err)
			}
			got, _ := os.ReadFile(path)
			if string(got) != unit.current || !strings.Contains(string(got), "--web.listen-address=127.0.0.1:") {
				t.Fatalf("unit not migrated: %s", got)
			}
			if changed, err := ensureUnitFile(path, unit.current, unit.old); err != nil || changed {
				t.Fatalf("unit migration not idempotent: %v %v", changed, err)
			}
			custom := unit.current + "\n# operator-managed setting\n"
			if err := os.WriteFile(path, []byte(custom), 0o644); err != nil {
				t.Fatal(err)
			}
			if changed, err := ensureUnitFile(path, unit.current, unit.old); err != nil || changed {
				t.Fatalf("custom unit overwritten: %v %v", changed, err)
			}
		})
	}
}

func TestMonitoringActivationReappliesUnchangedUnit(t *testing.T) {
	for _, active := range []bool{false, true} {
		t.Run(map[bool]string{false: "stopped", true: "active"}[active], func(t *testing.T) {
			dir := t.TempDir()
			calls := filepath.Join(dir, "calls")
			script := `#!/bin/sh
printf '%s\n' "$*" >> "$MONITOR_TEST_CALLS"
case "$1" in
  is-enabled) exit 0 ;;
  is-active) [ "$MONITOR_TEST_ACTIVE" = yes ] && exit 0; exit 3 ;;
  show) printf 'LoadState=loaded\nActiveState=%s\n' "$MONITOR_TEST_STATE" ;;
esac
`
			if err := os.WriteFile(filepath.Join(dir, "systemctl"), []byte(script), 0o755); err != nil {
				t.Fatal(err)
			}
			t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
			t.Setenv("MONITOR_TEST_CALLS", calls)
			t.Setenv("MONITOR_TEST_ACTIVE", map[bool]string{false: "no", true: "yes"}[active])
			t.Setenv("MONITOR_TEST_STATE", map[bool]string{false: "inactive", true: "active"}[active])
			// No unit change is needed on this retry, but the prior attempt
			// may have ended before systemd loaded the newly written file.
			if err := activateMonitoringService("monitor", active); err != nil {
				t.Fatal(err)
			}
			data, err := os.ReadFile(calls)
			if err != nil {
				t.Fatal(err)
			}
			lines := string(data)
			if !strings.HasPrefix(lines, "daemon-reload\n") {
				t.Fatalf("unit was not reloaded first: %s", lines)
			}
			if active {
				if strings.Count(lines, "restart monitor\n") != 1 || strings.Contains(lines, "enable --now") {
					t.Fatalf("running service did not reapply unit: %s", lines)
				}
			} else if !strings.Contains(lines, "enable --now monitor\n") || strings.Contains(lines, "restart monitor\n") {
				t.Fatalf("stopped service activation order: %s", lines)
			}
		})
	}
}
