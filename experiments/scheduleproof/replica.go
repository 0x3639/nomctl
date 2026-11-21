package scheduleproof

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/zenon-network/go-zenon/chain/store"
	"github.com/zenon-network/go-zenon/common/types"
	"golang.org/x/sys/unix"
)

const EngineVersion = "v0.0.10-alphanet/e8986a63f164259053e0342ceaf9a68cb85d649e"

type replicaIdentity struct {
	Format  int              `json:"format"`
	Engine  string           `json:"engine"`
	Genesis types.HashHeight `json:"genesis"`
}
type Replica struct {
	Mirror  *Mirror
	root    string
	lock    *os.File
	genesis store.Genesis
}

func atomicJSON(path string, value interface{}) error {
	raw, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".write-*")
	if err != nil {
		return err
	}
	name := f.Name()
	defer os.Remove(name)
	if err = f.Chmod(0600); err == nil {
		_, err = f.Write(append(raw, '\n'))
	}
	if err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	if err = os.Rename(name, path); err != nil {
		return err
	}
	d, err := os.Open(filepath.Dir(path))
	if err != nil {
		return err
	}
	defer d.Close()
	return d.Sync()
}
func noSymlinks(path string) error {
	path = filepath.Clean(path)
	for {
		s, err := os.Lstat(path)
		if err != nil && !os.IsNotExist(err) {
			return err
		}
		if err == nil && s.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("symlink data path refused: %s", path)
		}
		parent := filepath.Dir(path)
		if parent == path {
			return nil
		}
		path = parent
	}
}
func OpenReplica(root string, genesis store.Genesis) (r *Replica, err error) {
	return openReplica(root, genesis, nil)
}

// beforeLock is a test seam for competing initializers; production passes nil.
func openReplica(root string, genesis store.Genesis, beforeLock func()) (r *Replica, err error) {
	root, err = filepath.Abs(root)
	if err != nil {
		return nil, err
	}
	if err = noSymlinks(root); err != nil {
		return nil, err
	}
	if err = os.MkdirAll(root, 0700); err != nil {
		return nil, err
	}
	expected := replicaIdentity{2, EngineVersion, genesis.GetGenesisMomentum().Identifier()}
	identityPath := filepath.Join(root, "replica.json")
	// Avoid even creating a lock in an unrelated directory. This preliminary
	// screen never authorizes initialization; repeat validation under the lock.
	entries, err := os.ReadDir(root)
	if err != nil {
		return nil, err
	}
	hasIdentity := false
	for _, entry := range entries {
		hasIdentity = hasIdentity || entry.Name() == "replica.json"
	}
	if !hasIdentity {
		for _, entry := range entries {
			if entry.Name() != "LOCK" && !strings.HasPrefix(entry.Name(), ".write-") {
				return nil, fmt.Errorf("data directory contains unrelated bootstrap data: %s", entry.Name())
			}
		}
	}
	for _, name := range []string{"replica.json", "LOCK", "chain", "status.json"} {
		if err = noSymlinks(filepath.Join(root, name)); err != nil {
			return nil, err
		}
	}
	if beforeLock != nil {
		beforeLock()
	}
	fd, err := unix.Open(filepath.Join(root, "LOCK"), unix.O_CREAT|unix.O_RDWR|unix.O_NOFOLLOW|unix.O_NONBLOCK|unix.O_CLOEXEC, 0600)
	if err != nil {
		return nil, err
	}
	lock := os.NewFile(uintptr(fd), "replica lock")
	if err = privateRegular(fd); err != nil {
		lock.Close()
		return nil, err
	}
	if err = unix.Flock(fd, unix.LOCK_EX|unix.LOCK_NB); err != nil {
		lock.Close()
		return nil, fmt.Errorf("replica already in use: %w", err)
	}
	r = &Replica{root: root, lock: lock, genesis: genesis}
	owned := r
	defer func() {
		if err != nil {
			owned.Close()
		}
	}()
	// Identity observations and the bootstrap decision belong to this lock.
	// Another initializer may have completed while this opener was paused.
	raw, readErr := readPrivateRegular(identityPath, 64<<10)
	if readErr != nil && !os.IsNotExist(readErr) {
		return nil, readErr
	}
	if readErr == nil {
		var got replicaIdentity
		if err = json.Unmarshal(raw, &got); err != nil {
			return nil, err
		}
		if got != expected {
			return nil, fmt.Errorf("replica format, engine, or genesis mismatch")
		}
	} else {
		var leftovers []string
		leftovers, err = bootstrapArtifacts(root, expected)
		if err != nil {
			return nil, err
		}
		if err = atomicJSON(identityPath, expected); err != nil {
			return nil, err
		}
		for _, path := range leftovers {
			if err = os.Remove(path); err != nil {
				return nil, err
			}
		}
	}
	if err = atomicJSON(filepath.Join(root, "status.json"), Status{State: "starting", Engine: EngineVersion}); err != nil {
		return nil, err
	}
	r.Mirror, err = NewMirror(filepath.Join(root, "chain"), genesis)
	if err != nil {
		return nil, err
	}
	return r, nil
}

func privateRegular(fd int) error {
	var st unix.Stat_t
	if err := unix.Fstat(fd, &st); err != nil {
		return err
	}
	if st.Mode&unix.S_IFMT != unix.S_IFREG || st.Mode&0777 != 0600 || st.Nlink != 1 || st.Uid != uint32(os.Geteuid()) {
		return fmt.Errorf("replica metadata must be an owned private regular file")
	}
	return nil
}

func readPrivateRegular(path string, limit int64) ([]byte, error) {
	fd, err := unix.Open(path, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_NONBLOCK|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, &os.PathError{Op: "open", Path: path, Err: err}
	}
	f := os.NewFile(uintptr(fd), path)
	defer f.Close()
	if err = privateRegular(fd); err != nil {
		return nil, err
	}
	raw, err := io.ReadAll(io.LimitReader(f, limit+1))
	if err == nil && int64(len(raw)) > limit {
		err = fmt.Errorf("replica metadata exceeds size limit")
	}
	return raw, err
}

// Before the identity is published, only our empty lock and interrupted writes
// of that exact identity are adoptable. No chain or unrelated data is adopted.
func bootstrapArtifacts(root string, expected replicaIdentity) ([]string, error) {
	encoded, err := json.MarshalIndent(expected, "", "  ")
	if err != nil {
		return nil, err
	}
	encoded = append(encoded, '\n')
	entries, err := os.ReadDir(root)
	if err != nil {
		return nil, err
	}
	var leftovers []string
	for _, entry := range entries {
		name := entry.Name()
		if name != "LOCK" && !strings.HasPrefix(name, ".write-") {
			return nil, fmt.Errorf("data directory contains unrelated bootstrap data: %s", name)
		}
		path := filepath.Join(root, name)
		raw, err := readPrivateRegular(path, int64(len(encoded)))
		if err != nil {
			return nil, err
		}
		if name == "LOCK" {
			if len(raw) != 0 {
				return nil, fmt.Errorf("nonempty replica lock")
			}
		} else {
			if !bytes.HasPrefix(encoded, raw) {
				return nil, fmt.Errorf("unrecognized interrupted identity write: %s", name)
			}
			leftovers = append(leftovers, path)
		}
	}
	return leftovers, nil
}
func (r *Replica) Recover() error {
	if r.Mirror != nil {
		r.Mirror.Close()
		r.Mirror = nil
	}
	m, err := NewMirror(filepath.Join(r.root, "chain"), r.genesis)
	if err != nil {
		return err
	}
	r.Mirror = m
	return nil
}
func (r *Replica) SaveStatus(s Status) error {
	return atomicJSON(filepath.Join(r.root, "status.json"), s)
}
func (r *Replica) Close() {
	if r == nil {
		return
	}
	if r.Mirror != nil {
		r.Mirror.Close()
		r.Mirror = nil
	}
	if r.lock != nil {
		unix.Flock(int(r.lock.Fd()), unix.LOCK_UN)
		r.lock.Close()
		r.lock = nil
	}
}
