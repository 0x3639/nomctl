package scheduleproof

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

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
	raw, readErr := os.ReadFile(identityPath)
	if readErr != nil && !os.IsNotExist(readErr) {
		return nil, readErr
	}
	if os.IsNotExist(readErr) {
		entries, e := os.ReadDir(root)
		if e != nil {
			return nil, e
		}
		if len(entries) != 0 {
			return nil, fmt.Errorf("data directory is not an empty standalone replica directory")
		}
	}
	for _, name := range []string{"replica.json", "LOCK", "chain", "status.json"} {
		if err = noSymlinks(filepath.Join(root, name)); err != nil {
			return nil, err
		}
	}
	fd, err := unix.Open(filepath.Join(root, "LOCK"), unix.O_CREAT|unix.O_RDWR|unix.O_NOFOLLOW, 0600)
	if err != nil {
		return nil, err
	}
	lock := os.NewFile(uintptr(fd), "replica lock")
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
	if readErr == nil {
		var got replicaIdentity
		if err = json.Unmarshal(raw, &got); err != nil {
			return nil, err
		}
		if got != expected {
			return nil, fmt.Errorf("replica format, engine, or genesis mismatch")
		}
	} else if err = atomicJSON(identityPath, expected); err != nil {
		return nil, err
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
