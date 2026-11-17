package doctor

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestExecutableHashAndCancellation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "znnd")
	data := []byte("an executable fixture\n")
	if err := os.WriteFile(path, data, 0700); err != nil {
		t.Fatal(err)
	}
	want := sha256.Sum256(data)
	h, err := hashFile(context.Background(), path)
	if err != nil || h != hex.EncodeToString(want[:]) {
		t.Fatal(h, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := hashFile(ctx, path); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if _, err := hashFile(context.Background(), filepath.Dir(path)); err == nil {
		t.Fatal("directory accepted")
	}
	if d := digest(context.Background(), path+"-missing"); d.SHA256 != "" || d.Error == "" {
		t.Fatal(d)
	}
}

func TestIdentityMismatchAndOutput(t *testing.T) {
	i := Identity{Version: "v0.0.10", Commit: "a1b2c3d4", Installed: Digest{Path: "/usr/local/bin/znnd", SHA256: strings.Repeat("a", 64)}, Running: Digest{Path: "/usr/local/bin/znnd (deleted)", SHA256: strings.Repeat("b", 64)}}
	c := identityCheck(i)
	if c.Severity != "warning" {
		t.Fatal(c)
	}
	r := Report{SchemaVersion: 1, Service: "go-zenon", Identity: i, Checks: []Check{c}}
	var text, js bytes.Buffer
	if err := Write(&text, r, false); err != nil {
		t.Fatal(err)
	}
	if err := Write(&js, r, true); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{i.Version, i.Commit, i.Installed.SHA256, i.Running.SHA256} {
		if !strings.Contains(text.String(), want) || !strings.Contains(js.String(), want) {
			t.Fatalf("missing %s", want)
		}
	}
	var decoded Report
	if err := json.Unmarshal(js.Bytes(), &decoded); err != nil || decoded.SchemaVersion != 1 || decoded.Checks[0].ID != "identity" {
		t.Fatal(err, decoded)
	}
	i.Running.SHA256 = i.Installed.SHA256
	if identityCheck(i).Severity != "ok" {
		t.Fatal("matching hashes not recognized")
	}
	i.Commit = ""
	if identityCheck(i).Severity != "unknown" {
		t.Fatal("missing commit reported healthy")
	}
}

func TestUntrustedTextAndOutputErrors(t *testing.T) {
	c := check("test", "warning", "bad\x1b[31m\nmessage", "password=secret-value\x00", "inspect logs")
	if strings.ContainsAny(c.Summary, "\x1b\n") || strings.Contains(c.Evidence, "secret-value") || strings.ContainsRune(c.Evidence, 0) {
		t.Fatal(c)
	}
	for _, json := range []bool{false, true} {
		if err := Write(failedWriter{}, Report{}, json); err == nil {
			t.Fatal("write error ignored")
		}
	}
}

type failedWriter struct{}

func (failedWriter) Write([]byte) (int, error) { return 0, errors.New("closed output") }
