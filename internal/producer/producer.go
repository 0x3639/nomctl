// Package producer creates the pillar producer key store and wires it into
// the node's config.json, the part of znn_controller_dart's Deploy that the
// rest of nomctl did not cover. Key files are made with go-zenon's own
// wallet package so the node reads exactly what it would have written.
package producer

import (
	"bytes"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"os"
	"path/filepath"
	"time"

	"github.com/tyler-smith/go-bip39"
	"github.com/zenon-network/go-zenon/wallet"
)

// KeyFileName is the only name the ecosystem expects for the producer key.
const KeyFileName = "producer"

// PasswordLength matches the controller's generated passwords.
const PasswordLength = 16

// ErrWrongPassword is returned when a key file does not open.
var ErrWrongPassword = errors.New("wrong password for the producer key file")

// Config is the Producer section of config.json.
type Config struct {
	Index       uint32 `json:"Index"`
	KeyFilePath string `json:"KeyFilePath"`
	Password    string `json:"Password"`
	Address     string `json:"Address"`
}

// KeyFilePath is where the producer key lives for a data directory.
func KeyFilePath(znnDir string) string {
	return filepath.Join(znnDir, "wallet", KeyFileName)
}

// ConfigPath is the node's config.json.
func ConfigPath(znnDir string) string { return filepath.Join(znnDir, "config.json") }

// GeneratePassword returns PasswordLength random characters from
// [A-Za-z0-9], as the controller did.
func GeneratePassword() (string, error) {
	const alphabet = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789"
	out := make([]byte, PasswordLength)
	for i := range out {
		n, err := rand.Int(rand.Reader, big.NewInt(int64(len(alphabet))))
		if err != nil {
			return "", err
		}
		out[i] = alphabet[n.Int64()]
	}
	return string(out), nil
}

// Create writes a new key file at path encrypted with password and returns
// its base address. It refuses to overwrite an existing file.
func Create(path, password string) (string, error) {
	if _, err := os.Lstat(path); err == nil {
		return "", fmt.Errorf("%s already exists; refusing to overwrite a producer key", path)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return "", err
	}
	entropy := wallet.GetEntropyCSPRNG(32)
	mnemonic, err := bip39.NewMnemonic(entropy)
	if err != nil {
		return "", err
	}
	ks := &wallet.KeyStore{Entropy: entropy, Seed: bip39.NewSeed(mnemonic, ""), Mnemonic: mnemonic}
	defer ks.Zero()
	_, kp, err := ks.DeriveForIndexPath(0)
	if err != nil {
		return "", err
	}
	ks.BaseAddress = kp.Address
	kf, err := ks.Encrypt(password)
	if err != nil {
		return "", err
	}
	kf.Path = path
	if err := kf.Write(); err != nil {
		return "", err
	}
	if err := os.Chmod(path, 0o600); err != nil {
		return "", err
	}
	// Read back: the file on disk is what the node will use.
	return Verify(path, password)
}

// Verify opens the key file with password and returns its base address.
func Verify(path, password string) (string, error) {
	kf, err := wallet.ReadKeyFile(path)
	if err != nil {
		return "", fmt.Errorf("read %s: %w", path, err)
	}
	ks, err := kf.Decrypt(password)
	if err != nil {
		if errors.Is(err, wallet.ErrWrongPassword) {
			return "", ErrWrongPassword
		}
		return "", err
	}
	defer ks.Zero()
	return ks.BaseAddress.String(), nil
}

// Address reads the base address recorded in a key file without opening it.
func Address(path string) (string, error) {
	kf, err := wallet.ReadKeyFile(path)
	if err != nil {
		return "", err
	}
	return kf.BaseAddress.String(), nil
}

// ReadConfig returns the Producer section of config.json, or nil when the
// file or the section is absent.
func ReadConfig(configPath string) (*Config, error) {
	raw, err := readRaw(configPath)
	if err != nil {
		return nil, err
	}
	section, ok := raw["Producer"]
	if !ok || bytes.Equal(bytes.TrimSpace(section), []byte("null")) {
		return nil, nil
	}
	var c Config
	if err := json.Unmarshal(section, &c); err != nil {
		return nil, fmt.Errorf("config.json Producer section: %w", err)
	}
	return &c, nil
}

// WriteConfig sets the Producer section, keeping every other section's
// content (re-indented), after copying the previous file aside as
// config.json.bak.<unix>. The result is mode 0600: it holds the password.
func WriteConfig(configPath string, c Config, now time.Time) (backup string, err error) {
	raw, err := readRaw(configPath)
	if err != nil {
		return "", err
	}
	section, err := json.Marshal(c)
	if err != nil {
		return "", err
	}
	raw["Producer"] = section
	data, err := json.MarshalIndent(raw, "", "    ")
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(filepath.Dir(configPath), 0o755); err != nil {
		return "", err
	}
	if current, err := os.ReadFile(configPath); err == nil {
		backup = fmt.Sprintf("%s.bak.%d", configPath, now.Unix())
		if err := os.WriteFile(backup, current, 0o600); err != nil {
			return "", fmt.Errorf("back up config.json: %w", err)
		}
	}
	tmp := configPath + ".tmp"
	if err := os.WriteFile(tmp, append(data, '\n'), 0o600); err != nil {
		return backup, err
	}
	if err := os.Rename(tmp, configPath); err != nil {
		_ = os.Remove(tmp)
		return backup, err
	}
	return backup, nil
}

func readRaw(configPath string) (map[string]json.RawMessage, error) {
	data, err := os.ReadFile(configPath)
	if errors.Is(err, os.ErrNotExist) {
		return map[string]json.RawMessage{}, nil
	}
	if err != nil {
		return nil, err
	}
	raw := map[string]json.RawMessage{}
	if len(bytes.TrimSpace(data)) == 0 {
		return raw, nil
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, fmt.Errorf("%s is not a JSON object: %w", configPath, err)
	}
	return raw, nil
}
