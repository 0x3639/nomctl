package relay

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

// observedContext signals that an acquisition has reached its wait select.
type observedContext struct {
	context.Context
	once    sync.Once
	waiting chan struct{}
}

func (c *observedContext) Done() <-chan struct{} {
	c.once.Do(func() { close(c.waiting) })
	return c.Context.Done()
}

func waitSignal(t *testing.T, ch <-chan struct{}) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(5 * time.Second):
		t.Fatal("operation did not reach the expected synchronization point")
	}
}

func waitError(t *testing.T, ch <-chan error) error {
	t.Helper()
	select {
	case err := <-ch:
		return err
	case <-time.After(5 * time.Second):
		t.Fatal("operation did not complete")
		return nil
	}
}

func assertNoNodeLocks(t *testing.T, locks *nodeLocks) {
	t.Helper()
	locks.mu.Lock()
	defer locks.mu.Unlock()
	if len(locks.entries) != 0 {
		t.Errorf("unused node locks retained: %d", len(locks.entries))
	}
}

func TestNodeLockCancellationAndCleanup(t *testing.T) {
	var locks nodeLocks
	unlock, err := locks.lock(context.Background(), "node-a")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	observed := &observedContext{Context: ctx, waiting: make(chan struct{})}
	done := make(chan error, 1)
	go func() {
		release, err := locks.lock(observed, "node-a")
		if err == nil {
			release()
		}
		done <- err
	}()
	waitSignal(t, observed.waiting)
	cancel()
	if err := waitError(t, done); !errors.Is(err, context.Canceled) {
		t.Errorf("waiting acquisition returned %v", err)
	}
	locks.mu.Lock()
	refs := locks.entries["node-a"].refs
	locks.mu.Unlock()
	if refs != 1 {
		t.Errorf("canceled waiter retained a reference: %d", refs)
	}
	unlock()
	assertNoNodeLocks(t, &locks)
	if release, err := locks.lock(ctx, "unused-node"); !errors.Is(err, context.Canceled) || release != nil {
		t.Fatalf("canceled acquisition = %v", err)
	}
	assertNoNodeLocks(t, &locks)
}

func TestNodeLocksAllowIndependentNodes(t *testing.T) {
	var locks nodeLocks
	first, err := locks.lock(context.Background(), "node-a")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	second, err := locks.lock(ctx, "node-b")
	if err != nil {
		first()
		t.Fatalf("unrelated node blocked: %v", err)
	}
	second()
	first()
	assertNoNodeLocks(t, &locks)
}
