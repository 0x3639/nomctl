package scheduleproof

import (
	"runtime/debug"
	"testing"

	"github.com/zenon-network/go-zenon/chain/nom"
	"github.com/zenon-network/go-zenon/common/db"
	"github.com/zenon-network/go-zenon/common/types"
)

func TestAtomicManagerReleasesLocalSnapshots(t *testing.T) {
	m, err := openAtomicManager(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer m.Stop()
	// Finalizers must not hide leaked ownership from these local operations.
	old := debug.SetGCPercent(-1)
	defer debug.SetGCPercent(old)
	assertReleased := func() {
		t.Helper()
		count, err := m.ldb.GetProperty("leveldb.alivesnaps")
		if err != nil || count != "0" {
			t.Fatalf("local snapshots still alive: %q, %v", count, err)
		}
	}
	var previous types.Hash
	for h := uint64(1); h <= 3; h++ {
		momentum := &nom.Momentum{Height: h, Hash: types.Hash{byte(h)}, PreviousHash: previous}
		patch := db.NewPatch()
		patch.Put([]byte("account"), []byte{byte(h)})
		if err = m.Add(&nom.MomentumTransaction{Momentum: momentum, Changes: patch}); err != nil {
			t.Fatal(err)
		}
		assertReleased()
		got := m.GetPatch(momentum.Identifier())
		assertReleased()
		if got == nil || len(got.Dump()) == 0 {
			t.Fatal("returned patch did not survive snapshot release")
		}
		previous = momentum.Hash
	}
	for i := 0; i < 2; i++ {
		if err = m.Pop(); err != nil {
			t.Fatal(err)
		}
		assertReleased()
	}
	if err = m.Pop(); err == nil {
		t.Fatal("removed genesis")
	}
	assertReleased()
}
