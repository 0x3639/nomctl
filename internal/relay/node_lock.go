package relay

import (
	"context"
	"sync"
)

// nodeLocks serializes each node's read/modify/write operations within one
// Server. Waiting for one node never holds up operations on another node.
// References include waiters so an entry cannot disappear while it is in use.
type nodeLocks struct {
	mu      sync.Mutex
	entries map[string]*nodeLock
}

type nodeLock struct {
	held chan struct{}
	refs int
}

func (l *nodeLocks) lock(ctx context.Context, id string) (func(), error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	l.mu.Lock()
	if l.entries == nil {
		l.entries = make(map[string]*nodeLock)
	}
	entry := l.entries[id]
	if entry == nil {
		entry = &nodeLock{held: make(chan struct{}, 1)}
		l.entries[id] = entry
	}
	entry.refs++
	l.mu.Unlock()

	select {
	case entry.held <- struct{}{}:
		unlock := func() {
			<-entry.held
			l.release(id, entry)
		}
		// Cancellation can race with an available lock. Do not start the
		// operation if its context was canceled during acquisition.
		if err := ctx.Err(); err != nil {
			unlock()
			return nil, err
		}
		return unlock, nil
	case <-ctx.Done():
		l.release(id, entry)
		return nil, ctx.Err()
	}
}

func (l *nodeLocks) release(id string, entry *nodeLock) {
	l.mu.Lock()
	defer l.mu.Unlock()
	entry.refs--
	if entry.refs == 0 {
		delete(l.entries, id)
	}
}

// lockNode rereads the current node after acquiring its lock. A snapshot
// obtained during authentication or listing may already be stale or deleted.
func (s *Server) lockNode(ctx context.Context, id string) (Node, func(), error) {
	unlock, err := s.nodeOps.lock(ctx, id)
	if err != nil {
		return Node{}, nil, err
	}
	node, err := s.store.GetNode(ctx, id)
	if err != nil {
		unlock()
		return Node{}, nil, err
	}
	return node, unlock, nil
}
