package recovery

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/0x3639/nomctl/internal/backup"
	"github.com/0x3639/nomctl/internal/config"
	"github.com/0x3639/nomctl/internal/lock"
	"github.com/0x3639/nomctl/internal/restore"
)

const Limits = "File integrity only. Database compatibility and successful node startup have not been tested."

type Check struct {
	Name   string `json:"name"`
	Status string `json:"status"`
	Detail string `json:"detail,omitempty"`
}

type Result struct {
	Version       int               `json:"version"`
	Target        Target            `json:"target"`
	Started       time.Time         `json:"started"`
	Finished      time.Time         `json:"finished"`
	Outcome       string            `json:"outcome"`
	Integrity     string            `json:"integrity"`
	Files         []Identity        `json:"files"`
	SHA256        map[string]string `json:"sha256"`
	Checks        []Check           `json:"checks"`
	Manifest      *restore.Manifest `json:"manifest,omitempty"`
	BinaryVersion string            `json:"binary_version,omitempty"`
	BinaryCommit  string            `json:"binary_commit,omitempty"`
	Rehearsal     string            `json:"rehearsal"`
	Limits        string            `json:"limits"`
}

type Options struct{ Rehearse bool }

type Report struct {
	Results []Result `json:"results"`
	Limits  string   `json:"limits"`
}

// Verify only reads recovery files, except for an explicitly requested private
// extraction rehearsal. No service calls, executable launches, or pruning.
func Verify(ctx context.Context, cfg config.Config, target Target, opts Options) (result Result) {
	result = Result{Version: 1, Target: target, Started: time.Now().UTC(), Outcome: "failed", Integrity: "failed",
		SHA256: map[string]string{}, Rehearsal: "not_requested", Limits: Limits}
	var opened []*component
	defer func() {
		for _, c := range opened {
			_ = c.file.Close()
		}
		result.Finished = time.Now().UTC()
		if ctx.Err() != nil {
			result.Outcome = "cancelled"
			result.Integrity = "not_completed"
		}
	}()
	check := func(name string, err error) bool {
		c := Check{Name: name, Status: "passed"}
		if err != nil {
			c.Status, c.Detail = "failed", err.Error()
		}
		result.Checks = append(result.Checks, c)
		return err == nil
	}
	paths, err := target.paths()
	if !check("recovery location", errors.Join(err, target.parents(), ctx.Err())) {
		return
	}
	for _, path := range paths {
		result.Files = append(result.Files, identify(path))
	}
	for _, path := range paths {
		c, err := openComponent(path)
		if !check("read "+filepath.Base(path), err) {
			return
		}
		opened = append(opened, c)
	}
	archive, sidecar := opened[0], opened[1]
	stored, err := sidecar.read(ctx, 4096)
	if !check("read archive checksum", err) {
		return
	}
	for _, c := range opened {
		sum, err := c.hash(ctx)
		if !check("hash "+filepath.Base(c.id.Path), err) {
			return
		}
		result.SHA256[c.id.Path] = sum
	}
	if !check("checksum sidecar unchanged", contentMatches(stored, result.SHA256[sidecar.id.Path])) {
		return
	}
	if !check("archive checksum", restore.ChecksumMatches(result.SHA256[archive.id.Path], string(stored))) {
		return
	}
	r, err := archive.reader(ctx)
	if !check("read archive", err) {
		return
	}
	manifest, err := restore.InspectReader(r)
	if !check("archive contents and gzip trailer", err) {
		return
	}
	result.Manifest = &manifest
	if target.Kind == "restore-point" {
		data, err := opened[2].read(ctx, 1<<20)
		if !check("read restore point metadata", err) {
			return
		}
		if !check("metadata unchanged", contentMatches(data, result.SHA256[opened[2].id.Path])) {
			return
		}
		point, err := backup.DecodeRestorePoint(data, target.Path, target.Service)
		if !check("restore point metadata", err) {
			return
		}
		if !point.Complete {
			check("restore point complete", errors.New("restore point is incomplete; retained files need inspection"))
			return
		}
		result.BinaryVersion, result.BinaryCommit = point.BinaryVersion, point.BinaryCommit
		if !check("saved znnd checksum", restore.ChecksumMatches(result.SHA256[opened[3].id.Path], point.BinarySHA256)) {
			return
		}
	}
	unchanged := func() error {
		if err := target.parents(); err != nil {
			return err
		}
		for i, c := range opened {
			sum, err := c.hash(ctx)
			if err != nil {
				return err
			}
			if sum != result.SHA256[c.id.Path] || c.id != result.Files[i] || !c.unchanged() {
				return errors.New("recovery files changed during verification; verify again")
			}
		}
		return nil
	}
	if !check("files unchanged during verification", unchanged()) {
		return
	}
	result.Integrity = "passed"
	if opts.Rehearse {
		result.Rehearsal = "failed"
		if !check("extraction rehearsal", rehearse(ctx, cfg, archive, manifest)) {
			return
		}
		result.Rehearsal = "passed"
		if !check("files unchanged after rehearsal", unchanged()) {
			result.Integrity = "failed"
			return
		}
	}
	if !check("verification completed", ctx.Err()) {
		return
	}
	result.Outcome = "passed"
	return
}

func contentMatches(data []byte, expected string) error {
	sum := sha256.Sum256(data)
	if hex.EncodeToString(sum[:]) != expected {
		return errors.New("recovery metadata changed during verification; verify again")
	}
	return nil
}

var checkSpace = backup.CheckRestoreSpace

func rehearse(ctx context.Context, cfg config.Config, archive *component, manifest restore.Manifest) (err error) {
	if err := checkSpace(cfg, cfg.BackupDir, manifest.StagingBytes); err != nil {
		return err
	}
	dir, err := os.MkdirTemp(cfg.BackupDir, ".nomctl-verify-")
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, os.RemoveAll(dir)) }()
	r, err := archive.reader(ctx)
	if err != nil {
		return err
	}
	return restore.ExtractReader(r, dir, func(bytes int64) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		return checkSpace(cfg, dir, bytes)
	})
}

// Run serializes verification with backup/prune/restore and persists outcomes.
// Long reads happen while znnd remains in its existing state.
func Run(ctx context.Context, cfg config.Config, targets []Target, opts Options, stateDir, lockPath string) (Report, error) {
	l, err := lock.Acquire(lockPath, "verify recovery files")
	if err != nil {
		return Report{Limits: Limits}, err
	}
	defer l.Release()
	return runLocked(ctx, cfg, targets, opts, stateDir)
}

func runLocked(ctx context.Context, cfg config.Config, targets []Target, opts Options, stateDir string) (Report, error) {
	report := Report{Limits: Limits, Results: []Result{}}
	if len(targets) == 0 {
		return report, errors.New("no recovery files found for this service and directory")
	}
	var failures []error
	for _, target := range targets {
		if err := ctx.Err(); err != nil {
			return report, errors.Join(append(failures, err)...)
		}
		// Invalidate any previous green result before starting a fresh attempt.
		if err := saveResult(stateDir, Result{Version: 1, Target: target, Started: time.Now().UTC(), Outcome: "running", Limits: Limits}); err != nil {
			return report, err
		}
		r := Verify(ctx, cfg, target, opts)
		report.Results = append(report.Results, r)
		if err := saveResult(stateDir, r); err != nil {
			return report, err
		}
		if r.Outcome != "passed" {
			failures = append(failures, fmt.Errorf("verification %s: %s", r.Outcome, target.Path))
		}
	}
	return report, errors.Join(append(failures, ctx.Err())...)
}

func WriteReport(w io.Writer, report Report) {
	fmt.Fprintln(w, report.Limits)
	for _, r := range report.Results {
		fmt.Fprintf(w, "\n%s: %s\nChecked: %s\n", r.Target.Path, r.Outcome, r.Finished.Format(time.RFC3339))
		fmt.Fprintf(w, "  File integrity: %s\n", r.Integrity)
		for _, c := range r.Checks {
			if c.Status == "failed" {
				fmt.Fprintf(w, "  %s: %s\n", c.Name, c.Detail)
			}
		}
		if r.Target.Kind == "restore-point" {
			version, commit := r.BinaryVersion, r.BinaryCommit
			if version == "" {
				version = "unavailable (legacy metadata is supported)"
			}
			if commit == "" {
				commit = "unavailable"
			}
			fmt.Fprintf(w, "  Saved znnd version: %s\n  Commit: %s\n  SHA-256: %s\n", version, commit, r.SHA256[filepath.Join(r.Target.Path, "node-binary")])
		}
		fmt.Fprintf(w, "  Extraction rehearsal: %s\n", r.Rehearsal)
	}
}
