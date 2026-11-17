package doctor

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/0x3639/nomctl/internal/config"
	"github.com/0x3639/nomctl/internal/metrics"
)

func fakeTools(t *testing.T, props string) {
	t.Helper()
	dir := t.TempDir()
	for name, body := range map[string]string{
		"systemctl":  "#!/bin/sh\ncat <<'PROPS'\n" + props + "\nPROPS\n",
		"journalctl": "#!/bin/sh\nprintf 'ordinary service message\\n'\n",
	} {
		// Use an absolute cat so tests work with a deliberately minimal PATH.
		body = strings.ReplaceAll(body, "cat <<", "/bin/cat <<")
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0700); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", dir)
}

func TestCollectIdentityRechecksProcess(t *testing.T) {
	for _, changed := range []bool{false, true} {
		t.Run(fmt.Sprintf("changed=%v", changed), func(t *testing.T) {
			pid := 123
			if changed {
				pid = 456
			}
			fakeTools(t, fmt.Sprintf("MainPID=%d\nNRestarts=0", pid))
			root := t.TempDir()
			fields := strings.Fields(strings.Repeat("0 ", 20))
			fields[19] = "100"
			fixture(t, root, "123/stat", "123 (znnd) "+strings.Join(fields, " "))
			fixture(t, root, "installed", "new binary")
			fixture(t, root, "running", "old binary")
			if err := os.Symlink(filepath.Join(root, "running"), filepath.Join(root, "123/exe")); err != nil {
				t.Fatal(err)
			}
			i := collectIdentity(context.Background(), filepath.Join(root, "installed"), root, metrics.ServiceSample{Unit: "go-zenon", MainPID: 123}, metrics.NodeSample{Version: "v0.0.10", Commit: "abcdef"})
			if i.Version != "v0.0.10" || i.Commit != "abcdef" || i.Installed.SHA256 == "" {
				t.Fatal(i)
			}
			if changed {
				if i.Running.SHA256 != "" || i.Running.Error == "" {
					t.Fatal(i)
				}
			} else if i.Running.SHA256 == "" || identityCheck(i).Severity != "warning" {
				t.Fatal(i)
			}
		})
	}
}

func TestRunPreservesVersionWhenAnotherRPCMethodFails(t *testing.T) {
	fakeTools(t, "ActiveState=active\nSubState=running\nMainPID=0\nNRestarts=0")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Method string `json:"method"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Error(err)
			return
		}
		if req.Method == "stats.processInfo" {
			fmt.Fprint(w, `{"jsonrpc":"2.0","id":1,"result":{"version":"v0.0.10","commit":"feedbeef"}}`)
			return
		}
		fmt.Fprint(w, `{"jsonrpc":"2.0","id":1,"error":{"code":-1,"message":"unavailable password=secret-value"}}`)
	}))
	defer srv.Close()
	_, port, _ := net.SplitHostPort(strings.TrimPrefix(srv.URL, "http://"))
	portNum, _ := strconv.Atoi(port)
	cfg := config.Default()
	cfg.ZnnDir = t.TempDir()
	cfg.InstallDir = t.TempDir()
	cfg.BackupDir = t.TempDir()
	fixture(t, cfg.ZnnDir, "config.json", fmt.Sprintf(`{"RPC":{"HTTPPort":%d}}`, portNum))
	fixture(t, cfg.InstallDir, cfg.BinaryName, "installed executable fixture")
	r, err := Run(context.Background(), cfg, Options{Wait: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	if r.Identity.Version != "v0.0.10" || r.Identity.Commit != "feedbeef" {
		t.Fatal(r.Identity)
	}
	want := sha256.Sum256([]byte("installed executable fixture"))
	if r.Identity.Installed.SHA256 != hex.EncodeToString(want[:]) {
		t.Fatal(r.Identity)
	}
	if findCheck(t, r.Checks, "rpc").Severity != "error" || findCheck(t, r.Checks, "p2p_external").Severity != "unknown" {
		t.Fatal(r.Checks)
	}
	data, err := json.Marshal(r)
	if err != nil || strings.Contains(string(data), "secret-value") {
		t.Fatal(err, string(data))
	}
	if r.FinishedAt.Before(r.StartedAt) || r.SchemaVersion != 1 {
		t.Fatal(r)
	}
}
