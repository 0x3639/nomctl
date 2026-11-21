package nodeconfig

import (
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

func TestLocalHTTPURL(t *testing.T) {
	for _, tc := range []struct {
		name, body, want string
		bad              bool
	}{
		{"defaults", `{}`, "http://127.0.0.1:35997", false},
		{"internal Caddy upstream", `{"RPC":{"HTTPHost":"127.0.0.1","HTTPPort":3597,"WSPort":3598}}`, "http://127.0.0.1:3597", false},
		{"port only", `{"RPC":{"HTTPPort":40000}}`, "http://127.0.0.1:40000", false},
		{"localhost", `{"RPC":{"HTTPHost":"localhost"}}`, "http://127.0.0.1:35997", false},
		{"IPv6 wildcard", `{"RPC":{"HTTPHost":"::","HTTPPort":3597}}`, "http://[::1]:3597", false},
		{"specific IPv6", `{"RPC":{"HTTPHost":"::1"}}`, "http://[::1]:35997", false},
		{"specific IP", `{"RPC":{"HTTPHost":"127.0.0.2"}}`, "http://127.0.0.2:35997", false},
		{"disabled", `{"RPC":{"EnableHTTP":false}}`, "", false},
		{"invalid enable", `{"RPC":{"EnableHTTP":"true"}}`, "", true},
		{"invalid host", `{"RPC":{"HTTPHost":"https://example.org"}}`, "", true},
		{"empty host", `{"RPC":{"HTTPHost":""}}`, "", true},
		{"string port", `{"RPC":{"HTTPPort":"3597"}}`, "", true},
		{"null port", `{"RPC":{"HTTPPort":null}}`, "", true},
		{"zero port", `{"RPC":{"HTTPPort":0}}`, "", true},
		{"negative port", `{"RPC":{"HTTPPort":-1}}`, "", true},
		{"fraction port", `{"RPC":{"HTTPPort":3597.5}}`, "", true},
		{"large port", `{"RPC":{"HTTPPort":65536}}`, "", true},
		{"malformed RPC", `{"RPC":"secret-value"}`, "", true},
		{"malformed JSON", `{"Producer":{"Password":"secret-value"},`, "", true},
		{"unrelated setting", `{"Net":{"MinPeers":"invalid"},"RPC":{"HTTPPort":3597}}`, "http://127.0.0.1:3597", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			if err := os.WriteFile(filepath.Join(dir, "config.json"), []byte(tc.body), 0600); err != nil {
				t.Fatal(err)
			}
			got, err := ReadLocalHTTPURL(dir)
			if (err != nil) != tc.bad || got != tc.want {
				t.Fatalf("URL=%q err=%v, want %q bad=%v", got, err, tc.want, tc.bad)
			}
			if err != nil && strings.Contains(err.Error(), "secret-value") {
				t.Fatal("configuration secret leaked")
			}
		})
	}
	if got, err := ReadLocalHTTPURL(t.TempDir()); err != nil || got != "http://127.0.0.1:35997" {
		t.Fatalf("missing config: %q, %v", got, err)
	}
}

func TestLocalHTTPURLRejectsUnsafeFiles(t *testing.T) {
	for _, kind := range []string{"directory", "fifo", "oversized"} {
		t.Run(kind, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "config.json")
			var err error
			switch kind {
			case "directory":
				err = os.Mkdir(path, 0700)
			case "fifo":
				err = syscall.Mkfifo(path, 0600)
			case "oversized":
				err = os.WriteFile(path, []byte(strings.Repeat(" ", (1<<20)+1)), 0600)
			}
			if err != nil {
				t.Fatal(err)
			}
			if got, err := ReadLocalHTTPURL(dir); err == nil || got != "" {
				t.Fatalf("unsafe file accepted: %q, %v", got, err)
			}
		})
	}
}
