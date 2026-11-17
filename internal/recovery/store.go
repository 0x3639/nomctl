package recovery

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/0x3639/nomctl/internal/fsx"
)

const StateDir = "/var/lib/nomctl/verification"

func key(value any) string {
	data, _ := json.Marshal(value)
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func saveJSON(path string, value any) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	data, err := json.Marshal(value)
	if err != nil {
		return err
	}
	return fsx.WriteRuntimeFile(path, append(data, '\n'), 0o600)
}

func saveResult(dir string, r Result) error {
	if err := saveJSON(filepath.Join(dir, key(r.Target)+".json"), r); err != nil {
		return fmt.Errorf("save verification result: %w", err)
	}
	return nil
}

type Status struct {
	Target Target  `json:"target"`
	State  string  `json:"state"`
	Reason string  `json:"reason,omitempty"`
	Last   *Result `json:"last,omitempty"`
}

// SavedStatus compares both file metadata and content hashes. Timestamps alone
// can miss edits on filesystems with coarse change-time resolution. It does not
// rerun archive inspection or extraction; the result still has its original time.
func SavedStatus(ctx context.Context, dir string, target Target) Status {
	s := Status{Target: target, State: "never-verified"}
	data, err := fsx.ReadRuntimeFile(filepath.Join(dir, key(target)+".json"), 4<<20)
	if errors.Is(err, os.ErrNotExist) {
		return s
	}
	if err != nil {
		s.State, s.Reason = "unknown", err.Error()
		return s
	}
	var r Result
	if err := json.Unmarshal(data, &r); err != nil || r.Version != 1 || r.Target != target || r.Started.IsZero() {
		s.State, s.Reason = "unknown", "invalid saved verification result"
		return s
	}
	s.Last = &r
	if r.Outcome == "running" || r.Finished.IsZero() {
		s.State, s.Reason = "unfinished", "verification started but has no saved completion"
		return s
	}
	s.State = r.Outcome
	paths, err := target.paths()
	if err != nil || len(paths) != len(r.Files) || target.parents() != nil {
		s.State, s.Reason = "stale", "recovery files are unavailable or need a fresh verification"
		return s
	}
	for i, path := range paths {
		if r.Files[i] != identify(path) {
			s.State, s.Reason = "stale", "a recovery component changed or disappeared; verify again"
			return s
		}
		if expected, ok := r.SHA256[path]; ok {
			c, err := openComponent(path)
			if err != nil {
				s.State, s.Reason = "stale", "cannot read recovery files; verify again"
				return s
			}
			sum, err := c.hash(ctx)
			stable := c.unchanged()
			_ = c.file.Close()
			if err != nil || !stable || sum != expected {
				s.State, s.Reason = "stale", "recovery contents changed or could not be read; verify again"
				if ctx.Err() != nil {
					s.State, s.Reason = "unknown", ctx.Err().Error()
				}
				return s
			}
		} else if r.Outcome == "passed" {
			s.State, s.Reason = "unknown", "saved result lacks a content identity; verify again"
			return s
		}
	}
	return s
}

func WriteStatus(w io.Writer, states []Status) {
	fmt.Fprintln(w, Limits)
	fmt.Fprintln(w, "Content identities were rechecked; archive inspection and rehearsal retain their original check time.")
	if len(states) == 0 {
		fmt.Fprintln(w, "No recovery files found.")
	}
	for _, s := range states {
		fmt.Fprintf(w, "%s: %s", s.Target.Path, s.State)
		if s.Last != nil {
			fmt.Fprintf(w, " (last attempt %s)", s.Last.Started.Format(time.RFC3339))
		}
		fmt.Fprintln(w)
		if s.Reason != "" {
			fmt.Fprintln(w, "  "+s.Reason)
		}
		if s.Last != nil && s.State == "failed" {
			for _, c := range s.Last.Checks {
				if c.Status == "failed" {
					fmt.Fprintf(w, "  %s: %s\n", c.Name, c.Detail)
				}
			}
		}
	}
}
