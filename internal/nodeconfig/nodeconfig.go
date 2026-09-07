// Package nodeconfig reads, validates and edits the node's config.json
// against go-zenon's schema (node/config.go), so a typo cannot be written
// and silently ignored by znnd.
package nodeconfig

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Kind is a setting's type.
type Kind int

// Setting kinds.
const (
	Bool Kind = iota
	Int
	String
	StringList
)

func (k Kind) String() string {
	switch k {
	case Bool:
		return "bool"
	case Int:
		return "int"
	case String:
		return "string"
	default:
		return "list"
	}
}

// Setting describes one known key.
type Setting struct {
	Key     string // dotted, e.g. RPC.HTTPPort
	Kind    Kind
	Default any // go-zenon's default when the key is absent
	Help    string
	// Reserved keys are managed by another command and refused by Set.
	Reserved string
}

// Schema is every key go-zenon reads from config.json, with its default
// (node/defaults.go, p2p/config.go). Producer keys are reserved for
// `nomctl pillar setup`.
var Schema = []Setting{
	{Key: "Name", Kind: String, Default: "znn-node", Help: "node name shown to peers"},
	{Key: "LogLevel", Kind: String, Default: "info", Help: "debug, info, warn, error or crit"},
	{Key: "DataPath", Kind: String, Default: "", Help: "data directory (default ~/.znn); leave unset"},
	{Key: "WalletPath", Kind: String, Default: "", Help: "wallet directory (default <DataPath>/wallet)"},
	{Key: "GenesisFile", Kind: String, Default: "", Help: "absolute path to a genesis file (embedded when unset)"},
	{Key: "RPC.EnableHTTP", Kind: Bool, Default: true, Help: "serve JSON-RPC over HTTP"},
	{Key: "RPC.EnableWS", Kind: Bool, Default: true, Help: "serve JSON-RPC over WebSocket"},
	{Key: "RPC.HTTPHost", Kind: String, Default: "0.0.0.0", Help: "HTTP bind address; 127.0.0.1 keeps it local"},
	{Key: "RPC.HTTPPort", Kind: Int, Default: 35997, Help: "HTTP port"},
	{Key: "RPC.WSHost", Kind: String, Default: "0.0.0.0", Help: "WebSocket bind address"},
	{Key: "RPC.WSPort", Kind: Int, Default: 35998, Help: "WebSocket port"},
	{Key: "RPC.Endpoints", Kind: StringList, Default: []string{}, Help: "extra API namespaces to expose"},
	{Key: "RPC.HTTPVirtualHosts", Kind: StringList, Default: []string{}, Help: "allowed Host headers"},
	{Key: "RPC.HTTPCors", Kind: StringList, Default: []string{"*"}, Help: "allowed CORS origins"},
	{Key: "RPC.WSOrigins", Kind: StringList, Default: []string{"*"}, Help: "allowed WebSocket origins"},
	{Key: "Net.ListenHost", Kind: String, Default: "0.0.0.0", Help: "P2P bind address"},
	{Key: "Net.ListenPort", Kind: Int, Default: 35995, Help: "P2P port; must be reachable from the Internet"},
	{Key: "Net.MinPeers", Kind: Int, Default: 8, Help: "peers to keep dialing towards"},
	{Key: "Net.MinConnectedPeers", Kind: Int, Default: 16, Help: "connected peers before the node stops dialing"},
	{Key: "Net.MaxPeers", Kind: Int, Default: 60, Help: "maximum peers"},
	{Key: "Net.MaxPendingPeers", Kind: Int, Default: 10, Help: "maximum handshakes in progress"},
	{Key: "Net.Seeders", Kind: StringList, Default: []string{}, Help: "seed nodes (enode URLs); empty uses go-zenon's list"},
	{Key: "Producer.Address", Kind: String, Reserved: "pillar setup"},
	{Key: "Producer.Index", Kind: Int, Reserved: "pillar setup"},
	{Key: "Producer.KeyFilePath", Kind: String, Reserved: "pillar setup"},
	{Key: "Producer.Password", Kind: String, Reserved: "pillar setup"},
}

// Lookup finds a setting by key.
func Lookup(key string) (Setting, bool) {
	for _, s := range Schema {
		if s.Key == key {
			return s, true
		}
	}
	return Setting{}, false
}

// ReservedPrefix is the section managed by `nomctl pillar setup`; no
// config command writes under it, known key or not.
const ReservedPrefix = "Producer."

// IsReserved reports whether key is under the reserved section.
func IsReserved(key string) bool { return key == "Producer" || strings.HasPrefix(key, ReservedPrefix) }

// LogLevels go-zenon accepts.
var LogLevels = []string{"debug", "dbug", "info", "warn", "error", "eror", "crit"}

// Document is a parsed config.json. Sections are kept as raw JSON so
// anything nomctl does not know is written back unchanged.
type Document struct {
	root map[string]json.RawMessage
}

// Load reads path; a missing file is an empty document.
func Load(path string) (*Document, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return &Document{root: map[string]json.RawMessage{}}, nil
	}
	if err != nil {
		return nil, err
	}
	return Parse(data)
}

// Parse decodes config.json content.
func Parse(data []byte) (*Document, error) {
	d := &Document{root: map[string]json.RawMessage{}}
	if len(bytes.TrimSpace(data)) == 0 {
		return d, nil
	}
	if err := json.Unmarshal(data, &d.root); err != nil {
		return nil, fmt.Errorf("config.json is not a JSON object: %w", err)
	}
	if d.root == nil {
		return nil, errors.New("config.json is not a JSON object")
	}
	return d, nil
}

// Get returns the value at a dotted key and whether it is present.
func (d *Document) Get(key string) (any, bool, error) {
	section, field := split(key)
	var raw json.RawMessage
	if section == "" {
		r, ok := d.root[field]
		if !ok {
			return nil, false, nil
		}
		raw = r
	} else {
		sec, ok := d.root[section]
		if !ok || isNull(sec) {
			return nil, false, nil
		}
		m := map[string]json.RawMessage{}
		if err := json.Unmarshal(sec, &m); err != nil {
			return nil, false, fmt.Errorf("section %s is not an object", section)
		}
		r, ok := m[field]
		if !ok {
			return nil, false, nil
		}
		raw = r
	}
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		return nil, true, err
	}
	return v, true, nil
}

// Set stores value at a dotted key, creating the section if needed.
func (d *Document) Set(key string, value any) error {
	section, field := split(key)
	enc, err := json.Marshal(value)
	if err != nil {
		return err
	}
	if section == "" {
		d.root[field] = enc
		return nil
	}
	m := map[string]json.RawMessage{}
	if sec, ok := d.root[section]; ok && !isNull(sec) {
		if err := json.Unmarshal(sec, &m); err != nil {
			return fmt.Errorf("section %s is not an object", section)
		}
	}
	m[field] = enc
	out, err := json.Marshal(m)
	if err != nil {
		return err
	}
	d.root[section] = out
	return nil
}

// Unset removes a dotted key; an empty section is removed too.
func (d *Document) Unset(key string) error {
	section, field := split(key)
	if section == "" {
		delete(d.root, field)
		return nil
	}
	sec, ok := d.root[section]
	if !ok || isNull(sec) {
		return nil
	}
	m := map[string]json.RawMessage{}
	if err := json.Unmarshal(sec, &m); err != nil {
		return fmt.Errorf("section %s is not an object", section)
	}
	delete(m, field)
	if len(m) == 0 {
		delete(d.root, section)
		return nil
	}
	out, err := json.Marshal(m)
	if err != nil {
		return err
	}
	d.root[section] = out
	return nil
}

// Keys lists every dotted key present in the document, sorted.
func (d *Document) Keys() []string {
	var keys []string
	for name, raw := range d.root {
		m := map[string]json.RawMessage{}
		if !isNull(raw) && json.Unmarshal(raw, &m) == nil && (name == "RPC" || name == "Net" || name == "Producer") {
			for f := range m {
				keys = append(keys, name+"."+f)
			}
			continue
		}
		keys = append(keys, name)
	}
	sort.Strings(keys)
	return keys
}

// Bytes renders the document with four-space indent.
func (d *Document) Bytes() ([]byte, error) {
	data, err := json.MarshalIndent(d.root, "", "    ")
	if err != nil {
		return nil, err
	}
	return append(data, '\n'), nil
}

// ParseValue converts text to the setting's type.
func ParseValue(s Setting, text string) (any, error) {
	text = strings.TrimSpace(text)
	switch s.Kind {
	case Bool:
		switch strings.ToLower(text) {
		case "true", "yes", "on", "1":
			return true, nil
		case "false", "no", "off", "0":
			return false, nil
		}
		return nil, fmt.Errorf("%s wants true or false, got %q", s.Key, text)
	case Int:
		n, err := strconv.Atoi(text)
		if err != nil {
			return nil, fmt.Errorf("%s wants an integer, got %q", s.Key, text)
		}
		return n, nil
	case StringList:
		if strings.HasPrefix(text, "[") {
			var list []string
			if err := json.Unmarshal([]byte(text), &list); err != nil {
				return nil, fmt.Errorf("%s wants a JSON array of strings or a comma-separated list: %w", s.Key, err)
			}
			return list, nil
		}
		if text == "" {
			return []string{}, nil
		}
		parts := strings.Split(text, ",")
		for i := range parts {
			parts[i] = strings.TrimSpace(parts[i])
		}
		return parts, nil
	default:
		return text, nil
	}
}

// UnknownKeys lists keys znnd does not read. They are not errors for Save
// (a file with one stray key must stay repairable through set/unset) but
// Edit and show report them.
func UnknownKeys(d *Document) []string {
	var out []string
	for _, key := range d.Keys() {
		if _, ok := Lookup(key); !ok {
			out = append(out, key)
		}
	}
	return out
}

// ValidateStrict is Validate plus unknown keys as errors, for Edit.
func ValidateStrict(d *Document) error {
	var errs []error
	if err := Validate(d); err != nil {
		errs = append(errs, err)
	}
	for _, key := range UnknownKeys(d) {
		errs = append(errs, fmt.Errorf("unknown key %s (znnd would ignore it)", key))
	}
	return errors.Join(errs...)
}

// Validate checks every known key's type and value. It returns all
// problems at once. Unknown keys are reported by UnknownKeys.
func Validate(d *Document) error {
	var errs []error
	known := map[string]Setting{}
	for _, s := range Schema {
		known[s.Key] = s
	}
	for _, name := range []string{"RPC", "Net", "Producer"} {
		raw, ok := d.root[name]
		if !ok || isNull(raw) {
			continue
		}
		if err := json.Unmarshal(raw, &map[string]json.RawMessage{}); err != nil {
			errs = append(errs, fmt.Errorf("%s must be an object", name))
		}
	}
	for _, key := range d.Keys() {
		s, ok := known[key]
		if !ok {
			continue
		}
		v, _, err := d.Get(key)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		if err := checkValue(s, v, d); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

func checkValue(s Setting, v any, d *Document) error {
	switch s.Kind {
	case Bool:
		if _, ok := v.(bool); !ok {
			return fmt.Errorf("%s must be true or false", s.Key)
		}
	case Int:
		f, ok := v.(float64)
		if !ok || f != float64(int64(f)) {
			return fmt.Errorf("%s must be an integer", s.Key)
		}
		n := int(f)
		switch {
		case strings.HasSuffix(s.Key, "Port"):
			if n < 1 || n > 65535 {
				return fmt.Errorf("%s must be a port between 1 and 65535", s.Key)
			}
		case strings.HasPrefix(s.Key, "Net.") || s.Key == "Producer.Index":
			if n < 0 {
				return fmt.Errorf("%s must not be negative", s.Key)
			}
		}
		if s.Key == "Net.MaxPeers" {
			if minv, ok, _ := d.Get("Net.MinPeers"); ok {
				if mf, ok := minv.(float64); ok && float64(n) < mf {
					return errors.New("Net.MaxPeers must not be below Net.MinPeers")
				}
			}
		}
	case String:
		str, ok := v.(string)
		if !ok {
			return fmt.Errorf("%s must be a string", s.Key)
		}
		switch s.Key {
		case "LogLevel":
			found := false
			for _, l := range LogLevels {
				if str == l {
					found = true
				}
			}
			if !found {
				return fmt.Errorf("LogLevel must be one of %s", strings.Join(LogLevels, ", "))
			}
		case "RPC.HTTPHost", "RPC.WSHost", "Net.ListenHost":
			if strings.TrimSpace(str) == "" {
				return fmt.Errorf("%s must not be empty", s.Key)
			}
		}
	case StringList:
		list, ok := v.([]any)
		if !ok {
			return fmt.Errorf("%s must be a list of strings", s.Key)
		}
		for _, item := range list {
			if _, ok := item.(string); !ok {
				return fmt.Errorf("%s must contain only strings", s.Key)
			}
		}
	}
	return nil
}

// Save validates and writes the document to path atomically with mode
// 0600, after copying the previous file to path.bak.<unix> (a suffix is
// added when that name is taken). It returns the backup path.
func Save(path string, d *Document, now time.Time) (string, error) {
	if err := Validate(d); err != nil {
		return "", err
	}
	return SaveUnchecked(path, d, now)
}

// SaveUnchecked is Save without validation, for --force.
func SaveUnchecked(path string, d *Document, now time.Time) (string, error) {
	data, err := d.Bytes()
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return "", err
	}
	backup := ""
	if current, err := os.ReadFile(path); err == nil {
		backup, err = backupName(path, now)
		if err != nil {
			return "", err
		}
		if err := os.WriteFile(backup, current, 0o600); err != nil {
			return "", fmt.Errorf("back up config.json: %w", err)
		}
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return backup, err
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return backup, err
	}
	return backup, nil
}

// Effective returns the value in force for a setting: the document's, or
// the default.
func Effective(d *Document, s Setting) (value any, fromFile bool) {
	if v, ok, err := d.Get(s.Key); ok && err == nil {
		return v, true
	}
	return s.Default, false
}

// Format renders a value the way `set` accepts it.
func Format(v any) string {
	switch t := v.(type) {
	case nil:
		return ""
	case []string:
		return strings.Join(t, ",")
	case []any:
		parts := make([]string, 0, len(t))
		for _, i := range t {
			parts = append(parts, fmt.Sprint(i))
		}
		return strings.Join(parts, ",")
	case float64:
		if t == float64(int64(t)) {
			return strconv.FormatInt(int64(t), 10)
		}
		return fmt.Sprint(t)
	default:
		return fmt.Sprint(t)
	}
}

// backupName picks path.bak.<unix>, or path.bak.<unix>-N when a write in
// the same second already used it, so no backup overwrites another. The
// search is bounded and any error other than "does not exist" (a path too
// long, say) is returned rather than treated as a collision.
func backupName(path string, now time.Time) (string, error) {
	base := fmt.Sprintf("%s.bak.%d", path, now.Unix())
	name := base
	for n := 1; n <= 100; n++ {
		_, err := os.Lstat(name)
		if errors.Is(err, os.ErrNotExist) {
			return name, nil
		}
		if err != nil {
			return "", fmt.Errorf("choose a backup name: %w", err)
		}
		name = fmt.Sprintf("%s-%d", base, n)
	}
	return "", fmt.Errorf("too many backups named %s.*; remove some", base)
}

func split(key string) (section, field string) {
	if i := strings.Index(key, "."); i > 0 {
		return key[:i], key[i+1:]
	}
	return "", key
}

func isNull(raw json.RawMessage) bool { return bytes.Equal(bytes.TrimSpace(raw), []byte("null")) }
