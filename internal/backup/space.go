package backup

import (
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"

	"github.com/0x3639/nomctl/internal/config"
	"github.com/0x3639/nomctl/internal/fsx"
)

const MaxDiskUsedPercent = 75

// Leave room for filesystem metadata, the hash, and activity by the live node.
const diskHeadroom int64 = 16 << 20

var diskCapacity = fsx.DiskCapacity

// copyBudget uses apparent file sizes (including sparse files), with generous
// per-entry allocation/metadata overhead. No compression savings are assumed.
func copyBudget(root string) (int64, error) {
	var size int64
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if !info.IsDir() && !info.Mode().IsRegular() {
			return fmt.Errorf("unsupported chain-data entry: %s", path)
		}
		// A backup must be restorable; links and special files are not accepted.
		size += info.Size() + 64<<10
		return nil
	})
	return size, err
}

func dataBudget(cfg config.Config) (int64, error) {
	var size int64
	for _, folder := range Folders {
		n, err := copyBudget(filepath.Join(cfg.ZnnDir, folder))
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return 0, err
		}
		size += n
	}
	return size, nil
}

// ensureSpace includes every file on the destination filesystem in the 75%
// ceiling. It removes only completed chain archives belonging to this service.
// Callers hold the operation lock, including during compression and pruning.
func ensureSpace(cfg config.Config, additional int64) error {
	var infos []Info
	listed := false
	for {
		available, used, err := diskCapacity(cfg.BackupDir)
		if err != nil {
			return fmt.Errorf("check disk space: %w", err)
		}
		// Divide first to avoid overflow. Rounding down is conservative.
		limit := (used + available) / 4 * 3
		need := additional + diskHeadroom
		if available >= need && used <= limit-need && (available-need)/1024 >= cfg.MinFreeSpaceKB {
			return nil
		}
		if !listed {
			infos, err = List(cfg)
			if err != nil {
				return err
			}
			listed = true
		}
		if len(infos) == 0 {
			return fmt.Errorf("insufficient disk space: backup needs %d MiB plus free-space reserve while keeping disk usage at or below %d%%; no more chain backups can be pruned", need>>20, MaxDiskUsedPercent)
		}
		oldest := infos[len(infos)-1]
		infos = infos[:len(infos)-1]
		slog.Warn("Pruning oldest chain backup to preserve disk space: " + filepath.Base(oldest.Path))
		if err := removeArchive(oldest); err != nil {
			return err
		}
	}
}

// copyContents fills the metadata-only tree made by GNU cp. All data writes
// pass through the same space guard used during online compression. The node
// is stopped, so database files cannot change while they are being copied.
func copyContents(cfg config.Config, src, dst string) error {
	buffer := make([]byte, 1<<20)
	return filepath.WalkDir(src, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("unsupported chain-data entry: %s", path)
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		in, err := os.Open(path)
		if err != nil {
			return err
		}
		defer in.Close()
		out, err := os.OpenFile(target, os.O_WRONLY|os.O_TRUNC, info.Mode().Perm())
		if err != nil {
			return err
		}
		_, copyErr := io.CopyBuffer(spaceWriter{cfg: cfg, dst: out}, struct{ io.Reader }{in}, buffer)
		closeErr := out.Close()
		if copyErr != nil {
			return copyErr
		}
		if closeErr != nil {
			return closeErr
		}
		return os.Chtimes(target, info.ModTime(), info.ModTime())
	})
}

// spaceWriter checks each output chunk against current filesystem usage. This
// also catches node/log growth while the archive is compressed online.
type spaceWriter struct {
	cfg config.Config
	dst io.Writer
}

func (w spaceWriter) Write(p []byte) (int, error) {
	if err := ensureSpace(w.cfg, int64(len(p))); err != nil {
		return 0, err
	}
	return w.dst.Write(p)
}
