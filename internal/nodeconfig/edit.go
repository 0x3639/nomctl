package nodeconfig

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// EditOutcome tells the caller how an Edit session ended.
type EditOutcome int

// Edit outcomes.
const (
	EditSaved     EditOutcome = iota // validated and written
	EditUnchanged                    // the editor left the file as it was
	EditAborted                      // invalid and the user chose not to retry
)

// Editor runs an interactive editor on path and returns when it exits.
type Editor func(path string) error

// Retry asks whether to reopen the editor after a validation failure.
type Retry func(problem error) (bool, error)

// Edit opens a copy of config.json in the editor, the way visudo does:
// nothing reaches path until the edited copy parses and validates. On a
// problem, retry decides between reopening the copy (with the problem
// shown) and aborting. Returns the outcome and the backup path when saved.
func Edit(path string, editor Editor, retry Retry, now time.Time) (EditOutcome, string, error) {
	original, err := os.ReadFile(path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return EditAborted, "", err
	}
	if len(strings.TrimSpace(string(original))) == 0 {
		original = []byte("{\n}\n")
	}
	dir, err := os.MkdirTemp(filepath.Dir(path), ".config-edit-")
	if err != nil {
		return EditAborted, "", err
	}
	defer func() { _ = os.RemoveAll(dir) }()
	copyPath := filepath.Join(dir, "config.json")
	if err := os.WriteFile(copyPath, original, 0o600); err != nil {
		return EditAborted, "", err
	}
	for {
		if err := editor(copyPath); err != nil {
			return EditAborted, "", fmt.Errorf("editor: %w", err)
		}
		edited, err := os.ReadFile(copyPath)
		if err != nil {
			return EditAborted, "", err
		}
		if string(edited) == string(original) {
			return EditUnchanged, "", nil
		}
		doc, err := Parse(edited)
		if err == nil {
			err = ValidateStrict(doc)
		}
		if err == nil {
			backup, err := Save(path, doc, now)
			if err != nil {
				return EditAborted, "", err
			}
			return EditSaved, backup, nil
		}
		again, rerr := retry(err)
		if rerr != nil {
			return EditAborted, "", rerr
		}
		if !again {
			return EditAborted, "", nil
		}
	}
}

// DefaultEditorCommand is the editor to run when $VISUAL and $EDITOR are
// unset.
const DefaultEditorCommand = "nano"

// EditorCommand returns the user's editor command line, split on spaces.
func EditorCommand() []string {
	for _, env := range []string{"VISUAL", "EDITOR"} {
		if v := strings.TrimSpace(os.Getenv(env)); v != "" {
			return strings.Fields(v)
		}
	}
	return []string{DefaultEditorCommand}
}
