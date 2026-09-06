package relay

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/0x3639/nomctl/internal/alertproto"
)

func TestMemoryStore(t *testing.T) {
	runStoreSuite(t, func(*testing.T) Store { return NewMemoryStore() })
}

func TestSQLiteStore(t *testing.T) {
	runStoreSuite(t, func(t *testing.T) Store {
		s, err := OpenSQLite(filepath.Join(t.TempDir(), "relay.db"))
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = s.Close() })
		return s
	})
}

func runStoreSuite(t *testing.T, open func(t *testing.T) Store) {
	t.Helper()
	now := time.Date(2026, 9, 6, 12, 0, 0, 0, time.UTC)
	ctx := context.Background()

	t.Run("codes", func(t *testing.T) {
		s := open(t)
		exp := time.Now().Add(10 * time.Minute)
		if err := s.CreateCode(ctx, 1, "AAAAAAAA", exp); err != nil {
			t.Fatal(err)
		}
		if _, err := s.ConsumeCode(ctx, "AAAAAAAA", exp.Add(time.Minute)); !errors.Is(err, ErrCodeInvalid) {
			t.Errorf("expired code: %v", err)
		}
		chat, err := s.ConsumeCode(ctx, "AAAAAAAA", time.Now())
		if err != nil || chat != 1 {
			t.Fatalf("consume: %d %v", chat, err)
		}
		if _, err := s.ConsumeCode(ctx, "AAAAAAAA", time.Now()); !errors.Is(err, ErrCodeInvalid) {
			t.Errorf("reuse: %v", err)
		}
		if _, err := s.ConsumeCode(ctx, "NOPE", time.Now()); !errors.Is(err, ErrCodeInvalid) {
			t.Errorf("unknown: %v", err)
		}
		for i := range MaxCodesPerChat {
			if err := s.CreateCode(ctx, 2, string(rune('A'+i))+"BCDEFGH", exp); err != nil {
				t.Fatal(err)
			}
		}
		if err := s.CreateCode(ctx, 2, "ZZZZZZZZ", exp); !errors.Is(err, ErrTooManyCodes) {
			t.Errorf("limit: %v", err)
		}
		if err := s.CreateCode(ctx, 3, "YYYYYYYY", exp); err != nil {
			t.Errorf("other chat is not limited: %v", err)
		}
	})

	t.Run("nodes", func(t *testing.T) {
		s := open(t)
		n := Node{ID: "n1", ChatID: 1, Name: "pillar-1", Host: "h1", Version: "v", Secret: []byte("sec"), Created: now, LastSeen: now, Summary: alertproto.Summary{State: "synced", Height: 5}}
		if err := s.CreateNode(ctx, n); err != nil {
			t.Fatal(err)
		}
		if err := s.CreateNode(ctx, Node{ID: "n2", ChatID: 1, Name: "pillar-1", Secret: []byte("x"), Created: now, LastSeen: now}); !errors.Is(err, ErrNameTaken) {
			t.Errorf("duplicate name: %v", err)
		}
		if err := s.CreateNode(ctx, Node{ID: "n3", ChatID: 2, Name: "pillar-1", Created: now, LastSeen: now, Secret: []byte("x")}); err != nil {
			t.Errorf("same name in another chat is fine: %v", err)
		}
		got, err := s.GetNode(ctx, "n1")
		if err != nil || got.Name != "pillar-1" || string(got.Secret) != "sec" || got.Summary.Height != 5 || !got.LastSeen.Equal(now) {
			t.Errorf("get: %+v %v", got, err)
		}
		if _, err := s.GetNode(ctx, "missing"); !errors.Is(err, ErrNotFound) {
			t.Errorf("missing: %v", err)
		}
		got.LastSeen = now.Add(time.Minute)
		got.Silent = true
		got.Summary.Height = 6
		if err := s.UpdateNode(ctx, got); err != nil {
			t.Fatal(err)
		}
		got, _ = s.GetNode(ctx, "n1")
		if !got.Silent || got.Summary.Height != 6 || !got.LastSeen.Equal(now.Add(time.Minute)) {
			t.Errorf("update: %+v", got)
		}
		if err := s.UpdateNode(ctx, Node{ID: "missing"}); !errors.Is(err, ErrNotFound) {
			t.Errorf("update missing: %v", err)
		}
		list, err := s.ListNodes(ctx, 1)
		if err != nil || len(list) != 1 || list[0].ID != "n1" {
			t.Errorf("list: %+v %v", list, err)
		}
		if err := s.DeleteNode(ctx, "n1"); err != nil {
			t.Fatal(err)
		}
		if err := s.DeleteNode(ctx, "n1"); !errors.Is(err, ErrNotFound) {
			t.Errorf("delete twice: %v", err)
		}
		if list, _ := s.ListNodes(ctx, 1); len(list) != 0 {
			t.Errorf("list after delete: %+v", list)
		}
	})

	t.Run("silent", func(t *testing.T) {
		s := open(t)
		_ = s.CreateNode(ctx, Node{ID: "old", ChatID: 1, Name: "old", Secret: []byte("x"), Created: now, LastSeen: now.Add(-10 * time.Minute)})
		_ = s.CreateNode(ctx, Node{ID: "fresh", ChatID: 1, Name: "fresh", Secret: []byte("x"), Created: now, LastSeen: now})
		_ = s.CreateNode(ctx, Node{ID: "flagged", ChatID: 1, Name: "flagged", Secret: []byte("x"), Created: now, LastSeen: now.Add(-10 * time.Minute), Silent: true})
		c, err := s.SilentCandidates(ctx, now.Add(-5*time.Minute))
		if err != nil || len(c) != 1 || c[0].ID != "old" {
			t.Errorf("candidates: %+v %v", c, err)
		}
	})

	t.Run("mutes and last sent", func(t *testing.T) {
		s := open(t)
		_ = s.CreateNode(ctx, Node{ID: "n1", ChatID: 1, Name: "a", Secret: []byte("x"), Created: now, LastSeen: now})
		if m, _ := s.Muted(ctx, "n1", "disk_low", now); m {
			t.Error("not muted by default")
		}
		if err := s.SetMute(ctx, Mute{NodeID: "n1", Alert: "disk_low", Until: now.Add(time.Hour)}); err != nil {
			t.Fatal(err)
		}
		if m, _ := s.Muted(ctx, "n1", "disk_low", now); !m {
			t.Error("muted")
		}
		if m, _ := s.Muted(ctx, "n1", "disk_low", now.Add(2*time.Hour)); m {
			t.Error("mute expired")
		}
		if m, _ := s.Muted(ctx, "n1", "service_down", now); m {
			t.Error("other alert not muted")
		}
		if err := s.SetMute(ctx, Mute{NodeID: "n1", Alert: "all", Until: now.Add(time.Hour)}); err != nil {
			t.Fatal(err)
		}
		if m, _ := s.Muted(ctx, "n1", "service_down", now); !m {
			t.Error("all mutes everything")
		}
		_ = s.ClearMute(ctx, "n1", "all")
		_ = s.ClearMute(ctx, "n1", "disk_low")
		if m, _ := s.Muted(ctx, "n1", "disk_low", now); m {
			t.Error("cleared")
		}

		if st, at, err := s.LastSent(ctx, "n1", "disk_low"); err != nil || st != "" || !at.IsZero() {
			t.Errorf("never sent: %v %v %v", st, at, err)
		}
		if err := s.SetLastSent(ctx, "n1", "disk_low", alertproto.Firing, now); err != nil {
			t.Fatal(err)
		}
		if err := s.SetLastSent(ctx, "n1", "disk_low", alertproto.OK, now.Add(time.Minute)); err != nil {
			t.Fatal(err)
		}
		st, at, err := s.LastSent(ctx, "n1", "disk_low")
		if err != nil || st != alertproto.OK || !at.Equal(now.Add(time.Minute)) {
			t.Errorf("last sent: %v %v %v", st, at, err)
		}
		_ = s.DeleteNode(ctx, "n1")
		if st, _, _ := s.LastSent(ctx, "n1", "disk_low"); st != "" {
			t.Error("delete node clears last sent")
		}
	})
}
