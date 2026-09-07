package relay

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"github.com/0x3639/nomctl/internal/alertproto"
)

// snapshotStore pauses after obtaining one snapshot, allowing a competing
// operation to complete before the snapshot's caller resumes.
type snapshotStore struct {
	Store
	kind    string
	paused  atomic.Bool
	ready   chan struct{}
	proceed chan struct{}
}

func (s *snapshotStore) pause(ctx context.Context, kind string) error {
	if kind != s.kind || !s.paused.CompareAndSwap(false, true) {
		return nil
	}
	close(s.ready)
	select {
	case <-s.proceed:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (s *snapshotStore) SilentCandidates(ctx context.Context, before time.Time) ([]Node, error) {
	nodes, err := s.Store.SilentCandidates(ctx, before)
	if err == nil {
		err = s.pause(ctx, "silent")
	}
	return nodes, err
}

func (s *snapshotStore) ListNodes(ctx context.Context, chatID int64) ([]Node, error) {
	nodes, err := s.Store.ListNodes(ctx, chatID)
	if err == nil {
		err = s.pause(ctx, "list")
	}
	return nodes, err
}

func (s *snapshotStore) GetNode(ctx context.Context, id string) (Node, error) {
	node, err := s.Store.GetNode(ctx, id)
	if err == nil {
		err = s.pause(ctx, "get")
	}
	return node, err
}

func runNodeConcurrencyStores(t *testing.T, test func(*testing.T, Store)) {
	t.Helper()
	t.Run("memory", func(t *testing.T) { test(t, NewMemoryStore()) })
	t.Run("sqlite", func(t *testing.T) {
		store, err := OpenSQLite(filepath.Join(t.TempDir(), "relay.db"))
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = store.Close() })
		test(t, store)
	})
}

func concurrencyNode(t *testing.T, store Store, now time.Time) Node {
	t.Helper()
	node := Node{ID: "n_0000000000000001", ChatID: 1, Name: "test-node", Host: "test-host", Version: "test", Secret: []byte("synthetic-test-secret"), Created: now.Add(-time.Hour), LastSeen: now.Add(-10 * time.Minute)}
	if err := store.CreateNode(context.Background(), node); err != nil {
		t.Fatal(err)
	}
	return node
}

func signedHeartbeatRequest(t *testing.T, ctx context.Context, node Node, now time.Time, body alertproto.HeartbeatRequest) *http.Request {
	t.Helper()
	data, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest(http.MethodPost, "/v1/heartbeat", bytes.NewReader(data)).WithContext(ctx)
	r.Header.Set(alertproto.HeaderNode, node.ID)
	r.Header.Set(alertproto.HeaderTimestamp, strconv.FormatInt(now.Unix(), 10))
	r.Header.Set(alertproto.HeaderSignature, alertproto.Sign(node.Secret, now.Unix(), data))
	return r
}

func TestSilentSnapshotDoesNotOverwriteHeartbeat(t *testing.T) {
	runNodeConcurrencyStores(t, func(t *testing.T, base Store) {
		now := time.Date(2026, 9, 6, 12, 0, 0, 0, time.UTC)
		node := concurrencyNode(t, base, now)
		store := &snapshotStore{Store: base, kind: "silent", ready: make(chan struct{}), proceed: make(chan struct{})}
		sender := &memSender{}
		s := NewServer(store, sender, Options{Now: func() time.Time { return now }})
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		done := make(chan error, 1)
		go func() { done <- s.CheckSilent(ctx) }()
		waitSignal(t, store.ready)
		response := httptest.NewRecorder()
		s.Handler().ServeHTTP(response, signedHeartbeatRequest(t, ctx, node, now, alertproto.HeartbeatRequest{Summary: alertproto.Summary{Height: 42, State: "synced"}}))
		if response.Code != http.StatusNoContent {
			t.Fatalf("heartbeat returned %d", response.Code)
		}
		close(store.proceed)
		if err := waitError(t, done); err != nil {
			t.Fatal(err)
		}
		current, err := base.GetNode(ctx, node.ID)
		if err != nil || current.Silent || !current.LastSeen.Equal(now) || current.Summary.Height != 42 {
			t.Errorf("stale silence candidate changed the heartbeat state: %v", err)
		}
		if len(sender.forChat(node.ChatID)) != 0 {
			t.Error("a stale candidate emitted a silence alert")
		}
		state, _, err := base.LastSent(ctx, node.ID, "node_silent")
		if err != nil || state != "" {
			t.Errorf("stale candidate recorded a silence transition: %v", err)
		}
		assertNoNodeLocks(t, &s.nodeOps)
	})
}

func TestHeartbeatKeepsBlockedRecoveryState(t *testing.T) {
	runNodeConcurrencyStores(t, func(t *testing.T, store Store) {
		now := time.Date(2026, 9, 6, 12, 0, 0, 0, time.UTC)
		node := concurrencyNode(t, store, now)
		node.Silent = true
		ctx := context.Background()
		if err := store.UpdateNode(ctx, node); err != nil {
			t.Fatal(err)
		}
		if err := store.SetLastSent(ctx, node.ID, "node_silent", alertproto.Firing, now.Add(-time.Minute)); err != nil {
			t.Fatal(err)
		}
		sender := &flakySender{fail: ErrBlocked}
		s := NewServer(store, sender, Options{Now: func() time.Time { return now }})
		response := httptest.NewRecorder()
		s.Handler().ServeHTTP(response, signedHeartbeatRequest(t, ctx, node, now, alertproto.HeartbeatRequest{Summary: alertproto.Summary{Height: 42}}))
		if response.Code != http.StatusNoContent {
			t.Fatalf("heartbeat returned %d", response.Code)
		}
		current, err := store.GetNode(ctx, node.ID)
		if err != nil || !current.Blocked || !current.Silent || !current.LastSeen.Equal(now) || current.Summary.Height != 42 {
			t.Errorf("heartbeat lost the blocked or retry state: %v", err)
		}
		// The next request must retain the block without another send attempt.
		response = httptest.NewRecorder()
		s.Handler().ServeHTTP(response, signedHeartbeatRequest(t, ctx, node, now, alertproto.HeartbeatRequest{Summary: alertproto.Summary{Height: 43}}))
		if response.Code != http.StatusNoContent || sender.attempts != 1 {
			t.Errorf("blocked recovery retried a send: status=%d attempts=%d", response.Code, sender.attempts)
		}
		assertNoNodeLocks(t, &s.nodeOps)
	})
}

func TestClearBlockedRereadsAfterHeartbeat(t *testing.T) {
	runNodeConcurrencyStores(t, func(t *testing.T, base Store) {
		now := time.Date(2026, 9, 6, 12, 0, 0, 0, time.UTC)
		node := concurrencyNode(t, base, now)
		node.Blocked = true
		if err := base.UpdateNode(context.Background(), node); err != nil {
			t.Fatal(err)
		}
		store := &snapshotStore{Store: base, kind: "list", ready: make(chan struct{}), proceed: make(chan struct{})}
		s := NewServer(store, &memSender{}, Options{Now: func() time.Time { return now }})
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		done := make(chan error, 1)
		go func() { done <- s.clearBlocked(ctx, node.ChatID) }()
		waitSignal(t, store.ready)
		response := httptest.NewRecorder()
		s.Handler().ServeHTTP(response, signedHeartbeatRequest(t, ctx, node, now, alertproto.HeartbeatRequest{Summary: alertproto.Summary{Height: 42}}))
		if response.Code != http.StatusNoContent {
			t.Fatalf("heartbeat returned %d", response.Code)
		}
		close(store.proceed)
		if err := waitError(t, done); err != nil {
			t.Fatal(err)
		}
		current, err := base.GetNode(ctx, node.ID)
		if err != nil || current.Blocked || !current.LastSeen.Equal(now) || current.Summary.Height != 42 {
			t.Errorf("clearing a block lost the heartbeat state: %v", err)
		}
		assertNoNodeLocks(t, &s.nodeOps)
	})
}

func TestSilentSnapshotSkipsDeletedNode(t *testing.T) {
	runNodeConcurrencyStores(t, func(t *testing.T, base Store) {
		now := time.Date(2026, 9, 6, 12, 0, 0, 0, time.UTC)
		node := concurrencyNode(t, base, now)
		store := &snapshotStore{Store: base, kind: "silent", ready: make(chan struct{}), proceed: make(chan struct{})}
		sender := &memSender{}
		s := NewServer(store, sender, Options{Now: func() time.Time { return now }})
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		done := make(chan error, 1)
		go func() { done <- s.CheckSilent(ctx) }()
		waitSignal(t, store.ready)
		if _, err := s.cmdUnpair(ctx, node.ChatID, []string{node.Name}); err != nil {
			t.Fatal(err)
		}
		close(store.proceed)
		if err := waitError(t, done); err != nil {
			t.Fatal(err)
		}
		if _, err := base.GetNode(ctx, node.ID); !errors.Is(err, ErrNotFound) {
			t.Errorf("node deletion was lost: %v", err)
		}
		state, _, err := base.LastSent(ctx, node.ID, "node_silent")
		if err != nil || state != "" || len(sender.forChat(node.ChatID)) != 0 {
			t.Errorf("deleted node generated a transition: %v", err)
		}
		assertNoNodeLocks(t, &s.nodeOps)
	})
}

func TestAuthenticatedSnapshotRejectsDeletedNode(t *testing.T) {
	runNodeConcurrencyStores(t, func(t *testing.T, base Store) {
		now := time.Date(2026, 9, 6, 12, 0, 0, 0, time.UTC)
		node := concurrencyNode(t, base, now)
		store := &snapshotStore{Store: base, kind: "get", ready: make(chan struct{}), proceed: make(chan struct{})}
		s := NewServer(store, &memSender{}, Options{Now: func() time.Time { return now }})
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		request := signedHeartbeatRequest(t, ctx, node, now, alertproto.HeartbeatRequest{Summary: alertproto.Summary{Height: 42}})
		response := httptest.NewRecorder()
		done := make(chan struct{})
		go func() { s.Handler().ServeHTTP(response, request); close(done) }()
		waitSignal(t, store.ready)
		if _, err := s.cmdUnpair(ctx, node.ChatID, []string{node.Name}); err != nil {
			t.Fatal(err)
		}
		close(store.proceed)
		waitSignal(t, done)
		if response.Code != http.StatusUnauthorized {
			t.Errorf("request with a deleted snapshot returned %d", response.Code)
		}
		assertNoNodeLocks(t, &s.nodeOps)
	})
}

type pausedSender struct {
	chatID  int64
	ready   chan struct{}
	proceed chan struct{}
}

func (s *pausedSender) Send(ctx context.Context, chatID int64, _ string) error {
	if chatID != s.chatID {
		return nil
	}
	close(s.ready)
	select {
	case <-s.proceed:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func TestBusyNodeDoesNotBlockAnotherNode(t *testing.T) {
	runNodeConcurrencyStores(t, func(t *testing.T, store Store) {
		now := time.Date(2026, 9, 6, 12, 0, 0, 0, time.UTC)
		first := concurrencyNode(t, store, now)
		second := first
		second.ID, second.ChatID, second.Name = "n_0000000000000002", 2, "second-node"
		if err := store.CreateNode(context.Background(), second); err != nil {
			t.Fatal(err)
		}
		sender := &pausedSender{chatID: first.ChatID, ready: make(chan struct{}), proceed: make(chan struct{})}
		defer close(sender.proceed)
		s := NewServer(store, sender, Options{Now: func() time.Time { return now }})
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		// The silence check holds the first node's lock throughout its send.
		firstDone := make(chan error, 1)
		go func() { firstDone <- s.checkSilentNode(ctx, first.ID, now) }()
		waitSignal(t, sender.ready)
		otherCtx, otherCancel := context.WithTimeout(ctx, time.Second)
		defer otherCancel()
		response := httptest.NewRecorder()
		s.Handler().ServeHTTP(response, signedHeartbeatRequest(t, otherCtx, second, now, alertproto.HeartbeatRequest{Summary: alertproto.Summary{Height: 42}}))
		if response.Code != http.StatusNoContent {
			t.Errorf("another node was held up by delivery: %d", response.Code)
		}
		cancel()
		if err := waitError(t, firstDone); err != nil {
			t.Fatal(err)
		}
		assertNoNodeLocks(t, &s.nodeOps)
	})
}

func TestCanceledHeartbeatLeavesNodeStateUntouched(t *testing.T) {
	store := NewMemoryStore()
	now := time.Date(2026, 9, 6, 12, 0, 0, 0, time.UTC)
	node := concurrencyNode(t, store, now)
	s := NewServer(store, &memSender{}, Options{Now: func() time.Time { return now }})
	unlock, err := s.nodeOps.lock(context.Background(), node.ID)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	observed := &observedContext{Context: ctx, waiting: make(chan struct{})}
	request := signedHeartbeatRequest(t, observed, node, now, alertproto.HeartbeatRequest{Summary: alertproto.Summary{Height: 42}})
	response := httptest.NewRecorder()
	done := make(chan struct{})
	go func() { s.Handler().ServeHTTP(response, request); close(done) }()
	waitSignal(t, observed.waiting)
	cancel()
	waitSignal(t, done)
	unlock()
	if response.Code != http.StatusRequestTimeout {
		t.Errorf("canceled heartbeat returned %d", response.Code)
	}
	current, err := store.GetNode(context.Background(), node.ID)
	if err != nil || !current.LastSeen.Equal(node.LastSeen) || current.Summary.Height != node.Summary.Height {
		t.Errorf("canceled heartbeat changed node state: %v", err)
	}
	assertNoNodeLocks(t, &s.nodeOps)
}

type deadlineSender struct {
	observed chan time.Duration
}

func (s *deadlineSender) Send(ctx context.Context, _ int64, _ string) error {
	remaining := time.Duration(0)
	if deadline, ok := ctx.Deadline(); ok {
		remaining = time.Until(deadline)
	}
	s.observed <- remaining
	<-ctx.Done()
	return ctx.Err()
}

func TestSilentDeliveryHasDeadlineAndCancellationReleasesLock(t *testing.T) {
	runNodeConcurrencyStores(t, func(t *testing.T, store Store) {
		now := time.Date(2026, 9, 6, 12, 0, 0, 0, time.UTC)
		node := concurrencyNode(t, store, now)
		sender := &deadlineSender{observed: make(chan time.Duration, 1)}
		s := NewServer(store, sender, Options{Now: func() time.Time { return now }})
		// Like the process-lifetime ticker context, the parent has no deadline.
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		done := make(chan error, 1)
		go func() { done <- s.CheckSilent(ctx) }()
		select {
		case remaining := <-sender.observed:
			if remaining <= 0 || remaining > 30*time.Second {
				t.Errorf("delivery deadline remaining = %s", remaining)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("delivery did not reach the sender")
		}
		cancel()
		if err := waitError(t, done); err != nil {
			t.Fatal(err)
		}
		current, err := store.GetNode(context.Background(), node.ID)
		if err != nil || current.Silent || current.Blocked || !current.LastSeen.Equal(node.LastSeen) {
			t.Errorf("canceled delivery changed node state: %v", err)
		}
		state, _, err := store.LastSent(context.Background(), node.ID, "node_silent")
		if err != nil || state != "" {
			t.Errorf("canceled send was recorded as delivered: %v", err)
		}
		assertNoNodeLocks(t, &s.nodeOps)
	})
}
