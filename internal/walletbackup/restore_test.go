package walletbackup

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/0x3639/nomctl/internal/config"
	"github.com/0x3639/nomctl/internal/producer"
	"github.com/zenon-network/go-zenon/wallet"
)

func restoreTestConfig(t *testing.T) config.Config {
	t.Helper()
	return config.Config{ServiceName: "test-node", ZnnDir: t.TempDir(), BackupDir: t.TempDir()}
}

func writeTestFile(t *testing.T, name, contents string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(name), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(name, []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
}

func preserveRestoreHooks(t *testing.T) {
	t.Helper()
	oldRunning, oldStop, oldRestart, oldInstall, oldRollback := serviceRunning, stop, restart, installRename, rollbackRename
	t.Cleanup(func() {
		serviceRunning, stop, restart, installRename, rollbackRename = oldRunning, oldStop, oldRestart, oldInstall, oldRollback
	})
}

func assertTestFile(t *testing.T, name, contents string) {
	t.Helper()
	data, err := os.ReadFile(name)
	if err != nil || string(data) != contents {
		t.Errorf("unexpected file state for %s: %v", filepath.Base(name), err)
	}
}

func assertMissing(t *testing.T, name string) {
	t.Helper()
	if _, err := os.Lstat(name); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("expected %s to be absent: %v", filepath.Base(name), err)
	}
}

func TestRestoreRollbackPreservesOriginalExistence(t *testing.T) {
	archive := tarOf(t, map[string]string{"config.json": `{"LogLevel":"new"}`, "wallet/a": "new-a", "wallet/b": "new-b"})
	for _, hadWallet := range []bool{false, true} {
		for _, hadConfig := range []bool{false, true} {
			for _, failAt := range []int{1, 2, 3} {
				t.Run(fmt.Sprintf("wallet=%t/config=%t/install=%d", hadWallet, hadConfig, failAt), func(t *testing.T) {
					preserveRestoreHooks(t)
					cfg := restoreTestConfig(t)
					walletDir := filepath.Join(cfg.ZnnDir, "wallet")
					configPath := producer.ConfigPath(cfg.ZnnDir)
					if hadWallet {
						writeTestFile(t, filepath.Join(walletDir, "original"), "original-key")
						writeTestFile(t, filepath.Join(walletDir, "nested", "original"), "nested-key")
					}
					if hadConfig {
						writeTestFile(t, configPath, `{"LogLevel":"original"}`)
					}
					calls := 0
					installRename = func(src, dst string) error {
						calls++
						if calls == failAt {
							return errors.New("injected install failure")
						}
						return os.Rename(src, dst)
					}
					_, err := Restore(cfg, archive, false, time.Unix(1, 0))
					if err == nil || !strings.Contains(err.Error(), "previous files put back") {
						t.Fatalf("rollback result: %v", err)
					}
					if hadWallet {
						assertTestFile(t, filepath.Join(walletDir, "original"), "original-key")
						assertTestFile(t, filepath.Join(walletDir, "nested", "original"), "nested-key")
						assertMissing(t, filepath.Join(walletDir, "a"))
						assertMissing(t, filepath.Join(walletDir, "b"))
					} else {
						assertMissing(t, walletDir)
					}
					if hadConfig {
						assertTestFile(t, configPath, `{"LogLevel":"original"}`)
					} else {
						assertMissing(t, configPath)
					}
				})
			}
		}
	}
}

func TestRestoreServiceGateDoesNotTouchLiveFiles(t *testing.T) {
	for _, scenario := range []string{"running-without-restart", "unknown-state", "stop-failure"} {
		t.Run(scenario, func(t *testing.T) {
			preserveRestoreHooks(t)
			cfg := restoreTestConfig(t)
			live := producer.ConfigPath(cfg.ZnnDir)
			writeTestFile(t, live, `{"LogLevel":"original"}`)
			stopCalls, restartCalls := 0, 0
			SetServiceHooks(func(string) (bool, error) {
				if scenario == "unknown-state" {
					return false, errors.New("injected state failure")
				}
				return scenario == "running-without-restart", nil
			}, func(string) error {
				stopCalls++
				return errors.New("injected stop failure")
			}, func(string) error {
				restartCalls++
				return nil
			})
			res, err := Restore(cfg, tarOf(t, map[string]string{"config.json": `{}`}), false, time.Unix(1, 0))
			if err == nil {
				t.Fatal("restore passed the service gate")
			}
			if res.SafetyDir != "" || restartCalls != 0 {
				t.Fatalf("unexpected safety directory or restart: %+v, %d", res, restartCalls)
			}
			wantStops := 0
			if scenario == "stop-failure" {
				wantStops = 1
			}
			if stopCalls != wantStops {
				t.Errorf("stop calls = %d, want %d", stopCalls, wantStops)
			}
			assertTestFile(t, live, `{"LogLevel":"original"}`)
			entries, err := os.ReadDir(cfg.ZnnDir)
			if err != nil || len(entries) != 1 {
				t.Errorf("temporary directory left behind: %v, %v", entries, err)
			}
		})
	}
}

func TestRestoreRestartFailureRecovery(t *testing.T) {
	for _, recoveryStopFails := range []bool{false, true} {
		t.Run(fmt.Sprintf("recovery-stop-fails=%t", recoveryStopFails), func(t *testing.T) {
			preserveRestoreHooks(t)
			cfg := restoreTestConfig(t)
			configPath := producer.ConfigPath(cfg.ZnnDir)
			keyPath := filepath.Join(cfg.ZnnDir, "wallet", "test-key")
			writeTestFile(t, configPath, `{"LogLevel":"original"}`)
			writeTestFile(t, keyPath, "original-key")
			var events []string
			stopCalls := 0
			SetServiceHooks(func(string) (bool, error) { return true, nil }, func(string) error {
				stopCalls++
				events = append(events, "stop")
				if stopCalls == 1 {
					assertTestFile(t, configPath, `{"LogLevel":"original"}`)
					assertTestFile(t, keyPath, "original-key")
				} else {
					assertTestFile(t, configPath, `{"LogLevel":"restored"}`)
					assertTestFile(t, keyPath, "restored-key")
					if recoveryStopFails {
						return errors.New("injected recovery stop failure")
					}
				}
				return nil
			}, func(string) error {
				events = append(events, "restart")
				assertTestFile(t, configPath, `{"LogLevel":"restored"}`)
				assertTestFile(t, keyPath, "restored-key")
				return errors.New("injected restart failure")
			})
			archive := tarOf(t, map[string]string{"config.json": `{"LogLevel":"restored"}`, "wallet/test-key": "restored-key"})
			res, err := Restore(cfg, archive, true, time.Unix(1, 0))
			if err == nil || res.Restarted || !res.WasRunning || res.SafetyDir == "" {
				t.Fatalf("result: %+v, %v", res, err)
			}
			if got := strings.Join(events, ","); got != "stop,restart,stop" {
				t.Errorf("service order = %s", got)
			}
			if recoveryStopFails {
				if !strings.Contains(err.Error(), "cannot confirm node stopped") {
					t.Errorf("ambiguous recovery error: %v", err)
				}
				assertTestFile(t, configPath, `{"LogLevel":"restored"}`)
				assertTestFile(t, keyPath, "restored-key")
				assertTestFile(t, filepath.Join(res.SafetyDir, "config.json"), `{"LogLevel":"original"}`)
				assertTestFile(t, filepath.Join(res.SafetyDir, "wallet", "test-key"), "original-key")
			} else {
				if !strings.Contains(err.Error(), "previous files put back; node left stopped") {
					t.Errorf("recovery outcome missing: %v", err)
				}
				assertTestFile(t, configPath, `{"LogLevel":"original"}`)
				assertTestFile(t, keyPath, "original-key")
			}
		})
	}
}

func TestRestoreSafetyLivesOnDataFilesystem(t *testing.T) {
	preserveRestoreHooks(t)
	cfg := restoreTestConfig(t)
	// An unavailable backup destination must not affect the local safety
	// rename. It may normally be a separate removable filesystem.
	backupBase := filepath.Join(cfg.BackupDir, "unavailable")
	writeTestFile(t, backupBase, "not a directory")
	cfg.BackupDir = filepath.Join(backupBase, "backups")
	writeTestFile(t, producer.ConfigPath(cfg.ZnnDir), `{"LogLevel":"original"}`)
	var stops, restarts int
	SetServiceHooks(func(string) (bool, error) { return false, nil }, func(string) error { stops++; return nil }, func(string) error { restarts++; return nil })
	archive := tarOf(t, map[string]string{"config.json": `{}`})
	first, err := Restore(cfg, archive, true, time.Unix(1, 0))
	if err != nil {
		t.Fatal(err)
	}
	second, err := Restore(cfg, archive, true, time.Unix(1, 0))
	if err != nil {
		t.Fatal(err)
	}
	if first.SafetyDir == second.SafetyDir {
		t.Fatal("repeated timestamps reused a safety directory")
	}
	for _, safety := range []string{first.SafetyDir, second.SafetyDir} {
		if filepath.Dir(safety) != cfg.ZnnDir {
			t.Errorf("safety directory is not on the data filesystem: %s", safety)
		}
		if info, err := os.Stat(safety); err != nil || info.Mode().Perm() != 0o700 {
			t.Errorf("safety directory is not private: %v", err)
		}
	}
	assertTestFile(t, filepath.Join(first.SafetyDir, "config.json"), `{"LogLevel":"original"}`)
	assertTestFile(t, filepath.Join(second.SafetyDir, "config.json"), `{}`)
	if stops != 2 || restarts != 0 || first.Restarted || second.Restarted {
		t.Errorf("inactive node unexpectedly restarted: stops=%d restarts=%d", stops, restarts)
	}
}

func TestRestoreVerifiesProducerIndex(t *testing.T) {
	cfg, baseAddress := node(t)
	keyFile, err := wallet.ReadKeyFile(producer.KeyFilePath(cfg.ZnnDir))
	if err != nil {
		t.Fatal(err)
	}
	keyStore, err := keyFile.Decrypt("kpw")
	if err != nil {
		t.Fatal(err)
	}
	defer keyStore.Zero()
	_, selectedKey, err := keyStore.DeriveForIndexPath(7)
	if err != nil {
		t.Fatal(err)
	}
	selectedAddress := selectedKey.Address.String()
	if selectedAddress == baseAddress {
		t.Fatal("test requires distinct producer and base addresses")
	}
	if _, err := producer.WriteConfig(producer.ConfigPath(cfg.ZnnDir), producer.Config{
		Index: 7, KeyFilePath: "producer", Password: "kpw", Address: selectedAddress,
	}, time.Unix(1, 0)); err != nil {
		t.Fatal(err)
	}
	files, err := collect(cfg.ZnnDir)
	if err != nil {
		t.Fatal(err)
	}
	archive, err := pack(cfg.ZnnDir, files)
	if err != nil {
		t.Fatal(err)
	}
	res, err := Restore(cfg, archive, false, time.Unix(2, 0))
	if err != nil || res.Address != selectedAddress {
		t.Fatalf("valid nonzero producer index refused: %v", err)
	}
	original, err := os.ReadFile(producer.ConfigPath(cfg.ZnnDir))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := producer.WriteConfig(producer.ConfigPath(cfg.ZnnDir), producer.Config{
		Index: 7, KeyFilePath: "producer", Password: "kpw", Address: baseAddress,
	}, time.Unix(3, 0)); err != nil {
		t.Fatal(err)
	}
	mismatch, err := pack(cfg.ZnnDir, files)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(producer.ConfigPath(cfg.ZnnDir), original, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Restore(cfg, mismatch, false, time.Unix(4, 0)); err == nil || !strings.Contains(err.Error(), "expects") {
		t.Fatalf("base address accepted for nonzero index: %v", err)
	}
	current, err := os.ReadFile(producer.ConfigPath(cfg.ZnnDir))
	if err != nil || !bytes.Equal(current, original) {
		t.Error("refused archive changed the live config")
	}
}

func TestRestoreIncompleteRollbackKeepsSafetyFiles(t *testing.T) {
	preserveRestoreHooks(t)
	cfg := restoreTestConfig(t)
	configPath := producer.ConfigPath(cfg.ZnnDir)
	writeTestFile(t, configPath, `{"LogLevel":"original"}`)
	writeTestFile(t, filepath.Join(cfg.ZnnDir, "wallet", "original"), "original-key")
	stopCalls, restartCalls := 0, 0
	SetServiceHooks(func(string) (bool, error) { return true, nil }, func(string) error {
		stopCalls++
		return nil
	}, func(string) error {
		restartCalls++
		return nil
	})
	installRename = func(src, dst string) error {
		if filepath.Base(dst) == "replacement" {
			return errors.New("injected install failure")
		}
		return os.Rename(src, dst)
	}
	rollbackRename = func(src, dst string) error {
		if filepath.Base(dst) == "config.json" {
			return errors.New("injected recovery failure")
		}
		return os.Rename(src, dst)
	}
	archive := tarOf(t, map[string]string{"config.json": `{}`, "wallet/replacement": "replacement-key"})
	res, err := Restore(cfg, archive, true, time.Unix(1, 0))
	if err == nil || !strings.Contains(err.Error(), "could not all be put back; node left stopped") || !strings.Contains(err.Error(), res.SafetyDir) {
		t.Fatalf("incomplete recovery not clearly reported: %v", err)
	}
	if stopCalls != 1 || restartCalls != 0 || res.Restarted {
		t.Fatalf("incomplete recovery attempted restart: stops=%d restarts=%d", stopCalls, restartCalls)
	}
	assertMissing(t, configPath)
	assertTestFile(t, filepath.Join(res.SafetyDir, "config.json"), `{"LogLevel":"original"}`)
	assertTestFile(t, filepath.Join(cfg.ZnnDir, "wallet", "original"), "original-key")
	assertMissing(t, filepath.Join(cfg.ZnnDir, "wallet", "replacement"))
}
