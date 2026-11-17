package migrate

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"math"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"unicode"
	"unicode/utf8"

	"github.com/0x3639/nomctl/internal/producer"
	"golang.org/x/sys/unix"
)

type Tree struct {
	Bytes, Entries, LargestFile int64
	Digest                      string
}
type treeEntry struct {
	Path, Kind, Link, Hash, Hardlink string
	Mode                             uint32
	UID, GID                         uint32
	Size, Modified                   int64
}

// Scan includes every directory entry, including hidden files. It never follows
// links while walking. Link resolution must stay inside this real tree.
func Scan(ctx context.Context, root string, hashContents bool) (Tree, error) {
	var result Tree
	st, err := os.Lstat(root)
	if err != nil {
		return result, err
	}
	if !st.IsDir() {
		return result, errors.New("migration data path must be a real directory")
	}
	h := sha256.New()
	enc := json.NewEncoder(h)
	hardlinks := map[string]string{}
	err = filepath.WalkDir(root, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		if !utf8.ValidString(rel) || strings.IndexFunc(rel, unicode.IsControl) >= 0 {
			return errors.New("migration does not support filenames containing control characters or invalid UTF-8")
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		stat, ok := info.Sys().(*syscall.Stat_t)
		if !ok {
			return errors.New("file ownership metadata unavailable")
		}
		v := treeEntry{Path: rel, Mode: uint32(info.Mode() & (os.ModePerm | os.ModeSetuid | os.ModeSetgid | os.ModeSticky)), UID: stat.Uid, GID: stat.Gid, Modified: info.ModTime().UnixNano()}
		if rel == "." {
			v.Mode = 0o700
			v.Modified = 0
		} // promotion always keeps the data root private
		switch {
		case info.IsDir():
			v.Kind = "directory"
		case info.Mode().IsRegular():
			v.Kind = "file"
			v.Size = info.Size()
			if v.Size < 0 || result.Bytes > math.MaxInt64-v.Size-(64<<10) {
				return errors.New("migration size exceeds supported limits")
			}
			result.Bytes += v.Size
			result.LargestFile = max(result.LargestFile, v.Size)
			{
				key := fmt.Sprintf("%d:%d", stat.Dev, stat.Ino)
				first, ok := hardlinks[key]
				if !ok {
					first = rel
					hardlinks[key] = rel
				}
				v.Hardlink = first
			}
			if hashContents {
				v.Hash, err = hashFile(ctx, path)
				if err != nil {
					return err
				}
			}
		case info.Mode()&os.ModeSymlink != 0:
			v.Kind = "link"
			v.Link, err = os.Readlink(path)
			if err != nil {
				return err
			}
			if filepath.IsAbs(v.Link) {
				return errors.New("absolute symlinks must be converted to internal relative links before migration")
			}
			resolved, err := filepath.EvalSymlinks(path)
			if err != nil || !within(root, resolved) {
				return errors.New("external or dangling symlink blocks migration; bring its contents inside the data directory first")
			}
		default:
			return errors.New("migration data contains a socket, device, pipe, or other unsupported entry")
		}
		result.Entries++
		return enc.Encode(v)
	})
	if err == nil {
		result.Digest = hex.EncodeToString(h.Sum(nil))
	}
	return result, err
}

func hashFile(ctx context.Context, path string) (string, error) {
	fd, err := unix.Open(path, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_NONBLOCK|unix.O_CLOEXEC, 0)
	if err != nil {
		return "", err
	}
	f := os.NewFile(uintptr(fd), path)
	defer f.Close()
	before, err := f.Stat()
	if err != nil {
		return "", err
	}
	if !before.Mode().IsRegular() {
		return "", errors.New("expected a regular file")
	}
	h := sha256.New()
	buf := make([]byte, 1<<20)
	for {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		n, err := f.Read(buf)
		if n > 0 {
			_, _ = h.Write(buf[:n])
		}
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return "", err
		}
	}
	after, err := os.Lstat(path)
	if err != nil {
		return "", err
	}
	if !os.SameFile(before, after) || before.Size() != after.Size() || !before.ModTime().Equal(after.ModTime()) {
		return "", errors.New("file changed during migration scan")
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func validateProducer(data string) (string, error) {
	p, err := producer.ReadConfig(producer.ConfigPath(data))
	if err != nil {
		return "", errors.New("cannot read producer configuration")
	}
	if p == nil {
		return "", nil
	}
	key := p.KeyFilePath
	if key == "" {
		key = producer.KeyFileName
	}
	if !filepath.IsAbs(key) {
		key = filepath.Join(data, "wallet", key)
	}
	resolved, err := filepath.EvalSymlinks(key)
	if err != nil || !within(data, resolved) {
		return "", errors.New("producer key must resolve inside the migrated data directory")
	}
	// Absolute producer paths would refer to the old device after a path change.
	if filepath.IsAbs(p.KeyFilePath) {
		return "", errors.New("use a relative Producer.KeyFilePath inside wallet before migrating")
	}
	address, err := producer.VerifyAtIndex(resolved, p.Password, p.Index)
	if err != nil || !strings.EqualFold(address, p.Address) {
		return "", errors.New("producer key cannot be verified against config.json")
	}
	return address, nil
}
