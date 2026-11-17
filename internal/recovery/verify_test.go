package recovery

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/0x3639/nomctl/internal/backup"
	"github.com/0x3639/nomctl/internal/config"
	"github.com/0x3639/nomctl/internal/lock"
	"golang.org/x/sys/unix"
)

func write(t *testing.T, path string, data []byte) {
	t.Helper()
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
}

func fixture(t *testing.T, point bool) (config.Config, Target) {
	t.Helper()
	cfg := config.Default()
	cfg.BackupDir = t.TempDir()
	cfg.ZnnDir = t.TempDir()
	write(t, filepath.Join(cfg.ZnnDir, "config.json"), []byte("live configuration"))
	target := Backup(cfg, "go-zenon_backup_test.tar.gz")
	archive := target.Path
	if point {
		var err error
		target, err = Point(cfg, "go-zenon_point_test")
		if err != nil {
			t.Fatal(err)
		}
		if err := os.MkdirAll(target.Path, 0o700); err != nil {
			t.Fatal(err)
		}
		archive = filepath.Join(target.Path, "chain.tar.gz")
		write(t, filepath.Join(target.Path, "node-binary"), []byte("saved executable; must never run"))
		sum, err := backup.SHA256File(filepath.Join(target.Path, "node-binary"))
		if err != nil {
			t.Fatal(err)
		}
		// Legacy metadata deliberately omits version and commit.
		metadata := backup.RestorePoint{ID: filepath.Base(target.Path), Service: cfg.ServiceName, CreatedAt: time.Now().UTC(), Complete: true, BinarySHA256: sum}
		data, err := json.Marshal(metadata)
		if err != nil {
			t.Fatal(err)
		}
		write(t, filepath.Join(target.Path, "point.json"), data)
	}
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	if err := tw.WriteHeader(&tar.Header{Name: "./nom/state", Typeflag: tar.TypeReg, Size: 5, Mode: 0o600}); err != nil {
		t.Fatal(err)
	}
	if _, err := tw.Write([]byte("state")); err != nil {
		t.Fatal(err)
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	write(t, archive, buf.Bytes())
	rehash(t, archive)
	return cfg, target
}

func rehash(t *testing.T, path string) {
	t.Helper()
	sum, err := backup.SHA256File(path)
	if err != nil {
		t.Fatal(err)
	}
	write(t, backup.HashPath(path), []byte(sum+"\n"))
}

func TestVerifyLegacyPointsAndBackupsWithoutTouchingNode(t *testing.T) {
	for _, point := range []bool{false, true} {
		cfg, target := fixture(t, point)
		stateDir, lockPath := t.TempDir(), filepath.Join(t.TempDir(), "operation.lock")
		// Verification must not even try to invoke a service manager or binary.
		t.Setenv("PATH", t.TempDir())
		report, err := Run(context.Background(), cfg, []Target{target}, Options{}, stateDir, lockPath)
		if err != nil || len(report.Results) != 1 || report.Results[0].Outcome != "passed" {
			t.Fatalf("%+v %v", report, err)
		}
		r := report.Results[0]
		if len(r.SHA256) != len(r.Files) || r.Finished.Before(r.Started) || r.Manifest.Files != 1 || r.Rehearsal != "not_requested" || r.Integrity != "passed" || r.BinaryVersion != "" {
			t.Fatal(r)
		}
		if s := SavedStatus(context.Background(), stateDir, target); s.State != "passed" {
			t.Fatal(s)
		}
		if data, err := os.ReadFile(filepath.Join(cfg.ZnnDir, "config.json")); err != nil || string(data) != "live configuration" {
			t.Fatal("live data changed", err)
		}
		var out bytes.Buffer
		WriteReport(&out, report)
		if !strings.Contains(out.String(), "startup have not been tested") {
			t.Fatal(out.String())
		}
	}
}

func TestFailuresPreserveArtifacts(t *testing.T) {
	for _, kind := range []string{"bad-checksum", "missing-hash", "missing-archive", "gzip-trailer", "truncated", "binary", "missing-binary", "metadata", "missing-metadata", "incomplete", "symlink", "fifo"} {
		t.Run(kind, func(t *testing.T) {
			cfg, target := fixture(t, true)
			paths, _ := target.paths()
			switch kind {
			case "bad-checksum":
				write(t, paths[1], []byte(strings.Repeat("0", 64)))
			case "missing-hash":
				if err := os.Remove(paths[1]); err != nil {
					t.Fatal(err)
				}
			case "missing-archive":
				if err := os.Remove(paths[0]); err != nil {
					t.Fatal(err)
				}
			case "gzip-trailer", "truncated":
				data, err := os.ReadFile(paths[0])
				if err != nil {
					t.Fatal(err)
				}
				if kind == "gzip-trailer" {
					data[len(data)-8] ^= 0xff
				} else {
					data = data[:len(data)-5]
				}
				write(t, paths[0], data)
				rehash(t, paths[0])
			case "binary":
				write(t, paths[3], []byte("bad binary"))
			case "missing-binary":
				if err := os.Remove(paths[3]); err != nil {
					t.Fatal(err)
				}
			case "metadata":
				write(t, paths[2], []byte("not json"))
			case "missing-metadata":
				if err := os.Remove(paths[2]); err != nil {
					t.Fatal(err)
				}
			case "incomplete":
				data, err := os.ReadFile(paths[2])
				if err != nil {
					t.Fatal(err)
				}
				write(t, paths[2], bytes.ReplaceAll(data, []byte(`"complete":true`), []byte(`"complete":false`)))
			case "symlink", "fifo":
				if err := os.Remove(paths[3]); err != nil {
					t.Fatal(err)
				}
				if kind == "symlink" {
					if err := os.Symlink(paths[0], paths[3]); err != nil {
						t.Fatal(err)
					}
				} else {
					if err := unix.Mkfifo(paths[3], 0o600); err != nil {
						t.Fatal(err)
					}
				}
			}
			r := Verify(context.Background(), cfg, target, Options{})
			if r.Outcome != "failed" || len(r.Checks) == 0 {
				t.Fatalf("accepted %s: %+v", kind, r)
			}
			if _, err := os.Stat(target.Path); err != nil {
				t.Fatal("deleted failed restore point", err)
			}
			if data, err := os.ReadFile(filepath.Join(cfg.ZnnDir, "config.json")); err != nil || string(data) != "live configuration" {
				t.Fatal("live data changed", err)
			}
		})
	}
}

func TestStatusInvalidatesEveryComponent(t *testing.T) {
	for componentIndex := 0; componentIndex < 4; componentIndex++ {
		for _, mutation := range []string{"replace", "same-size-and-mtime", "remove"} {
			cfg, target := fixture(t, true)
			dir := t.TempDir()
			r := Verify(context.Background(), cfg, target, Options{})
			if r.Outcome != "passed" {
				t.Fatal(r)
			}
			if err := saveResult(dir, r); err != nil {
				t.Fatal(err)
			}
			path := r.Files[componentIndex].Path
			before, err := os.Stat(path)
			if err != nil {
				t.Fatal(err)
			}
			data, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			switch mutation {
			case "replace":
				write(t, path+".new", data)
				if err := os.Rename(path+".new", path); err != nil {
					t.Fatal(err)
				}
			case "same-size-and-mtime":
				data[0] ^= 1
				write(t, path, data)
				if err := os.Chtimes(path, before.ModTime(), before.ModTime()); err != nil {
					t.Fatal(err)
				}
			case "remove":
				if err := os.Remove(path); err != nil {
					t.Fatal(err)
				}
			}
			if s := SavedStatus(context.Background(), dir, target); s.State != "stale" {
				t.Fatalf("component %d %s: %+v", componentIndex, mutation, s)
			}
		}
	}
}

func TestRehearsalSpaceCancellationAndConcurrentMutation(t *testing.T) {
	for _, mode := range []string{"success", "no-space-before", "no-space-during", "cancel", "changed"} {
		t.Run(mode, func(t *testing.T) {
			cfg, target := fixture(t, false)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			old := checkSpace
			t.Cleanup(func() { checkSpace = old })
			calls := 0
			checkSpace = func(_ config.Config, path string, additional int64) error {
				calls++
				if additional <= 0 {
					t.Fatal("missing space budget")
				}
				if calls == 1 && path != cfg.BackupDir {
					t.Fatal("initial check must precede staging")
				}
				if mode == "no-space-before" || mode == "no-space-during" && calls > 1 {
					return errors.New("insufficient space")
				}
				if mode == "cancel" && calls > 1 {
					cancel()
					return ctx.Err()
				}
				if mode == "changed" && calls == 1 {
					data, err := os.ReadFile(backup.HashPath(target.Path))
					if err != nil {
						t.Fatal(err)
					}
					write(t, backup.HashPath(target.Path), append(data, '\n'))
				}
				return nil
			}
			r := Verify(ctx, cfg, target, Options{Rehearse: true})
			if (r.Outcome == "passed") != (mode == "success") {
				t.Fatalf("%s: %+v", mode, r)
			}
			if mode == "success" && (calls < 3 || r.Rehearsal != "passed") {
				t.Fatal("no extraction", r, calls)
			}
			if strings.HasPrefix(mode, "no-space") && r.Integrity != "passed" {
				t.Fatal("lost independent integrity result", r)
			}
			if mode == "cancel" && r.Outcome != "cancelled" {
				t.Fatal(r)
			}
			matches, err := filepath.Glob(filepath.Join(cfg.BackupDir, ".nomctl-verify-*"))
			if err != nil || len(matches) > 0 {
				t.Fatal("left staging behind", matches, err)
			}
		})
	}
}

func TestListIncludesIncompleteAndFiltersServices(t *testing.T) {
	cfg, target := fixture(t, false)
	if err := os.Remove(backup.HashPath(target.Path)); err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(cfg.BackupDir, "other_backup_1.tar.gz"), []byte("x"))
	if err := os.MkdirAll(filepath.Join(backup.RestorePointsDir(cfg), "go-zenon_point_broken"), 0o700); err != nil {
		t.Fatal(err)
	}
	targets, err := List(cfg)
	if err != nil || len(targets) != 2 {
		t.Fatal(targets, err)
	}
}

func TestRunLockFailureCancellationAndUnfinishedState(t *testing.T) {
	cfg, target := fixture(t, false)
	dir, lockPath := t.TempDir(), filepath.Join(t.TempDir(), "lock")
	l, err := lock.Acquire(lockPath, "other operation")
	if err != nil {
		t.Fatal(err)
	}
	_, err = Run(context.Background(), cfg, []Target{target}, Options{}, dir, lockPath)
	l.Release()
	if !errors.Is(err, lock.ErrBusy) {
		t.Fatal(err)
	}
	if s := SavedStatus(context.Background(), dir, target); s.State != "never-verified" {
		t.Fatal(s)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if r := Verify(ctx, cfg, target, Options{}); r.Outcome != "cancelled" {
		t.Fatal(r)
	}
	if err := saveResult(dir, Result{Version: 1, Target: target, Started: time.Now().UTC(), Outcome: "running"}); err != nil {
		t.Fatal(err)
	}
	if s := SavedStatus(context.Background(), dir, target); s.State != "unfinished" {
		t.Fatal(s)
	}
	if _, err := Run(context.Background(), cfg, []Target{target}, Options{}, filepath.Join(target.Path, "impossible"), lockPath); err == nil {
		t.Fatal("ignored persistence failure")
	}
}
