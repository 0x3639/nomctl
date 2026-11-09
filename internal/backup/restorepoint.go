package backup

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/0x3639/nomctl/internal/config"
	"github.com/0x3639/nomctl/internal/logx"
)

// RestorePoint is kept in its own directory, outside List/Prune and the
// disk-pressure deletion pool. KeepDays is a UI suggestion, never an automatic
// deletion policy. The matching node binary survives subsequent upgrades.
type RestorePoint struct {
	ID            string    `json:"id"`
	Service       string    `json:"service"`
	CreatedAt     time.Time `json:"created_at"`
	KeepDays      int       `json:"keep_days"`
	Label         string    `json:"label"`
	Complete      bool      `json:"complete"`
	BinarySHA256  string    `json:"binary_sha256"`
	BinaryVersion string    `json:"binary_version,omitempty"`
	BinaryCommit  string    `json:"binary_commit,omitempty"`
	Dir           string    `json:"-"`
	Size          int64     `json:"-"`
	Problem       string    `json:"-"`
}

func RestorePointsDir(cfg config.Config) string {
	return filepath.Join(cfg.BackupDir, "restore-points")
}
func (p RestorePoint) ArchivePath() string { return filepath.Join(p.Dir, "chain.tar.gz") }
func (p RestorePoint) BinaryPath() string  { return filepath.Join(p.Dir, "node-binary") }
func (p RestorePoint) Eligible(now time.Time) bool {
	return p.Problem == "" && p.Complete && p.KeepDays > 0 && !now.Before(p.CreatedAt.Add(time.Duration(p.KeepDays)*24*time.Hour))
}

func ParseRestorePointKeep(s string) (int, error) {
	s = strings.TrimSpace(strings.ToLower(s))
	if s == "indefinite" {
		return 0, nil
	}
	n, err := strconv.Atoi(s)
	if err != nil || n < 1 || n > 30 {
		return 0, fmt.Errorf("choose 1-30 days or indefinite")
	}
	return n, nil
}

// PrepareRestorePoint checks the full chain-copy/archive/binary budget before
// service downtime, then preserves the currently installed binary. The caller
// holds the operation lock throughout Prepare, Capture, and Finish.
func PrepareRestorePoint(cfg config.Config, label string) (*Snapshot, error) {
	info, err := os.Lstat(cfg.BinaryPath())
	if err != nil {
		return nil, fmt.Errorf("read installed binary for restore point: %w", err)
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("restore point requires a regular installed node binary")
	}
	s, err := prepare(cfg, info.Size()+64<<10)
	if err != nil {
		return nil, err
	}
	fail := func(err error) (*Snapshot, error) { return nil, errors.Join(err, s.Discard()) }
	root := RestorePointsDir(cfg)
	if err := os.MkdirAll(root, 0o700); err != nil {
		return fail(err)
	}
	if err := realDirectory(root); err != nil {
		return fail(err)
	}
	dir, err := os.MkdirTemp(root, cfg.ServiceName+"_point_"+time.Now().Format("20060102_150405")+"-")
	if err != nil {
		return fail(err)
	}
	point := &RestorePoint{ID: filepath.Base(dir), Service: cfg.ServiceName, CreatedAt: time.Now().UTC(), KeepDays: cfg.RestorePointKeepDays, Label: label, Dir: dir}
	if err := point.save(); err != nil {
		_ = os.RemoveAll(dir)
		return fail(err)
	}
	if err := os.Rename(s.dir, filepath.Join(dir, "data")); err != nil {
		_ = os.RemoveAll(dir)
		return fail(err)
	}
	s.dir, s.point = filepath.Join(dir, "data"), point
	if err := copyPointBinary(cfg, cfg.BinaryPath(), point.BinaryPath()); err != nil {
		return fail(err)
	}
	point.BinarySHA256, err = SHA256File(point.BinaryPath())
	if err != nil {
		return fail(err)
	}
	point.BinaryVersion, point.BinaryCommit = savedBinaryInfo(point.BinaryPath())
	if err := point.save(); err != nil {
		return fail(err)
	}
	return s, nil
}

func copyPointBinary(cfg config.Config, src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o700)
	if err != nil {
		return err
	}
	_, copyErr := io.CopyBuffer(spaceWriter{cfg: cfg, dst: out}, struct{ io.Reader }{in}, make([]byte, 1<<20))
	return errors.Join(copyErr, out.Close())
}

func (p RestorePoint) save() error {
	data, err := json.MarshalIndent(p, "", "  ")
	if err != nil {
		return err
	}
	tmp := filepath.Join(p.Dir, "point.json.tmp")
	if err := os.WriteFile(tmp, append(data, '\n'), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, filepath.Join(p.Dir, "point.json"))
}

func (s *Snapshot) finishRestorePoint() (string, error) {
	p := s.point
	archive := p.ArchivePath()
	if err := writeArchiveGuarded(s.ctx, s.dir, archive, &s.cfg); err != nil {
		return "", fmt.Errorf("finish restore point (recovery copy retained at %s): %w", s.dir, err)
	}
	if err := ensureSpace(s.cfg, 4096); err != nil {
		return "", err
	}
	p.Complete = true
	if err := p.save(); err != nil {
		p.Complete = false
		return "", fmt.Errorf("save restore point metadata (recovery copy retained at %s): %w", s.dir, err)
	}
	if err := os.RemoveAll(s.dir); err != nil {
		return archive, err
	}
	logx.Success("Restore point created: " + p.ID + " (deleted only through the restore-point menu)")
	slog.Info("Saved chain data and znnd. " + p.BinaryDescription())
	return archive, nil
}

// BinaryDescription identifies the saved executable, including legacy points
// without version metadata. Missing information is never inferred from labels.
func (p RestorePoint) BinaryDescription() string {
	available := func(value string) string {
		if value == "" {
			return "unavailable"
		}
		return value
	}
	return fmt.Sprintf("znnd version: %s\nCommit hash: %s\nSHA-256: %s", available(p.BinaryVersion), available(p.BinaryCommit), available(p.BinarySHA256))
}

func RunRestorePoint(cfg config.Config, label string) (string, error) {
	s, err := PrepareRestorePoint(cfg, label)
	if err != nil {
		return "", err
	}
	return runSnapshot(cfg, s)
}

func realDirectory(path string) error {
	st, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !st.IsDir() {
		return fmt.Errorf("expected a real directory: %s", path)
	}
	return nil
}

func restorePointPath(cfg config.Config, id string) (string, error) {
	if id == "" || filepath.Base(id) != id || !strings.HasPrefix(id, cfg.ServiceName+"_point_") {
		return "", fmt.Errorf("invalid restore point ID")
	}
	root := RestorePointsDir(cfg)
	if err := realDirectory(root); err != nil {
		return "", err
	}
	dir := filepath.Join(root, id)
	if err := realDirectory(dir); err != nil {
		return "", err
	}
	return dir, nil
}

func ReadRestorePoint(cfg config.Config, id string) (RestorePoint, error) {
	dir, err := restorePointPath(cfg, id)
	if err != nil {
		return RestorePoint{}, err
	}
	var p RestorePoint
	data, err := os.ReadFile(filepath.Join(dir, "point.json"))
	if err != nil {
		return p, err
	}
	if err := json.Unmarshal(data, &p); err != nil {
		return p, err
	}
	if p.ID != id || p.Service != cfg.ServiceName || p.CreatedAt.IsZero() || p.KeepDays < 0 || p.KeepDays > 30 {
		return p, fmt.Errorf("invalid restore point metadata: %s", id)
	}
	p.Dir = dir
	return p, nil
}

// ListRestorePoints includes incomplete/damaged points for explicit UI cleanup.
// They are never eligible for bulk pruning; users must select them individually.
func ListRestorePoints(cfg config.Config) ([]RestorePoint, error) {
	root := RestorePointsDir(cfg)
	if err := realDirectory(root); err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		return nil, err
	}
	var points []RestorePoint
	for _, entry := range entries {
		if !entry.IsDir() || !strings.HasPrefix(entry.Name(), cfg.ServiceName+"_point_") {
			continue
		}
		p, err := ReadRestorePoint(cfg, entry.Name())
		if err != nil {
			p = RestorePoint{ID: entry.Name(), Service: cfg.ServiceName, Dir: filepath.Join(root, entry.Name()), Label: "Incomplete or damaged restore point", Problem: err.Error()}
		}
		err = filepath.WalkDir(p.Dir, func(_ string, e fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			info, err := e.Info()
			if err != nil {
				return err
			}
			p.Size += info.Size()
			return nil
		})
		if err != nil {
			p.Problem = err.Error()
		}
		points = append(points, p)
	}
	sort.Slice(points, func(i, j int) bool { return points[i].CreatedAt.After(points[j].CreatedAt) })
	return points, nil
}

// DeleteRestorePoint must only be called by the UI after explicit confirmation,
// under the operation lock. No retention or disk-pressure path calls this.
func DeleteRestorePoint(cfg config.Config, id string) error {
	dir, err := restorePointPath(cfg, id)
	if err != nil {
		return err
	}
	slog.Info("Deleting restore point selected by user: " + id)
	return os.RemoveAll(dir)
}

func VerifyRestorePointBinary(p RestorePoint) error {
	st, err := os.Lstat(p.BinaryPath())
	if err != nil {
		return err
	}
	if !p.Complete || !st.Mode().IsRegular() || len(p.BinarySHA256) != 64 {
		return fmt.Errorf("restore point is incomplete or has no verified binary")
	}
	sum, err := SHA256File(p.BinaryPath())
	if err != nil {
		return err
	}
	if sum != p.BinarySHA256 {
		return fmt.Errorf("restore point binary checksum does not match")
	}
	return nil
}
