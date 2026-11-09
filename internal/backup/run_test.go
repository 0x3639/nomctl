package backup

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestBackupRestartsBeforeCompressionAndRecovers(t *testing.T) {
	for _, mode := range []string{"active", "inactive", "copy-failure", "pressure-during-copy", "no-space"} {
		t.Run(mode, func(t *testing.T) {
			cfg := testConfig(t)
			if err := os.Mkdir(filepath.Join(cfg.ZnnDir, "nom"), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(cfg.ZnnDir, "nom", "data"), []byte("chain data"), 0o600); err != nil {
				t.Fatal(err)
			}
			commands := t.TempDir()
			state, events := filepath.Join(commands, "state"), filepath.Join(commands, "events")
			initial := "active"
			if mode == "inactive" {
				initial = "inactive"
			}
			if err := os.WriteFile(state, []byte(initial), 0o600); err != nil {
				t.Fatal(err)
			}
			t.Setenv("BACKUP_TEST_STATE", state)
			t.Setenv("BACKUP_TEST_EVENTS", events)
			t.Setenv("BACKUP_TEST_MODE", mode)
			stubCommand(t, commands, "systemctl", `
case "$1" in
 status) exit 0 ;;
 show) printf 'LoadState=loaded\nActiveState=%s\n' "$(cat "$BACKUP_TEST_STATE")" ;;
 stop) echo stop >> "$BACKUP_TEST_EVENTS"; echo inactive > "$BACKUP_TEST_STATE" ;;
 start) echo start >> "$BACKUP_TEST_EVENTS"; echo active > "$BACKUP_TEST_STATE" ;;
 *) exit 1 ;;
esac
`)
			stubCommand(t, commands, "cp", `
echo copy >> "$BACKUP_TEST_EVENTS"
[ "$BACKUP_TEST_MODE" != copy-failure ] || exit 1
exec /bin/cp "$@"
`)
			stubCommand(t, commands, "tar", `
echo "archive:$(cat "$BACKUP_TEST_STATE")" >> "$BACKUP_TEST_EVENTS"
exec /usr/bin/tar "$@"
`)
			t.Setenv("PATH", commands+":"+os.Getenv("PATH"))
			mockCapacity(t, func(string) (int64, int64, error) {
				data, _ := os.ReadFile(events)
				if mode == "no-space" || (mode == "pressure-during-copy" && strings.Contains(string(data), "copy")) {
					if mode == "pressure-during-copy" {
						files, _ := filepath.Glob(filepath.Join(cfg.BackupDir, "snapshot-*", "nom", "data"))
						for _, file := range files {
							info, err := os.Stat(file)
							if err != nil || info.Size() != 0 {
								t.Fatal("data written before space guard", err)
							}
						}
					}
					return 20 << 20, 180 << 20, nil
				}
				return 150 << 20, 50 << 20, nil
			})
			archive, err := Run(cfg, Options{})
			success := mode == "active" || mode == "inactive"
			if mode == "pressure-during-copy" && (err == nil || !strings.Contains(err.Error(), "insufficient disk space")) {
				t.Fatalf("disk-pressure cause lost: %v", err)
			}
			if (err == nil) != success {
				t.Fatalf("unexpected result: %v", err)
			}
			data, _ := os.ReadFile(events)
			want := "stop\ncopy\nstart\narchive:active\n"
			switch mode {
			case "inactive":
				want = "stop\ncopy\narchive:inactive\n"
			case "copy-failure", "pressure-during-copy":
				want = "stop\ncopy\nstart\n"
			case "no-space":
				want = ""
			}
			if string(data) != want {
				t.Fatalf("sequence %q, want %q", data, want)
			}
			if success {
				if _, err := os.Stat(archive); err != nil {
					t.Fatal(err)
				}
			}
			snapshots, _ := filepath.Glob(filepath.Join(cfg.BackupDir, "snapshot-*"))
			if len(snapshots) > 0 {
				t.Fatal("temporary copy leaked")
			}
		})
	}
}
