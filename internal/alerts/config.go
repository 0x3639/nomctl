// Package alerts is the node side of nomctl alerting: a config file, pure
// rules evaluated over metrics samples, and a daemon that reports state
// changes to the relay.
package alerts

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/user"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/0x3639/nomctl/internal/alertproto"
)

// Paths.
const (
	DefaultConfigPath = "/etc/nomctl/alerts.json"
	DefaultStatePath  = "/run/nomctl/alerts-state.json"
	DefaultInterval   = 30 * time.Second
)

// DefaultRelayURL is the relay nodes pair with unless overridden. Set at
// build time with -ldflags "-X .../internal/alerts.DefaultRelayURL=...".
var DefaultRelayURL = "https://alerts.example.invalid"

// RuleConfig holds the per-alert switch and thresholds.
type RuleConfig struct {
	Enabled    bool               `json:"enabled"`
	Thresholds map[string]float64 `json:"thresholds,omitempty"`
}

// Config is /etc/nomctl/alerts.json.
type Config struct {
	RelayURL string `json:"relay_url"`
	NodeID   string `json:"node_id"`
	Secret   string `json:"secret"`
	Name     string `json:"name"`
	// PillarName is the pillar this node produces for; empty for a plain node.
	PillarName string                `json:"pillar_name,omitempty"`
	Interval   time.Duration         `json:"-"`
	Rules      map[string]RuleConfig `json:"alerts"`
}

// configJSON is Config with the interval as a duration string.
type configJSON struct {
	Config
	IntervalText string `json:"interval"`
}

// defaultThresholds lists the tunable keys of each rule with their default.
var defaultThresholds = map[string]map[string]float64{
	"service_down":      {},
	"crash_loop":        {"window_minutes": 10, "count": 2},
	"sync_stalled":      {},
	"sync_behind":       {"minutes": 10},
	"not_enough_peers":  {"min_peers": 3, "minutes": 5},
	"disk_low":          {"min_free_gb": 15},
	"memory_high":       {"pct": 85},
	"fds_high":          {"pct": 80},
	"backup_stale":      {},
	"rpc_unreachable":   {"minutes": 5},
	"momentums_stalled": {"minutes": 5},
	"pillar_missed":     {"minutes": 30, "missed": 2},
	"update_available":  {},
}

// disabledByDefault lists alerts an operator must opt into.
var disabledByDefault = map[string]bool{"update_available": true}

// DefaultConfig returns every node-raised alert enabled with spec defaults.
func DefaultConfig() Config {
	c := Config{RelayURL: DefaultRelayURL, Interval: DefaultInterval, Rules: map[string]RuleConfig{}}
	for _, a := range alertproto.Alerts {
		if a.Relay {
			continue
		}
		c.Rules[a.Name] = defaultRule(a.Name)
	}
	return c
}

func defaultRule(name string) RuleConfig {
	rc := RuleConfig{Enabled: !disabledByDefault[name], Thresholds: map[string]float64{}}
	for k, v := range defaultThresholds[name] {
		rc.Thresholds[k] = v
	}
	return rc
}

// Threshold returns a rule threshold, falling back to the default.
func (r RuleConfig) Threshold(name, key string) float64 {
	if v, ok := r.Thresholds[key]; ok {
		return v
	}
	return defaultThresholds[name][key]
}

// Load reads a config file, filling missing rules and keys from defaults.
func Load(path string) (Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Config{}, err
	}
	var cj configJSON
	if err := json.Unmarshal(data, &cj); err != nil {
		return Config{}, fmt.Errorf("parse %s: %w", path, err)
	}
	c := cj.Config
	c.Interval = DefaultInterval
	if cj.IntervalText != "" {
		d, err := time.ParseDuration(cj.IntervalText)
		if err != nil || d < 5*time.Second {
			return Config{}, fmt.Errorf("parse %s: bad interval %q", path, cj.IntervalText)
		}
		c.Interval = d
	}
	if c.RelayURL == "" {
		c.RelayURL = DefaultRelayURL
	}
	if c.Rules == nil {
		c.Rules = map[string]RuleConfig{}
	}
	for name := range defaultThresholds {
		rc, ok := c.Rules[name]
		if !ok {
			c.Rules[name] = defaultRule(name)
			continue
		}
		if rc.Thresholds == nil {
			rc.Thresholds = map[string]float64{}
		}
		for k, v := range defaultThresholds[name] {
			if _, ok := rc.Thresholds[k]; !ok {
				rc.Thresholds[k] = v
			}
			rc.Thresholds[k] = clampThreshold(k, rc.Thresholds[k], v)
		}
		c.Rules[name] = rc
	}
	return c, nil
}

// Save writes the config with mode 0600, creating the parent directory.
func (c Config) Save(path string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	cj := configJSON{Config: c, IntervalText: c.Interval.String()}
	data, err := json.MarshalIndent(cj, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, append(data, '\n'), 0o600); err != nil {
		return err
	}
	// Written by root: the daemon's user must be able to read it, so the
	// modes are set on the temp file before it is published. Before setup
	// has created the user the file stays root-only, which is correct.
	if os.Geteuid() == 0 {
		if err := secureIfUser(tmp); err != nil {
			_ = os.Remove(tmp)
			return err
		}
	}
	return os.Rename(tmp, path)
}

// secureIfUser applies root:nomctl 0640 when the run user exists. A
// missing user is not an error; a lookup failure or a bad gid is.
func secureIfUser(path string) error {
	u, err := lookupUser(RunUser)
	if err != nil {
		var unknown user.UnknownUserError
		if errors.As(err, &unknown) {
			return nil
		}
		return fmt.Errorf("look up %s: %w", RunUser, err)
	}
	gid, err := strconv.Atoi(u.Gid)
	if err != nil {
		return fmt.Errorf("gid of %s: %w", RunUser, err)
	}
	return SecureConfig(path, gid)
}

// Paired reports whether the node has relay credentials.
func (c Config) Paired() bool { return c.NodeID != "" && c.Secret != "" }

// Set changes "<alert>.enabled" or "<alert>.<threshold>".
func (c *Config) Set(key, value string) error {
	alert, field, ok := strings.Cut(key, ".")
	if !ok {
		return errors.New("key must be <alert>.<setting>, e.g. disk_low.min_free_gb")
	}
	if alert == "pillar" && field == "name" {
		// Validated against the node by the command; empty clears it.
		c.PillarName = strings.TrimSpace(value)
		return nil
	}
	defaults, known := defaultThresholds[alert]
	if !known {
		return fmt.Errorf("unknown alert %q (see nomctl alerts list)", alert)
	}
	rc, ok := c.Rules[alert]
	if !ok {
		rc = defaultRule(alert)
	}
	if field == "enabled" {
		b, err := strconv.ParseBool(value)
		if err != nil {
			return fmt.Errorf("%s: %q is not a boolean", key, value)
		}
		rc.Enabled = b
		c.Rules[alert] = rc
		return nil
	}
	if _, ok := defaults[field]; !ok {
		return fmt.Errorf("%s has no setting %q (available: %s)", alert, field, strings.Join(thresholdKeys(alert), ", "))
	}
	f, err := strconv.ParseFloat(value, 64)
	if err != nil {
		return fmt.Errorf("%s: %q is not a number", key, value)
	}
	if f < 0 {
		return fmt.Errorf("%s must not be negative", key)
	}
	if field == "pct" && f > 100 {
		return fmt.Errorf("%s must be between 0 and 100", key)
	}
	if isMinutes(field) && f > MaxWindowMinutes {
		return fmt.Errorf("%s must be at most %d minutes (the daemon keeps %d minutes of history)", key, MaxWindowMinutes, MaxWindowMinutes)
	}
	if rc.Thresholds == nil {
		rc.Thresholds = map[string]float64{}
	}
	rc.Thresholds[field] = f
	c.Rules[alert] = rc
	return nil
}

// MaxWindowMinutes is the longest duration threshold that can fire: the
// daemon keeps exactly this much history (see HistoryWindow).
const MaxWindowMinutes = 30

func isMinutes(field string) bool { return field == "minutes" || field == "window_minutes" }

// clampThreshold applies the same bounds Set enforces to a hand-edited
// value: negatives fall back to the default, pct is capped at 100 and
// minute windows at MaxWindowMinutes.
func clampThreshold(field string, value, def float64) float64 {
	switch {
	case value < 0:
		return def
	case field == "pct" && value > 100:
		return 100
	case isMinutes(field) && value > MaxWindowMinutes:
		return MaxWindowMinutes
	}
	return value
}

// thresholdKeys lists the tunable settings of an alert, sorted.
func thresholdKeys(alert string) []string {
	keys := []string{"enabled"}
	for k := range defaultThresholds[alert] {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// RuleNames returns the node-raised alert names in spec order.
func RuleNames() []string {
	var names []string
	for _, a := range alertproto.Alerts {
		if !a.Relay {
			names = append(names, a.Name)
		}
	}
	return names
}
