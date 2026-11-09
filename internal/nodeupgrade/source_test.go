package nodeupgrade

import (
	"context"
	"github.com/0x3639/nomctl/internal/backup"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSourceUpgradeBuildsBeforeDowntime(t *testing.T) {
	for _, failure := range []string{"", "clone", "build", "empty-build"} {
		name := failure
		if name == "" {
			name = "success"
		}
		t.Run(name, func(t *testing.T) {
			cfg, _, events := fixture(t)
			cfg.RestorePointKeepDays = 7
			cfg.WorkDir = filepath.Join(filepath.Dir(cfg.ZnnDir), "work")
			put(t, filepath.Join(cfg.SourceDir(), "local-changes"), "keep checkout")
			cfg.RepoURL = "https://example.invalid/default.git"
			cfg.BranchName = "default"
			t.Setenv("SOURCE_FAILURE", failure)
			commands := strings.Split(os.Getenv("PATH"), string(os.PathListSeparator))[0]
			for _, tool := range []string{"make", "gcc"} {
				stub(t, commands, tool, "exit 99\n")
			}
			stub(t, commands, "git", `
[ "$(cat "$TEST_STATE")" = active ] || exit 8
case "$1" in
 clone)
  echo clone >> "$TEST_EVENTS"
  [ "$SOURCE_FAILURE" != clone ] || exit 1
  [ "$2" = --branch ] && [ "$3" = release-branch ] && [ "$4" = --single-branch ] && [ "$5" = -- ] && [ "$6" = https://example.invalid/chosen.git ] || exit 9
  mkdir -p "$7" ;;
 -C)
  [ "$3" = rev-parse ] && [ "$4" = HEAD ] || exit 9
  echo 1111111111111111111111111111111111111111 ;;
 *) exit 9 ;;
esac
`)
			stub(t, filepath.Dir(cfg.GoBinary()), "go", `
case "$1" in
 version) echo 'go version go1.23.0 linux/amd64' ;;
 build)
  [ "$(cat "$TEST_STATE")" = active ] || exit 8
  echo build >> "$TEST_EVENTS"
  [ "$SOURCE_FAILURE" != build ] || exit 1
  [ "$2" = -o ] && [ "$4" = ./cmd/znnd ] || exit 9
  [ "$GO111MODULE" = on ] && [ "$GOWORK" = off ] || exit 9
  mkdir -p "$(dirname "$3")"
  if [ "$SOURCE_FAILURE" = empty-build ]; then touch "$3"; else printf new > "$3"; fi ;;
 *) exit 9 ;;
esac
`)
			err := FromSource(context.Background(), cfg, "https://example.invalid/chosen.git", "release-branch")
			if (err != nil) != (failure != "") {
				t.Fatalf("upgrade: %v", err)
			}
			calls, _ := os.ReadFile(events)
			want := "clone\nbuild\nstop\ncopy\nstart:new\narchive:new\n"
			if failure == "clone" {
				want = "clone\n"
			} else if failure != "" {
				want = "clone\nbuild\n"
			}
			if string(calls) != want {
				t.Fatalf("calls=%q error=%v", calls, err)
			}
			points, err := backup.ListRestorePoints(cfg)
			if err != nil {
				t.Fatal(err)
			}
			if failure == "" {
				if len(points) != 1 || !points[0].Complete || points[0].KeepDays != 7 {
					t.Fatalf("source upgrade restore point missing: %+v", points)
				}
				binary, err := os.ReadFile(points[0].BinaryPath())
				if err != nil || string(binary) != "old" {
					t.Fatal("source upgrade lost original binary", err)
				}
			} else if len(points) != 0 {
				t.Fatal("failed build created a restore point")
			}
			installed, _ := os.ReadFile(cfg.BinaryPath())
			expected := "new"
			if failure != "" {
				expected = "old"
			}
			if string(installed) != expected {
				t.Fatal("incorrect installed binary", string(installed))
			}
			checkout, _ := os.ReadFile(filepath.Join(cfg.SourceDir(), "local-changes"))
			if string(checkout) != "keep checkout" {
				t.Fatal("existing checkout changed")
			}
			leftovers, _ := filepath.Glob(filepath.Join(cfg.WorkDir, "node-upgrade-*"))
			if len(leftovers) != 0 {
				t.Fatal("source staging left behind", leftovers)
			}
		})
	}
}

func TestSourceUpgradeCancelledBeforeWork(t *testing.T) {
	cfg, _, events := fixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := FromSource(ctx, cfg, "", ""); err == nil {
		t.Fatal("cancellation ignored")
	}
	if _, err := os.Stat(events); !os.IsNotExist(err) {
		t.Fatal("service changed after cancellation")
	}
}
