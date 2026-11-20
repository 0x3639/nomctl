package scheduleproof

import (
	"encoding/binary"
	"fmt"
	"sync"

	"github.com/syndtr/goleveldb/leveldb"
	"github.com/syndtr/goleveldb/leveldb/opt"
	"github.com/zenon-network/go-zenon/common/db"
	"github.com/zenon-network/go-zenon/common/types"
)

// atomicManager uses a private format, never a znnd database. State, frontier,
// forward patch, and undo patch are one synchronous LevelDB batch per momentum.
// The engine's logical DB encoding is preserved through its snapshot wrapper.
type atomicManager struct {
	mu          sync.Mutex
	ldb         *leveldb.DB
	path        string
	cache       map[types.HashHeight]db.DB
	beforeWrite func() // fault-injection seam; nil outside tests
}

var statePrefix = []byte("state/")

func heightKey(prefix string, h uint64) []byte {
	b := make([]byte, len(prefix)+8)
	copy(b, prefix)
	binary.BigEndian.PutUint64(b[len(prefix):], h)
	return b
}
func openAtomicManager(path string) (*atomicManager, error) {
	ldb, err := leveldb.OpenFile(path, &opt.Options{OpenFilesCacheCapacity: 64})
	if err != nil {
		return nil, err
	}
	return &atomicManager{ldb: ldb, path: path, cache: make(map[types.HashHeight]db.DB)}, nil
}
func (m *atomicManager) snapshot() db.DB {
	s, err := m.ldb.GetSnapshot()
	if err != nil {
		panic(err)
	}
	return db.NewLevelDBSnapshotWrapper(s).Subset(statePrefix)
}
func (m *atomicManager) Frontier() db.DB { m.mu.Lock(); defer m.mu.Unlock(); return m.snapshot() }
func (m *atomicManager) Get(id types.HashHeight) db.DB {
	m.mu.Lock()
	defer m.mu.Unlock()
	if id.IsZero() {
		return db.NewMemDB()
	}
	if hit, ok := m.cache[id]; ok {
		return hit.Snapshot()
	}
	current := m.snapshot()
	frontier := db.GetFrontierIdentifier(current)
	if id == frontier {
		return current
	}
	found, err := db.GetIdentifierByHash(current, id.Hash)
	if err == leveldb.ErrNotFound {
		return nil
	}
	if err != nil {
		panic(err)
	}
	if *found != id {
		return nil
	}
	for h := frontier.Height; h > id.Height; h-- {
		raw, err := m.ldb.Get(heightKey("undo/", h), nil)
		if err != nil {
			panic(err)
		}
		p, err := db.NewPatchFromDump(raw)
		if err != nil {
			panic(err)
		}
		if err = current.Apply(p); err != nil {
			panic(err)
		}
	}
	if db.GetFrontierIdentifier(current) != id {
		panic("historical state identifier mismatch")
	}
	if len(m.cache) >= 64 {
		m.cache = make(map[types.HashHeight]db.DB)
	}
	m.cache[id] = current
	return current.Snapshot()
}
func (m *atomicManager) GetPatch(id types.HashHeight) db.Patch {
	m.mu.Lock()
	defer m.mu.Unlock()
	current := m.snapshot()
	found, err := db.GetIdentifierByHash(current, id.Hash)
	if err == leveldb.ErrNotFound {
		return nil
	}
	if err != nil {
		panic(err)
	}
	if *found != id {
		return nil
	}
	raw, err := m.ldb.Get(heightKey("patch/", id.Height), nil)
	if err != nil {
		panic(err)
	}
	p, err := db.NewPatchFromDump(raw)
	if err != nil {
		panic(err)
	}
	return p
}

type batchState struct{ batch *leveldb.Batch }

func (b batchState) Put(k, v []byte) {
	key := append(append([]byte{}, statePrefix...), k...)
	value := append([]byte{0}, v...)
	b.batch.Put(key, value)
}
func (b batchState) Delete(k []byte) { b.batch.Delete(append(append([]byte{}, statePrefix...), k...)) }

type undoBuilder struct {
	view  db.DB
	patch db.Patch
	err   error
	seen  map[string]bool
}

func (u *undoBuilder) capture(k []byte) {
	if u.err != nil || u.seen[string(k)] {
		return
	}
	u.seen[string(k)] = true
	v, err := u.view.Get(k)
	if err == leveldb.ErrNotFound {
		u.patch.Delete(k)
	} else if err != nil {
		u.err = err
	} else {
		u.patch.Put(k, v)
	}
}
func (u *undoBuilder) Put(k, v []byte) { u.capture(k) }
func (u *undoBuilder) Delete(k []byte) { u.capture(k) }
func (m *atomicManager) write(b *leveldb.Batch) error {
	if m.beforeWrite != nil {
		m.beforeWrite()
	}
	if err := m.ldb.Write(b, &opt.WriteOptions{Sync: true}); err != nil {
		return err
	}
	m.cache = make(map[types.HashHeight]db.DB)
	return nil
}
func (m *atomicManager) Add(tx db.Transaction) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	commits := tx.GetCommits()
	if len(commits) != 1 {
		return fmt.Errorf("expected one momentum commit")
	}
	current := m.snapshot()
	if db.GetFrontierIdentifier(current) != commits[0].Previous() {
		return fmt.Errorf("non-contiguous atomic commit")
	}
	patch := tx.StealChanges()
	metadata := db.NewMemDB()
	payload, err := commits[0].Serialize()
	if err != nil {
		return err
	}
	if err = db.SetFrontier(metadata, commits[0].Identifier(), payload); err != nil {
		return err
	}
	meta, err := metadata.Changes()
	if err != nil {
		return err
	}
	if err = meta.Replay(patch); err != nil {
		return err
	}
	undo := &undoBuilder{view: current, patch: db.NewPatch(), seen: make(map[string]bool)}
	if err = patch.Replay(undo); err != nil {
		return err
	}
	if undo.err != nil {
		return undo.err
	}
	batch := new(leveldb.Batch)
	if err = patch.Replay(batchState{batch}); err != nil {
		return err
	}
	height := commits[0].Identifier().Height
	batch.Put(heightKey("patch/", height), patch.Dump())
	batch.Put(heightKey("undo/", height), undo.patch.Dump())
	return m.write(batch)
}
func (m *atomicManager) Pop() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	id := db.GetFrontierIdentifier(m.snapshot())
	if id.Height <= 1 {
		return fmt.Errorf("cannot remove genesis")
	}
	raw, err := m.ldb.Get(heightKey("undo/", id.Height), nil)
	if err != nil {
		return err
	}
	patch, err := db.NewPatchFromDump(raw)
	if err != nil {
		return err
	}
	batch := new(leveldb.Batch)
	if err = patch.Replay(batchState{batch}); err != nil {
		return err
	}
	batch.Delete(heightKey("undo/", id.Height))
	batch.Delete(heightKey("patch/", id.Height))
	return m.write(batch)
}
func (m *atomicManager) Stop() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.cache = nil
	return m.ldb.Close()
}
func (m *atomicManager) Location() string { return m.path }
