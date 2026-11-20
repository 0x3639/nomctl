package scheduleproof

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/zenon-network/go-zenon/chain/genesis"
	g "github.com/zenon-network/go-zenon/chain/genesis/mock"
	"github.com/zenon-network/go-zenon/chain/nom"
	"github.com/zenon-network/go-zenon/chain/store"
	"golang.org/x/sys/unix"
)

func TestReplicaResumesInterruptedInitialization(t *testing.T) {
	gen := genesis.NewGenesis(g.EmbeddedGenesis)
	raw, err := json.MarshalIndent(replicaIdentity{2, EngineVersion, gen.GetGenesisMomentum().Identifier()}, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	raw = append(raw, '\n')
	for _, size := range []int{-1, 0, len(raw) / 2, len(raw)} {
		t.Run(fmt.Sprintf("write-bytes-%d", size), func(t *testing.T) {
			root := t.TempDir()
			if err := os.WriteFile(filepath.Join(root, "LOCK"), nil, 0600); err != nil {
				t.Fatal(err)
			}
			if size >= 0 {
				if err := os.WriteFile(filepath.Join(root, ".write-interrupted"), raw[:size], 0600); err != nil {
					t.Fatal(err)
				}
			}
			r := openTestReplica(t, root)
			if r.Mirror.Frontier() != gen.GetGenesisMomentum().Identifier() {
				t.Fatal("incorrect bootstrap frontier")
			}
			if _, err := os.Stat(filepath.Join(root, ".write-interrupted")); !os.IsNotExist(err) {
				t.Fatal("interrupted write was not removed")
			}
			r.Close()
			openTestReplica(t, root)
		})
	}
}

func TestReplicaRefusesUnrelatedBootstrapArtifacts(t *testing.T) {
	for _, name := range []string{"config.json", "chain", ".write-unrelated"} {
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			if err := os.WriteFile(filepath.Join(root, name), []byte("unrelated"), 0600); err != nil {
				t.Fatal(err)
			}
			if r, err := OpenReplica(root, genesis.NewGenesis(g.EmbeddedGenesis)); err == nil {
				r.Close()
				t.Fatal("adopted unrelated data")
			}
			if _, err := os.Stat(filepath.Join(root, "replica.json")); !os.IsNotExist(err) {
				t.Fatal("published identity for unrelated data")
			}
		})
	}
}

func TestReplicaRejectsNonregularIdentityWithoutBlocking(t *testing.T) {
	for _, linked := range []bool{false, true} {
		name := "fifo"
		if linked {
			name = "symlink-to-fifo"
		}
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			marker := filepath.Join(root, "replica.json")
			fifo := marker
			if linked {
				fifo = filepath.Join(t.TempDir(), "fifo")
			}
			if err := unix.Mkfifo(fifo, 0600); err != nil {
				t.Fatal(err)
			}
			if linked {
				if err := os.Symlink(fifo, marker); err != nil {
					t.Fatal(err)
				}
			}
			done := make(chan error, 1)
			go func() {
				r, err := OpenReplica(root, genesis.NewGenesis(g.EmbeddedGenesis))
				if r != nil {
					r.Close()
				}
				done <- err
			}()
			select {
			case err := <-done:
				if err == nil {
					t.Fatal("accepted nonregular identity")
				}
			case <-time.After(3 * time.Second):
				// Unblock a regressed reader before test cleanup.
				fd, err := unix.Open(fifo, unix.O_WRONLY|unix.O_NONBLOCK, 0)
				if err == nil {
					unix.Close(fd)
				}
				t.Fatal("identity validation blocked on a FIFO")
			}
		})
	}
}

type differentGenesis struct{ store.Genesis }

func (g differentGenesis) GetGenesisMomentum() *nom.Momentum {
	m := *g.Genesis.GetGenesisMomentum()
	m.Hash[0] ^= 1
	return &m
}

func TestReplicaRechecksIdentityAfterAcquiringLock(t *testing.T) {
	root := t.TempDir()
	gen := genesis.NewGenesis(g.EmbeddedGenesis)
	paused, resume, done := make(chan struct{}), make(chan struct{}), make(chan error, 1)
	release := sync.OnceFunc(func() { close(resume) })
	defer release()
	go func() {
		r, err := openReplica(root, differentGenesis{gen}, func() { close(paused); <-resume })
		if r != nil {
			r.Close()
		}
		done <- err
	}()
	<-paused
	// Finish the winning initialization before the other opener takes the lock.
	r := openTestReplica(t, root)
	r.Close()
	before, err := os.ReadFile(filepath.Join(root, "replica.json"))
	if err != nil {
		t.Fatal(err)
	}
	release()
	if err = <-done; err == nil || !strings.Contains(err.Error(), "mismatch") {
		t.Fatalf("expected identity mismatch: %v", err)
	}
	after, err := os.ReadFile(filepath.Join(root, "replica.json"))
	if err != nil || !bytes.Equal(before, after) {
		t.Fatal("losing opener changed the winning identity")
	}
	openTestReplica(t, root)
}
