package doctor

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func fixture(t *testing.T, root, name, data string) {
	t.Helper()
	path := filepath.Join(root, name)
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(data), 0600); err != nil {
		t.Fatal(err)
	}
}

func tcpTable(address string, port int) string {
	return fmt.Sprintf(" sl local_address rem_address st tx_queue rx_queue tr tm->when retrnsmt uid timeout inode\n0: %s:%04X 00000000:0000 0A 00000000:00000000 00:00000000 00000000 0 0 456\n", address, port)
}

func TestLocalListenerOwnershipAndBindings(t *testing.T) {
	for _, tc := range []struct {
		name, address, owner, want string
		port                       int
		pid                        int
	}{
		{"owned wildcard", "00000000", "456", "ok", 40001, 123},
		{"loopback", "0100007F", "456", "warning", 40001, 123},
		{"another process", "00000000", "999", "unknown", 40001, 123},
		{"no node PID", "00000000", "456", "unknown", 40001, 0},
		{"wrong port", "00000000", "456", "warning", 35995, 123},
		{"ipv6 wildcard", "00000000000000000000000000000000", "456", "ok", 40001, 123},
		{"ipv6 loopback", "00000000000000000000000001000000", "456", "warning", 40001, 123},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			fixture(t, root, "net/tcp", tcpTable(tc.address, tc.port))
			if err := os.MkdirAll(filepath.Join(root, "123/fd"), 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink("socket:["+tc.owner+"]", filepath.Join(root, "123/fd/3")); err != nil {
				t.Fatal(err)
			}
			c := localNetworkCheck(context.Background(), root, tc.pid, 40001)
			if c.Severity != tc.want {
				t.Fatal(c)
			}
		})
	}
	if c := localNetworkCheck(context.Background(), t.TempDir(), 123, 35995); c.Severity != "unknown" {
		t.Fatal(c)
	}
	if _, err := parseListeners("malformed", 35995); err == nil {
		t.Fatal("malformed table accepted")
	}
}

func TestExternalProbeValidation(t *testing.T) {
	for _, tc := range []struct {
		endpoint, ip string
		valid        bool
	}{
		{"", "", true}, {"https://probe.example/check", "203.0.113.2", true},
		{"http://probe.example", "203.0.113.2", false}, {"https://user:pass@probe.example", "203.0.113.2", false},
		{"https://probe.example?token=x", "203.0.113.2", false}, {"https://probe.example", "", false},
		{"", "203.0.113.2", false}, {"https://probe.example", "127.0.0.1", false}, {"https://probe.example", "192.168.1.1", false},
	} {
		if err := validateProbe(tc.endpoint, tc.ip); (err == nil) != tc.valid {
			t.Fatalf("%+v: %v", tc, err)
		}
	}
}

func TestExternalProbeResponses(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		code       int
		want       string
	}{
		{"reachable", `{"address":"203.0.113.2","port":40001,"reachable":true}`, 200, "ok"},
		{"unreachable", `{"address":"203.0.113.2","port":40001,"reachable":false}`, 200, "warning"},
		{"missing bool", `{"address":"203.0.113.2","port":40001}`, 200, "unknown"},
		{"different target", `{"address":"203.0.113.3","port":40001,"reachable":true}`, 200, "unknown"},
		{"different port", `{"address":"203.0.113.2","port":35995,"reachable":true}`, 200, "unknown"},
		{"malformed", "oops password=secret", 200, "unknown"}, {"too large", strings.Repeat("x", 5000), 200, "unknown"},
		{"server error", "", 503, "unknown"}, {"redirect", "", 302, "unknown"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Query().Get("address") != "203.0.113.2" || r.URL.Query().Get("port") != "40001" {
					t.Error(r.URL)
				}
				w.WriteHeader(tc.code)
				_, _ = w.Write([]byte(tc.body))
			}))
			defer srv.Close()
			unknown := func(s string) Check { return check("p2p_external", "unknown", "Unknown", s, "") }
			c := probeWithClient(context.Background(), Options{ProbeURL: srv.URL, PublicIP: "203.0.113.2"}, 40001, srv.Client(), unknown)
			if c.Severity != tc.want || strings.Contains(c.Evidence, "secret") {
				t.Fatal(c)
			}
		})
	}
	if c := externalCheck(context.Background(), Options{}, 35995); c.Severity != "unknown" {
		t.Fatal(c)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Nanosecond)
	defer cancel()
	c := externalCheck(ctx, Options{ProbeURL: "https://127.0.0.1:1", PublicIP: "203.0.113.2"}, 40001)
	if c.Severity != "unknown" {
		t.Fatal(c)
	}
}
