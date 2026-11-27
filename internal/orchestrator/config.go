package orchestrator

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/0x3639/nomctl/internal/nodeconfig"
)

const (
	// MaxConfigBytes matches the orchestrator's own 1 MiB config.json limit.
	MaxConfigBytes = 1 << 20

	// Canonical network names used in orchestrator config.json.
	NetworkBNBChain  = "BNB Chain"
	NetworkEthereum  = "Ethereum"
	NetworkSupernova = "Supernova"
	NetworkZenon     = "Zenon"

	// Provider identifiers for built-in sync templates.
	ProviderAlchemy = "alchemy"
	ProviderDRPC    = "drpc"
)

// SyncSettings holds the per-network EVM log-query and RPC rate-limit fields.
type SyncSettings struct {
	FilterQuerySize      uint64  `json:"FilterQuerySize"`
	RpcRequestsPerSecond float64 `json:"RpcRequestsPerSecond"`
	RpcBurst             int     `json:"RpcBurst"`
}

// SyncTemplate describes one provider's sync settings for one EVM network.
type SyncTemplate struct {
	Provider    string
	DisplayName string
	Network     string
	Settings    SyncSettings
	Description string
}

var builtInTemplates = []SyncTemplate{
	{
		Provider:    ProviderDRPC,
		DisplayName: "dRPC (drpc.org)",
		Network:     NetworkBNBChain,
		Settings: SyncSettings{
			FilterQuerySize:      2000,
			RpcRequestsPerSecond: 0,
			RpcBurst:             0,
		},
		Description: "dRPC BNB Chain sync settings (FilterQuerySize 2000, uncapped)",
	},
	{
		Provider:    ProviderDRPC,
		DisplayName: "dRPC (drpc.org)",
		Network:     NetworkEthereum,
		Settings: SyncSettings{
			FilterQuerySize:      500,
			RpcRequestsPerSecond: 0,
			RpcBurst:             0,
		},
		Description: "dRPC Ethereum sync settings (FilterQuerySize 500, uncapped)",
	},
	{
		Provider:    ProviderAlchemy,
		DisplayName: "Alchemy",
		Network:     NetworkBNBChain,
		Settings: SyncSettings{
			FilterQuerySize:      10,
			RpcRequestsPerSecond: 0.5,
			RpcBurst:             1,
		},
		Description: "Alchemy free-tier BNB Chain sync settings (FilterQuerySize 10, 0.5 req/s, burst 1)",
	},
	{
		Provider:    ProviderAlchemy,
		DisplayName: "Alchemy",
		Network:     NetworkEthereum,
		Settings: SyncSettings{
			FilterQuerySize:      10,
			RpcRequestsPerSecond: 0.5,
			RpcBurst:             1,
		},
		Description: "Alchemy free-tier Ethereum sync settings (FilterQuerySize 10, 0.5 req/s, burst 1)",
	},
}

// Templates returns a copy of all built-in provider sync templates.
func Templates() []SyncTemplate {
	out := make([]SyncTemplate, len(builtInTemplates))
	copy(out, builtInTemplates)
	return out
}

// NormalizeProvider maps user input ("alchemy", "drpc", "drpc.org") onto a
// canonical provider name.
func NormalizeProvider(input string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(input)) {
	case "alchemy", "alchemy.com":
		return ProviderAlchemy, nil
	case "drpc", "drpc.org":
		return ProviderDRPC, nil
	default:
		return "", fmt.Errorf("unknown sync provider %q (supported: alchemy, drpc)", input)
	}
}

// NormalizeNetwork maps common names ("bnb", "bnb-chain", "bsc", "eth",
// "ethereum") onto the canonical config.json network key.
func NormalizeNetwork(input string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(input)) {
	case "bnb", "bnb chain", "bnb-chain", "bnbchain", "bsc":
		return NetworkBNBChain, nil
	case "eth", "ethereum":
		return NetworkEthereum, nil
	default:
		return "", fmt.Errorf("unknown template network %q (supported: %q, %q)", input, NetworkBNBChain, NetworkEthereum)
	}
}

// LookupTemplate returns the sync template for provider and network.
func LookupTemplate(provider, network string) (SyncTemplate, error) {
	p, err := NormalizeProvider(provider)
	if err != nil {
		return SyncTemplate{}, err
	}
	n, err := NormalizeNetwork(network)
	if err != nil {
		return SyncTemplate{}, err
	}
	for _, t := range builtInTemplates {
		if t.Provider == p && t.Network == n {
			return t, nil
		}
	}
	return SyncTemplate{}, fmt.Errorf("no sync template for provider %q and network %q", p, n)
}

// ConfigPath returns <dir>/config.json.
func ConfigPath(dir string) string {
	return filepath.Join(dir, "config.json")
}

// DefaultConfigJSON renders a valid starter orchestrator config.json for dir.
func DefaultConfigJSON(dir string) ([]byte, error) {
	clean, err := ValidateDir(dir)
	if err != nil {
		return nil, err
	}
	doc := map[string]any{
		"DataPath":    clean,
		"GlobalState": 0,
		"Networks": map[string]any{
			NetworkZenon: map[string]any{
				"Urls": []string{"ws://127.0.0.1:35998"},
			},
			NetworkBNBChain: map[string]any{
				"Urls":                 []string{"ws://127.0.0.1:8545"},
				"FilterQuerySize":      2000,
				"RpcRequestsPerSecond": 3,
				"RpcBurst":             5,
			},
			NetworkEthereum: map[string]any{
				"Urls":                 []string{"ws://127.0.0.1:8545"},
				"FilterQuerySize":      2000,
				"RpcRequestsPerSecond": 3,
				"RpcBurst":             5,
			},
			NetworkSupernova: map[string]any{
				"Urls":                 []string{},
				"FilterQuerySize":      2000,
				"RpcRequestsPerSecond": 3,
				"RpcBurst":             5,
			},
		},
		"TssConfig": map[string]any{
			"Port":      55055,
			"Bootstrap": "",
			"BaseDir":   filepath.Join(clean, "tss"),
		},
		"HealthConfig": map[string]any{
			"Address":                     "127.0.0.1",
			"Port":                        55000,
			"CachedResponseDelay":         25,
			"ResponsesPerSecond":          2,
			"Burst":                       2,
			"PerClientResponsesPerSecond": 2,
			"PerClientBurst":              2,
		},
		"ProducerKeyFileName":           "producer",
		"ProducerKeyFilePassphraseFile": "",
		"ProducerIndex":                 0,
	}
	raw, err := json.MarshalIndent(doc, "", "    ")
	if err != nil {
		return nil, err
	}
	return append(raw, '\n'), nil
}

// EnsureDefaultConfig writes a starter config.json (mode 0600) only if one
// does not already exist under dir.
func EnsureDefaultConfig(dir string) (created bool, path string, err error) {
	clean, err := EnsureDataDir(dir)
	if err != nil {
		return false, "", err
	}
	path = ConfigPath(clean)
	if st, err := os.Lstat(path); err == nil {
		if st.Mode()&os.ModeSymlink != 0 || !st.Mode().IsRegular() {
			return false, path, fmt.Errorf("orchestrator config %s must be a regular file", path)
		}
		return false, path, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return false, path, err
	}
	raw, err := DefaultConfigJSON(clean)
	if err != nil {
		return false, path, err
	}
	if _, err := SaveConfig(path, raw, time.Now()); err != nil {
		return false, path, err
	}
	return true, path, nil
}

// ReadConfig reads path after checking that it is a regular non-symlink file
// within MaxConfigBytes.
func ReadConfig(path string) ([]byte, error) {
	st, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if st.Mode()&os.ModeSymlink != 0 || !st.Mode().IsRegular() {
		return nil, fmt.Errorf("%s must be a regular file", path)
	}
	if st.Size() > MaxConfigBytes {
		return nil, fmt.Errorf("%s exceeds %d bytes", path, MaxConfigBytes)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	if len(raw) > MaxConfigBytes {
		return nil, fmt.Errorf("%s exceeds %d bytes", path, MaxConfigBytes)
	}
	return raw, nil
}

// ValidateConfig checks that raw is a valid orchestrator config.json object
// without rejecting unknown forward-compatible fields.
func ValidateConfig(raw []byte) error {
	if len(raw) > MaxConfigBytes {
		return fmt.Errorf("config.json exceeds %d bytes", MaxConfigBytes)
	}
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 {
		return errors.New("config.json is empty")
	}
	var root map[string]json.RawMessage
	if err := json.Unmarshal(trimmed, &root); err != nil {
		return fmt.Errorf("config.json is not a valid JSON object: %w", err)
	}
	if root == nil {
		return errors.New("config.json must be a JSON object")
	}

	var errs []error
	if netRaw, ok := findKeyCI(root, "Networks"); ok && !isJSONNull(netRaw) {
		var networks map[string]json.RawMessage
		if err := json.Unmarshal(netRaw, &networks); err != nil || networks == nil {
			errs = append(errs, errors.New("field Networks must be a JSON object"))
		} else {
			for name, entryRaw := range networks {
				if isJSONNull(entryRaw) {
					errs = append(errs, fmt.Errorf("field Networks.%s must be a JSON object", name))
					continue
				}
				var fields map[string]json.RawMessage
				if err := json.Unmarshal(entryRaw, &fields); err != nil || fields == nil {
					errs = append(errs, fmt.Errorf("field Networks.%s must be a JSON object", name))
					continue
				}
				if uRaw, ok := findKeyCI(fields, "Urls"); ok && !isJSONNull(uRaw) {
					var urls []string
					if err := json.Unmarshal(uRaw, &urls); err != nil {
						errs = append(errs, fmt.Errorf("field Networks.%s.Urls must be an array of strings", name))
					}
				}
				if fqsRaw, ok := findKeyCI(fields, "FilterQuerySize"); ok && !isJSONNull(fqsRaw) {
					if err := validateNonNegUint(fqsRaw, fmt.Sprintf("field Networks.%s.FilterQuerySize", name)); err != nil {
						errs = append(errs, err)
					}
				}
				if rpsRaw, ok := findKeyCI(fields, "RpcRequestsPerSecond"); ok && !isJSONNull(rpsRaw) {
					var rps float64
					if err := json.Unmarshal(rpsRaw, &rps); err != nil || math.IsNaN(rps) || math.IsInf(rps, 0) || rps < 0 {
						errs = append(errs, fmt.Errorf("field Networks.%s.RpcRequestsPerSecond must be a non-negative number", name))
					}
				}
				if burstRaw, ok := findKeyCI(fields, "RpcBurst"); ok && !isJSONNull(burstRaw) {
					if err := validateNonNegInt(burstRaw, fmt.Sprintf("field Networks.%s.RpcBurst", name)); err != nil {
						errs = append(errs, err)
					}
				}
			}
		}
	}

	for _, section := range []string{"TssConfig", "HealthConfig"} {
		if secRaw, ok := findKeyCI(root, section); ok && !isJSONNull(secRaw) {
			var sec map[string]json.RawMessage
			if err := json.Unmarshal(secRaw, &sec); err != nil || sec == nil {
				errs = append(errs, fmt.Errorf("section %s must be a JSON object", section))
				continue
			}
			if portRaw, ok := findKeyCI(sec, "Port"); ok && !isJSONNull(portRaw) {
				var p float64
				if err := json.Unmarshal(portRaw, &p); err != nil || p != float64(int64(p)) || p < 0 || p > 65535 {
					errs = append(errs, fmt.Errorf("field %s.Port must be an integer between 0 and 65535", section))
				}
			}
		}
	}

	return errors.Join(errs...)
}

func validateNonNegUint(raw json.RawMessage, field string) error {
	var v float64
	if err := json.Unmarshal(raw, &v); err != nil || math.IsNaN(v) || math.IsInf(v, 0) || v < 0 || math.Trunc(v) != v {
		return fmt.Errorf("%s must be a non-negative integer", field)
	}
	return nil
}

func validateNonNegInt(raw json.RawMessage, field string) error {
	var v float64
	if err := json.Unmarshal(raw, &v); err != nil || math.IsNaN(v) || math.IsInf(v, 0) || v < 0 || math.Trunc(v) != v || v > math.MaxInt32 {
		return fmt.Errorf("%s must be a non-negative integer", field)
	}
	return nil
}

// SaveConfig validates raw, backs up an existing config.json as
// config.json.bak.<unix> (mode 0600), and atomically writes the new file with
// mode 0600. Invalid input leaves any existing file untouched.
func SaveConfig(path string, raw []byte, now time.Time) (string, error) {
	if err := ValidateConfig(raw); err != nil {
		return "", err
	}
	dir := filepath.Dir(path)
	if _, err := EnsureDataDir(dir); err != nil {
		return "", err
	}
	if st, err := os.Lstat(path); err == nil {
		if st.Mode()&os.ModeSymlink != 0 || !st.Mode().IsRegular() {
			return "", fmt.Errorf("%s must be a regular file", path)
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", err
	}

	backupPath := ""
	if current, err := os.ReadFile(path); err == nil {
		backupPath, err = configBackupName(path, now)
		if err != nil {
			return "", err
		}
		if err := os.WriteFile(backupPath, current, 0o600); err != nil {
			return "", fmt.Errorf("back up config.json: %w", err)
		}
	}

	tmpFile, err := os.CreateTemp(dir, ".config-json-")
	if err != nil {
		return backupPath, err
	}
	tmpPath := tmpFile.Name()
	if err := tmpFile.Chmod(0o600); err != nil {
		_ = tmpFile.Close()
		_ = os.Remove(tmpPath)
		return backupPath, err
	}
	if _, err := tmpFile.Write(raw); err != nil {
		_ = tmpFile.Close()
		_ = os.Remove(tmpPath)
		return backupPath, err
	}
	if err := tmpFile.Sync(); err != nil {
		_ = tmpFile.Close()
		_ = os.Remove(tmpPath)
		return backupPath, err
	}
	if err := tmpFile.Close(); err != nil {
		_ = os.Remove(tmpPath)
		return backupPath, err
	}
	if err := os.Rename(tmpPath, path); err != nil {
		_ = os.Remove(tmpPath)
		return backupPath, err
	}
	return backupPath, nil
}

func configBackupName(path string, now time.Time) (string, error) {
	base := fmt.Sprintf("%s.bak.%d", path, now.Unix())
	name := base
	for n := 1; n <= 100; n++ {
		_, err := os.Lstat(name)
		if errors.Is(err, os.ErrNotExist) {
			return name, nil
		}
		if err != nil {
			return "", fmt.Errorf("choose config backup name: %w", err)
		}
		name = fmt.Sprintf("%s-%d", base, n)
	}
	return "", fmt.Errorf("too many config backups named %s.*; remove some", base)
}

// EditConfig opens a private copy of the orchestrator's config.json in the
// user's editor and installs it atomically only after JSON and schema
// validation pass.
func EditConfig(path string, editor nodeconfig.Editor, retry nodeconfig.Retry, now time.Time) (nodeconfig.EditOutcome, string, error) {
	dir, err := EnsureDataDir(filepath.Dir(path))
	if err != nil {
		return nodeconfig.EditAborted, "", err
	}
	original, err := ReadConfig(path)
	if errors.Is(err, os.ErrNotExist) {
		original, err = DefaultConfigJSON(dir)
	}
	if err != nil {
		return nodeconfig.EditAborted, "", err
	}
	tmpDir, err := os.MkdirTemp(dir, ".config-edit-")
	if err != nil {
		return nodeconfig.EditAborted, "", err
	}
	defer func() { _ = os.RemoveAll(tmpDir) }()
	copyPath := filepath.Join(tmpDir, "config.json")
	if err := os.WriteFile(copyPath, original, 0o600); err != nil {
		return nodeconfig.EditAborted, "", err
	}
	for {
		if err := editor(copyPath); err != nil {
			return nodeconfig.EditAborted, "", fmt.Errorf("editor: %w", err)
		}
		edited, err := os.ReadFile(copyPath)
		if err != nil {
			return nodeconfig.EditAborted, "", err
		}
		if bytes.Equal(edited, original) {
			return nodeconfig.EditUnchanged, "", nil
		}
		if err := ValidateConfig(edited); err == nil {
			backupPath, err := SaveConfig(path, edited, now)
			if err != nil {
				return nodeconfig.EditAborted, "", err
			}
			return nodeconfig.EditSaved, backupPath, nil
		} else {
			again, rerr := retry(err)
			if rerr != nil {
				return nodeconfig.EditAborted, "", rerr
			}
			if !again {
				return nodeconfig.EditAborted, "", nil
			}
		}
	}
}

// TemplateApplyResult records which sync templates were applied and where the
// previous config.json was backed up.
type TemplateApplyResult struct {
	ConfigPath string
	BackupPath string
	Applied    []SyncTemplate
}

// ApplySyncTemplates updates FilterQuerySize, RpcRequestsPerSecond, and
// RpcBurst for each requested network->provider assignment while preserving
// every Urls list, unrelated network, and every other config.json field.
func ApplySyncTemplates(path string, assignments map[string]string, now time.Time) (TemplateApplyResult, error) {
	var res TemplateApplyResult
	if len(assignments) == 0 {
		return res, errors.New("at least one network template assignment is required")
	}
	// Resolve and validate every assignment before reading or touching disk.
	resolved := make(map[string]SyncTemplate, len(assignments))
	for rawNet, rawProv := range assignments {
		tpl, err := LookupTemplate(rawProv, rawNet)
		if err != nil {
			return res, err
		}
		resolved[tpl.Network] = tpl
	}

	dir, err := EnsureDataDir(filepath.Dir(path))
	if err != nil {
		return res, err
	}
	raw, err := ReadConfig(path)
	if errors.Is(err, os.ErrNotExist) {
		raw, err = DefaultConfigJSON(dir)
	}
	if err != nil {
		return res, err
	}
	if err := ValidateConfig(raw); err != nil {
		return res, fmt.Errorf("existing config.json is invalid: %w", err)
	}

	var root map[string]json.RawMessage
	if err := json.Unmarshal(raw, &root); err != nil {
		return res, err
	}
	networksKey := existingKeyCI(root, "Networks", "Networks")
	networks := map[string]json.RawMessage{}
	if existing, ok := root[networksKey]; ok && !isJSONNull(existing) {
		if err := json.Unmarshal(existing, &networks); err != nil {
			return res, fmt.Errorf("parse Networks: %w", err)
		}
	}

	keys := make([]string, 0, len(resolved))
	for k := range resolved {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	applied := make([]SyncTemplate, 0, len(keys))
	for _, netName := range keys {
		tpl := resolved[netName]
		targetKey := existingKeyCI(networks, netName, netName)
		entry := map[string]json.RawMessage{}
		if existing, ok := networks[targetKey]; ok && !isJSONNull(existing) {
			if err := json.Unmarshal(existing, &entry); err != nil {
				return res, fmt.Errorf("parse Networks.%s: %w", targetKey, err)
			}
		} else {
			entry["Urls"] = json.RawMessage(`[]`)
		}
		if err := setJSONField(entry, "FilterQuerySize", tpl.Settings.FilterQuerySize); err != nil {
			return res, err
		}
		if err := setJSONField(entry, "RpcRequestsPerSecond", tpl.Settings.RpcRequestsPerSecond); err != nil {
			return res, err
		}
		if err := setJSONField(entry, "RpcBurst", tpl.Settings.RpcBurst); err != nil {
			return res, err
		}
		enc, err := json.Marshal(entry)
		if err != nil {
			return res, err
		}
		networks[targetKey] = enc
		applied = append(applied, tpl)
	}

	encNets, err := json.Marshal(networks)
	if err != nil {
		return res, err
	}
	root[networksKey] = encNets
	formatted, err := json.MarshalIndent(root, "", "    ")
	if err != nil {
		return res, err
	}
	formatted = append(formatted, '\n')

	backupPath, err := SaveConfig(path, formatted, now)
	if err != nil {
		return res, err
	}
	return TemplateApplyResult{
		ConfigPath: path,
		BackupPath: backupPath,
		Applied:    applied,
	}, nil
}

// NetworkSyncInfo summarizes one network's sync parameters from config.json.
type NetworkSyncInfo struct {
	Name     string
	URLCount int
	Settings SyncSettings
}

// ReadNetworkSyncSummary returns the sorted network sync settings present in
// path without exposing URL secrets.
func ReadNetworkSyncSummary(path string) ([]NetworkSyncInfo, error) {
	raw, err := ReadConfig(path)
	if err != nil {
		return nil, err
	}
	var doc struct {
		Networks map[string]struct {
			Urls                 []string `json:"Urls"`
			FilterQuerySize      uint64   `json:"FilterQuerySize"`
			RpcRequestsPerSecond float64  `json:"RpcRequestsPerSecond"`
			RpcBurst             int      `json:"RpcBurst"`
		} `json:"Networks"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		return nil, fmt.Errorf("parse orchestrator config: %w", err)
	}
	names := make([]string, 0, len(doc.Networks))
	for name := range doc.Networks {
		names = append(names, name)
	}
	sort.Strings(names)
	out := make([]NetworkSyncInfo, 0, len(names))
	for _, name := range names {
		n := doc.Networks[name]
		out = append(out, NetworkSyncInfo{
			Name:     name,
			URLCount: len(n.Urls),
			Settings: SyncSettings{
				FilterQuerySize:      n.FilterQuerySize,
				RpcRequestsPerSecond: n.RpcRequestsPerSecond,
				RpcBurst:             n.RpcBurst,
			},
		})
	}
	return out, nil
}

// RedactConfigJSON masks ProducerKeyFilePassphrase and secret path/query/auth
// parts of network RPC URLs so displaying config.json does not leak keys.
// Redaction is idempotent.
func RedactConfigJSON(raw []byte) ([]byte, error) {
	if err := ValidateConfig(raw); err != nil {
		return nil, err
	}
	var root map[string]json.RawMessage
	if err := json.Unmarshal(raw, &root); err != nil {
		return nil, err
	}
	for k, v := range root {
		if strings.EqualFold(k, "ProducerKeyFilePassphrase") && !isJSONNull(v) {
			var s string
			if err := json.Unmarshal(v, &s); err == nil && s != "" {
				root[k] = json.RawMessage(`"***"`)
			}
		}
		if strings.EqualFold(k, "Networks") && !isJSONNull(v) {
			var networks map[string]json.RawMessage
			if err := json.Unmarshal(v, &networks); err == nil && networks != nil {
				for netName, netRaw := range networks {
					var entry map[string]json.RawMessage
					if err := json.Unmarshal(netRaw, &entry); err != nil || entry == nil {
						continue
					}
					for fk, fv := range entry {
						if strings.EqualFold(fk, "Urls") && !isJSONNull(fv) {
							var urls []string
							if err := json.Unmarshal(fv, &urls); err == nil {
								for i, u := range urls {
									urls[i] = redactRPCURL(u)
								}
								if enc, err := json.Marshal(urls); err == nil {
									entry[fk] = enc
								}
							}
						}
					}
					if enc, err := json.Marshal(entry); err == nil {
						networks[netName] = enc
					}
				}
				if enc, err := json.Marshal(networks); err == nil {
					root[k] = enc
				}
			}
		}
	}
	out, err := json.MarshalIndent(root, "", "    ")
	if err != nil {
		return nil, err
	}
	return append(out, '\n'), nil
}

func redactRPCURL(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return raw
	}
	if u.User != nil {
		u.User = url.User("***")
	}
	if u.Path != "" && u.Path != "/" {
		u.Path = "/***"
		u.RawPath = ""
	}
	if u.RawQuery != "" {
		u.RawQuery = "***"
	}
	return u.String()
}

func findKeyCI(m map[string]json.RawMessage, want string) (json.RawMessage, bool) {
	if v, ok := m[want]; ok {
		return v, true
	}
	for k, v := range m {
		if strings.EqualFold(k, want) {
			return v, true
		}
	}
	return nil, false
}

func existingKeyCI(m map[string]json.RawMessage, want, fallback string) string {
	if _, ok := m[want]; ok {
		return want
	}
	for k := range m {
		if strings.EqualFold(k, want) {
			return k
		}
	}
	return fallback
}

func setJSONField(entry map[string]json.RawMessage, key string, val any) error {
	target := existingKeyCI(entry, key, key)
	enc, err := json.Marshal(val)
	if err != nil {
		return err
	}
	entry[target] = enc
	return nil
}

func isJSONNull(raw json.RawMessage) bool {
	return bytes.Equal(bytes.TrimSpace(raw), []byte("null"))
}
