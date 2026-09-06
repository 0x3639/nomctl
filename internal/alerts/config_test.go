package alerts

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestDefaultConfig(t *testing.T) {
	c := DefaultConfig()
	if len(c.Rules) != 12 || c.Interval != 30*time.Second || c.RelayURL != DefaultRelayURL || c.Paired() {
		t.Errorf("defaults: %+v", c)
	}
	for _, name := range RuleNames() {
		rc, ok := c.Rules[name]
		if !ok || !rc.Enabled {
			t.Errorf("%s missing or disabled", name)
		}
	}
	if c.Rules["disk_low"].Threshold("disk_low", "min_free_gb") != 15 || c.Rules["memory_high"].Threshold("memory_high", "pct") != 85 {
		t.Error("default thresholds wrong")
	}
}

func TestSaveLoadRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "etc", "alerts.json")
	c := DefaultConfig()
	c.NodeID, c.Secret, c.Name = "n_1", "c2VjcmV0", "pillar-1"
	c.Interval = 45 * time.Second
	if err := c.Set("disk_low.min_free_gb", "20"); err != nil {
		t.Fatal(err)
	}
	if err := c.Save(path); err != nil {
		t.Fatal(err)
	}
	if st, _ := os.Stat(path); st.Mode().Perm() != 0o600 {
		t.Errorf("mode = %o", st.Mode().Perm())
	}
	got, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := c.Set("pillar.name", " MyPillar "); err != nil || c.PillarName != "MyPillar" {
		t.Fatalf("pillar.name: %v %q", err, c.PillarName)
	}
	if err := c.Save(path); err != nil {
		t.Fatal(err)
	}
	got, err = Load(path)
	if err != nil || got.PillarName != "MyPillar" {
		t.Fatalf("pillar name lost: %+v %v", got, err)
	}
	if got.NodeID != "n_1" || got.Secret != "c2VjcmV0" || got.Name != "pillar-1" || got.Interval != 45*time.Second || !got.Paired() {
		t.Errorf("round trip: %+v", got)
	}
	if got.Rules["disk_low"].Threshold("disk_low", "min_free_gb") != 20 {
		t.Error("threshold lost")
	}
}

func TestLoadFillsDefaults(t *testing.T) {
	path := filepath.Join(t.TempDir(), "alerts.json")
	if err := os.WriteFile(path, []byte(`{"relay_url":"https://r","node_id":"n","secret":"s","alerts":{"disk_low":{"enabled":false}}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	c, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(c.Rules) != 12 || c.Rules["disk_low"].Enabled || c.Rules["disk_low"].Threshold("disk_low", "min_free_gb") != 15 || !c.Rules["service_down"].Enabled || c.Interval != DefaultInterval {
		t.Errorf("fill defaults: %+v", c.Rules)
	}
	if err := os.WriteFile(path, []byte(`{"interval":"1s"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path); err == nil {
		t.Error("interval below 5s must be rejected")
	}
	if _, err := Load(filepath.Join(t.TempDir(), "missing")); err == nil {
		t.Error("missing file must error")
	}
}

func TestLoadClampsInvalidThresholds(t *testing.T) {
	path := filepath.Join(t.TempDir(), "alerts.json")
	raw := `{"alerts":{"disk_low":{"enabled":true,"thresholds":{"min_free_gb":-5}},"memory_high":{"enabled":true,"thresholds":{"pct":140}},"crash_loop":{"enabled":true,"thresholds":{"window_minutes":90,"count":-1}}}}`
	if err := os.WriteFile(path, []byte(raw), 0o600); err != nil {
		t.Fatal(err)
	}
	c, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if c.Rules["disk_low"].Thresholds["min_free_gb"] != 15 || c.Rules["memory_high"].Thresholds["pct"] != 100 ||
		c.Rules["crash_loop"].Thresholds["window_minutes"] != MaxWindowMinutes || c.Rules["crash_loop"].Thresholds["count"] != 2 {
		t.Errorf("clamped values: %+v", c.Rules)
	}
}

func TestSet(t *testing.T) {
	c := DefaultConfig()
	good := map[string]string{"disk_low.min_free_gb": "30", "memory_high.pct": "90", "service_down.enabled": "false", "crash_loop.count": "3"}
	for k, v := range good {
		if err := c.Set(k, v); err != nil {
			t.Errorf("Set(%s,%s): %v", k, v, err)
		}
	}
	if c.Rules["service_down"].Enabled || c.Rules["crash_loop"].Thresholds["count"] != 3 {
		t.Errorf("values not applied: %+v", c.Rules)
	}
	bad := [][2]string{{"nope.pct", "1"}, {"disk_low.nope", "1"}, {"disk_low.min_free_gb", "abc"}, {"disk_low.min_free_gb", "-1"}, {"memory_high.pct", "101"}, {"noDot", "1"}, {"service_down.enabled", "maybe"}}
	for _, kv := range bad {
		if err := c.Set(kv[0], kv[1]); err == nil {
			t.Errorf("Set(%s,%s) should fail", kv[0], kv[1])
		}
	}
}

func writeFile(path, content string) error { return os.WriteFile(path, []byte(content), 0o600) }
