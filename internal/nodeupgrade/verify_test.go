package nodeupgrade

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/0x3639/nomctl/internal/backup"
	"github.com/0x3639/nomctl/internal/execx"
)

func put(t *testing.T, path, value string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(value), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestPinnedSignerStatus(t *testing.T) {
	line := "[GNUPG:] VALIDSIG " + SigningFingerprint + " 2026-11-07 1794000000 0 4 0 22 8 00 " + SigningFingerprint
	for _, tc := range []struct {
		name, status string
		want         bool
	}{
		{"primary", line, true},
		{"subkey", strings.Replace(line, SigningFingerprint, strings.Repeat("A", 40), 1), true},
		{"wrong key", strings.ReplaceAll(line, SigningFingerprint, strings.Repeat("A", 40)), false},
		{"human message", "gpg: Good signature", false},
		{"expired", line + "\n[GNUPG:] EXPKEYSIG something", false},
		{"revoked", line + "\n[GNUPG:] REVKEYSIG something", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := validSigner(tc.status, SigningFingerprint); got != tc.want {
				t.Fatalf("got %v", got)
			}
		})
	}
}

// Verify the real v0.0.10 checksum signature, and reject altered data/signers.
func TestGPGSignatureVerification(t *testing.T) {
	if !execx.Exists("gpg") {
		t.Skip("gpg not installed")
	}
	sums, err := os.ReadFile("testdata/" + checksumName)
	if err != nil {
		t.Fatal(err)
	}
	signature, err := os.ReadFile("testdata/" + checksumName + ".asc")
	if err != nil {
		t.Fatal(err)
	}
	key, err := filepath.Abs("testdata/signer.asc")
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, data, pin string
		wantErr         bool
	}{
		{"valid", string(sums), SigningFingerprint, false},
		{"tampered", string(sums) + "tampered\n", SigningFingerprint, true},
		{"wrong fingerprint", string(sums), strings.Repeat("A", 40), true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir, err := os.MkdirTemp("/tmp", "nomctl-verify-")
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = os.RemoveAll(dir) })
			put(t, filepath.Join(dir, checksumName), tc.data)
			put(t, filepath.Join(dir, checksumName+".asc"), string(signature))
			err = verifySignature(context.Background(), dir, key, tc.pin)
			if (err != nil) != tc.wantErr {
				t.Fatalf("verify: %v", err)
			}
		})
	}
}

func TestChecksumVerification(t *testing.T) {
	dir := t.TempDir()
	name := "znnd-linux-amd64.tar.gz"
	put(t, filepath.Join(dir, name), "archive")
	hash, err := backup.SHA256File(filepath.Join(dir, name))
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, sums string
		wantErr    bool
	}{
		{"valid", hash + "  " + name + "\n", false},
		{"binary mode", hash + " *" + name + "\n", false},
		{"missing", hash + " other\n", true},
		{"duplicate", strings.Repeat(hash+"  "+name+"\n", 2), true},
		{"bad hash", "not-a-hash  " + name, true},
		{"tampered", strings.Repeat("0", 64) + "  " + name, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			put(t, filepath.Join(dir, checksumName), tc.sums)
			if err := verifyChecksum(dir, name); (err != nil) != tc.wantErr {
				t.Fatal(err)
			}
		})
	}
}

func makeArchive(t *testing.T, dir string, headers ...*tar.Header) string {
	t.Helper()
	path := filepath.Join(dir, "release.tar.gz")
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	gz := gzip.NewWriter(f)
	tw := tar.NewWriter(gz)
	for _, h := range headers {
		if err := tw.WriteHeader(h); err != nil {
			t.Fatal(err)
		}
		if h.Size > 0 {
			if _, err := tw.Write([]byte(strings.Repeat("x", int(h.Size)))); err != nil {
				t.Fatal(err)
			}
		}
	}
	for _, err := range []error{tw.Close(), gz.Close(), f.Close()} {
		if err != nil {
			t.Fatal(err)
		}
	}
	return path
}

func TestArchiveExtraction(t *testing.T) {
	expected := "znnd-linux-amd64"
	for _, tc := range []struct {
		name    string
		headers []*tar.Header
		wantErr bool
	}{
		{"valid", []*tar.Header{{Name: expected, Mode: 0o755, Size: 3}}, false},
		{"empty", nil, true},
		{"traversal", []*tar.Header{{Name: "../" + expected, Mode: 0o755, Size: 3}}, true},
		{"link", []*tar.Header{{Name: expected, Typeflag: tar.TypeSymlink, Linkname: "/bin/sh"}}, true},
		{"duplicate", []*tar.Header{{Name: expected, Size: 3}, {Name: expected, Size: 3}}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			archive := makeArchive(t, dir, tc.headers...)
			path, err := extractBinary(archive, dir, expected)
			if (err != nil) != tc.wantErr {
				t.Fatalf("extract: %v", err)
			}
			if !tc.wantErr {
				data, _ := os.ReadFile(path)
				if string(data) != "xxx" {
					t.Fatalf("data %q", data)
				}
			}
		})
	}
}
