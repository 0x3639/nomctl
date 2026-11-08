package nodeupgrade

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"github.com/0x3639/nomctl/internal/backup"
	"github.com/0x3639/nomctl/internal/execx"
)

func archiveName(goos, arch string) (string, error) {
	if goos != "linux" || (arch != "amd64" && arch != "arm64") {
		return "", fmt.Errorf("signed node upgrades support only linux/amd64 and linux/arm64 (got %s/%s)", goos, arch)
	}
	return "znnd-linux-" + arch + ".tar.gz", nil
}

// Fetch verifies the publisher's signature and archive checksum before extracting.
// dir must be a private staging directory owned by the caller.
func (c *Client) Fetch(ctx context.Context, release Release, dir, goos, arch string) (string, error) {
	name, err := archiveName(goos, arch)
	if err != nil {
		return "", err
	}
	if !execx.Exists("gpg") {
		return "", fmt.Errorf("gpg is required to verify releases; install it with: apt-get install gnupg")
	}
	// Check all required assets before starting any download.
	var assets []Asset
	for _, n := range []string{name, checksumName, checksumName + ".asc"} {
		a, err := release.asset(n)
		if err != nil {
			return "", err
		}
		assets = append(assets, a)
	}
	for _, a := range assets {
		slog.Info("Downloading " + a.Name + " for " + release.Tag + "...")
		limit := int64(1 << 20)
		if a.Name == name {
			limit = maxArchiveSize
		}
		url := fmt.Sprintf("https://api.github.com/repos/%s/releases/assets/%d", Repository, a.ID)
		if err := c.download(ctx, url, filepath.Join(dir, a.Name), limit); err != nil {
			return "", err
		}
	}
	slog.Info("Downloading the release signing key...")
	key := filepath.Join(dir, "signer.gpg")
	if err := c.download(ctx, keyURL, key, 1<<20); err != nil {
		return "", err
	}
	slog.Info("Verifying checksum signature against signing key " + SigningFingerprint + "...")
	if err := verifySignature(ctx, dir, key, SigningFingerprint); err != nil {
		return "", err
	}
	slog.Info("Verifying SHA-256 of " + name + "...")
	if err := verifyChecksum(dir, name); err != nil {
		return "", err
	}
	slog.Info("Extracting the verified node binary...")
	return extractBinary(filepath.Join(dir, name), dir, strings.TrimSuffix(name, ".tar.gz"))
}

func verifySignature(ctx context.Context, dir, key, fingerprint string) error {
	home := filepath.Join(dir, "gnupg")
	if err := os.Mkdir(home, 0o700); err != nil {
		return err
	}
	args := []string{"--no-options", "--batch", "--homedir", home, "--no-auto-key-retrieve", "--no-autostart"}
	if err := execx.New("gpg", append(args, "--import", key)...).Context(ctx).Run(); err != nil {
		return fmt.Errorf("import release signing key: %w", err)
	}
	status, err := execx.New("gpg", append(args, "--status-fd", "1", "--verify", filepath.Join(dir, checksumName+".asc"), filepath.Join(dir, checksumName))...).Context(ctx).Output()
	if err != nil {
		return fmt.Errorf("release signature verification failed: %w", err)
	}
	if !validSigner(status, fingerprint) {
		return fmt.Errorf("release signature is not valid for pinned signing key %s", fingerprint)
	}
	return nil
}

func validSigner(status, fingerprint string) bool {
	valid := false
	for _, line := range strings.Split(status, "\n") {
		f := strings.Fields(line)
		if len(f) < 2 || f[0] != "[GNUPG:]" {
			continue
		}
		switch f[1] {
		case "BADSIG", "ERRSIG", "EXPSIG", "EXPKEYSIG", "REVKEYSIG", "KEYREVOKED", "KEYEXPIRED", "SIGEXPIRED":
			return false
		case "VALIDSIG":
			// GnuPG reports the signing subkey first and its primary key last.
			if len(f) >= 11 && (f[2] == fingerprint || (len(f) == 12 && f[11] == fingerprint)) {
				valid = true
			}
		}
	}
	return valid
}

func verifyChecksum(dir, name string) error {
	sums, err := os.ReadFile(filepath.Join(dir, checksumName))
	if err != nil {
		return err
	}
	want := ""
	for _, line := range strings.Split(string(sums), "\n") {
		fields := strings.Fields(line)
		if len(fields) != 2 || strings.TrimPrefix(fields[1], "*") != name {
			continue
		}
		decoded, err := hex.DecodeString(fields[0])
		if err != nil || len(decoded) != sha256.Size || want != "" {
			return fmt.Errorf("invalid or duplicate checksum for %s", name)
		}
		want = strings.ToLower(fields[0])
	}
	if want == "" {
		return fmt.Errorf("%s is not listed in signed %s", name, checksumName)
	}
	got, err := backup.SHA256File(filepath.Join(dir, name))
	if err != nil {
		return err
	}
	if got != want {
		return fmt.Errorf("SHA-256 mismatch for %s", name)
	}
	return nil
}

// Extract only the expected regular binary into a fixed path, never archive paths.
func extractBinary(archive, dir, expected string) (string, error) {
	f, err := os.Open(archive)
	if err != nil {
		return "", err
	}
	defer func() { _ = f.Close() }()
	gz, err := gzip.NewReader(f)
	if err != nil {
		return "", err
	}
	defer func() { _ = gz.Close() }()
	tr := tar.NewReader(gz)
	target := filepath.Join(dir, "znnd.verified")
	found := false
	for {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return "", err
		}
		if strings.TrimPrefix(h.Name, "./") != expected || !h.FileInfo().Mode().IsRegular() || found || h.Size <= 0 || h.Size > maxArchiveSize {
			return "", fmt.Errorf("unexpected entry in release archive: %q", h.Name)
		}
		out, err := os.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o755)
		if err != nil {
			return "", err
		}
		_, err = io.Copy(out, tr)
		closeErr := out.Close()
		if err != nil {
			return "", err
		}
		if closeErr != nil {
			return "", closeErr
		}
		found = true
	}
	// Read through gzip's trailer so truncation/CRC errors cannot be ignored.
	n, err := io.Copy(io.Discard, io.LimitReader(gz, maxArchiveSize+1))
	if err != nil {
		return "", err
	}
	if n > maxArchiveSize {
		return "", fmt.Errorf("release archive has excessive trailing data")
	}
	if !found {
		return "", fmt.Errorf("release archive does not contain %s", expected)
	}
	return target, nil
}
