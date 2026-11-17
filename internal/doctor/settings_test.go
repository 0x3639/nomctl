package doctor

import (
	"os"
	"path/filepath"
	"testing"
)

func TestConfiguredNetworkSettings(t *testing.T) {
	for _, tc := range []struct {
		name, body, url string
		port            int
		bad             bool
	}{
		{"defaults", `{}`, "http://127.0.0.1:35997", 35995, false},
		{"custom", `{"RPC":{"HTTPHost":"127.0.0.2","HTTPPort":40000},"Net":{"ListenPort":40001}}`, "http://127.0.0.2:40000", 40001, false},
		{"P2P hostname", `{"Net":{"ListenHost":"node.example","ListenPort":40001},"RPC":{"HTTPPort":40000}}`, "http://127.0.0.1:40000", 40001, false},
		{"empty P2P host", `{"Net":{"ListenHost":"  "}}`, "", 0, true},
		{"ipv6", `{"RPC":{"HTTPHost":"::","HTTPPort":40000}}`, "http://[::1]:40000", 35995, false},
		{"disabled", `{"RPC":{"EnableHTTP":false}}`, "", 35995, false},
		{"bad JSON", `{"Producer":{"Password":"don't print"},`, "", 0, true},
		{"wrong type", `{"Net":{"ListenPort":"35995"}}`, "", 0, true},
		{"null", `{"Net":{"ListenPort":null}}`, "", 0, true},
		{"zero", `{"Net":{"ListenPort":0}}`, "", 0, true},
		{"fraction", `{"RPC":{"HTTPPort":35.5}}`, "", 0, true},
		{"oversized", `{"RPC":{"HTTPPort":65536}}`, "", 0, true},
		{"hostname", `{"RPC":{"HTTPHost":"example.org"}}`, "", 0, true},
		{"malformed section", `{"Net":"bad"}`, "", 0, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			if err := os.WriteFile(filepath.Join(dir, "config.json"), []byte(tc.body), 0600); err != nil {
				t.Fatal(err)
			}
			s, err := readSettings(dir)
			if (err != nil) != tc.bad {
				t.Fatalf("settings=%+v err=%v", s, err)
			}
			if !tc.bad && (s.rpcURL != tc.url || s.port != tc.port) {
				t.Fatal(s)
			}
		})
	}
	if s, err := readSettings(t.TempDir()); err != nil || s.port != 35995 {
		t.Fatal(s, err)
	}
}
