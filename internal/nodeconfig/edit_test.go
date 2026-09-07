package nodeconfig

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestEditFlow(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	if err := os.WriteFile(path, []byte(sample), 0o600); err != nil {
		t.Fatal(err)
	}
	now := time.Unix(1_800_000_000, 0)
	write := func(content string) Editor {
		return func(p string) error {
			if !strings.HasPrefix(p, dir) || p == path {
				t.Fatalf("editor opened %s, not a copy", p)
			}
			return os.WriteFile(p, []byte(content), 0o600)
		}
	}
	noRetry := func(error) (bool, error) { return false, nil }

	// Unchanged: nothing written, no backup.
	out, backup, err := Edit(path, func(string) error { return nil }, noRetry, now)
	if err != nil || out != EditUnchanged || backup != "" {
		t.Fatalf("unchanged: %v %q %v", out, backup, err)
	}
	// Invalid, no retry: original intact.
	out, _, err = Edit(path, write(`{"LogLevel": "loud"}`), noRetry, now)
	if err != nil || out != EditAborted {
		t.Fatalf("aborted: %v %v", out, err)
	}
	if b, _ := os.ReadFile(path); string(b) != sample {
		t.Error("original modified by an aborted edit")
	}
	// Invalid, retry once with a fix.
	calls := 0
	editor := func(p string) error {
		calls++
		if calls == 1 {
			return os.WriteFile(p, []byte(`{"LogLevel": "loud"}`), 0o600)
		}
		return os.WriteFile(p, []byte(`{"LogLevel": "warn", "Net": {"MaxPeers": 70}}`), 0o600)
	}
	var seen error
	out, backup, err = Edit(path, editor, func(e error) (bool, error) { seen = e; return true, nil }, now)
	if err != nil || out != EditSaved || backup == "" || seen == nil || !strings.Contains(seen.Error(), "LogLevel") {
		t.Fatalf("retry: out=%v backup=%q err=%v seen=%v", out, backup, err, seen)
	}
	d, _ := Load(path)
	if v, _, _ := d.Get("Net.MaxPeers"); v != float64(70) {
		t.Errorf("edited value not saved: %v", v)
	}
	if b, _ := os.ReadFile(backup); string(b) != sample {
		t.Error("backup is not the previous file")
	}
	// Editor failure surfaces.
	if _, _, err := Edit(path, func(string) error { return errors.New("no tty") }, noRetry, now); err == nil {
		t.Error("editor failure swallowed")
	}
	// Missing file: the copy starts as an empty object.
	missing := filepath.Join(dir, "new.json")
	out, _, err = Edit(missing, func(p string) error {
		b, _ := os.ReadFile(p)
		if strings.TrimSpace(string(b)) != "{\n}" {
			t.Errorf("seed = %q", b)
		}
		return os.WriteFile(p, []byte(`{"Name": "x"}`), 0o600)
	}, noRetry, now)
	if err != nil || out != EditSaved {
		t.Fatalf("new file: %v %v", out, err)
	}
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".config-edit-") {
			t.Error("temp dir left behind")
		}
	}
}

func TestEditorCommand(t *testing.T) {
	t.Setenv("VISUAL", "")
	t.Setenv("EDITOR", "")
	if got := EditorCommand(); len(got) != 1 || got[0] != DefaultEditorCommand {
		t.Errorf("default = %v", got)
	}
	t.Setenv("EDITOR", "vim -u NONE")
	if got := EditorCommand(); strings.Join(got, " ") != "vim -u NONE" {
		t.Errorf("EDITOR = %v", got)
	}
	t.Setenv("VISUAL", "code --wait")
	if got := EditorCommand(); got[0] != "code" {
		t.Errorf("VISUAL should win: %v", got)
	}
}
