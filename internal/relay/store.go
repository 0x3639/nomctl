// Package relay is the server side of nomctl alerting: it pairs nodes with
// Telegram chats, verifies signed node requests and forwards alerts.
package relay

import (
	"context"
	"errors"
	"sort"
	"sync"
	"time"

	"github.com/0x3639/nomctl/internal/alertproto"
)

// MaxCodesPerChat bounds unused pairing codes per chat.
const MaxCodesPerChat = 5

// Store errors.
var (
	ErrNotFound     = errors.New("not found")
	ErrCodeInvalid  = errors.New("pairing code is invalid or expired")
	ErrTooManyCodes = errors.New("too many unused pairing codes; use one of them or wait for them to expire")
	ErrNameTaken    = errors.New("a node with that name is already paired to this chat")
)

// Node is a paired node.
type Node struct {
	ID       string
	ChatID   int64
	Name     string
	Host     string
	Version  string
	Secret   []byte
	Created  time.Time
	LastSeen time.Time
	Silent   bool
	Blocked  bool
	Summary  alertproto.Summary
}

// Mute suppresses delivery of one alert (or "all") for a node until Until.
type Mute struct {
	NodeID string
	Alert  string
	Until  time.Time
}

// Store persists pairing codes, nodes, mutes and last-sent state.
type Store interface {
	CreateCode(ctx context.Context, chatID int64, code string, expires time.Time) error
	ConsumeCode(ctx context.Context, code string, now time.Time) (int64, error)
	CreateNode(ctx context.Context, n Node) error
	GetNode(ctx context.Context, id string) (Node, error)
	ListNodes(ctx context.Context, chatID int64) ([]Node, error)
	UpdateNode(ctx context.Context, n Node) error
	DeleteNode(ctx context.Context, id string) error
	SilentCandidates(ctx context.Context, before time.Time) ([]Node, error)
	SetMute(ctx context.Context, m Mute) error
	ClearMute(ctx context.Context, nodeID, alert string) error
	Muted(ctx context.Context, nodeID, alert string, now time.Time) (bool, error)
	LastSent(ctx context.Context, nodeID, alert string) (alertproto.State, time.Time, error)
	SetLastSent(ctx context.Context, nodeID, alert string, state alertproto.State, at time.Time) error
	Close() error
}

// --- in-memory implementation ---------------------------------------------

type codeRec struct {
	chatID  int64
	expires time.Time
	used    bool
}

type memoryStore struct {
	mu       sync.Mutex
	codes    map[string]codeRec
	nodes    map[string]Node
	mutes    map[string]time.Time // nodeID + "\x00" + alert
	lastSent map[string]struct {
		state alertproto.State
		at    time.Time
	}
}

// NewMemoryStore returns a Store that lives in memory (tests, dev).
func NewMemoryStore() Store {
	return &memoryStore{
		codes: map[string]codeRec{},
		nodes: map[string]Node{},
		mutes: map[string]time.Time{},
		lastSent: map[string]struct {
			state alertproto.State
			at    time.Time
		}{},
	}
}

func key(nodeID, alert string) string { return nodeID + "\x00" + alert }

func (m *memoryStore) CreateCode(_ context.Context, chatID int64, code string, expires time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	unused := 0
	for _, c := range m.codes {
		if c.chatID == chatID && !c.used && c.expires.After(time.Now()) {
			unused++
		}
	}
	if unused >= MaxCodesPerChat {
		return ErrTooManyCodes
	}
	m.codes[code] = codeRec{chatID: chatID, expires: expires}
	return nil
}

func (m *memoryStore) ConsumeCode(_ context.Context, code string, now time.Time) (int64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	c, ok := m.codes[code]
	if !ok || c.used || !c.expires.After(now) {
		return 0, ErrCodeInvalid
	}
	c.used = true
	m.codes[code] = c
	return c.chatID, nil
}

func (m *memoryStore) CreateNode(_ context.Context, n Node) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, other := range m.nodes {
		if other.ChatID == n.ChatID && other.Name == n.Name {
			return ErrNameTaken
		}
	}
	m.nodes[n.ID] = n
	return nil
}

func (m *memoryStore) GetNode(_ context.Context, id string) (Node, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	n, ok := m.nodes[id]
	if !ok {
		return Node{}, ErrNotFound
	}
	return n, nil
}

func (m *memoryStore) ListNodes(_ context.Context, chatID int64) ([]Node, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []Node
	for _, n := range m.nodes {
		if n.ChatID == chatID {
			out = append(out, n)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

func (m *memoryStore) UpdateNode(_ context.Context, n Node) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.nodes[n.ID]; !ok {
		return ErrNotFound
	}
	m.nodes[n.ID] = n
	return nil
}

func (m *memoryStore) DeleteNode(_ context.Context, id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.nodes[id]; !ok {
		return ErrNotFound
	}
	delete(m.nodes, id)
	for k := range m.mutes {
		if len(k) > len(id) && k[:len(id)] == id && k[len(id)] == 0 {
			delete(m.mutes, k)
		}
	}
	for k := range m.lastSent {
		if len(k) > len(id) && k[:len(id)] == id && k[len(id)] == 0 {
			delete(m.lastSent, k)
		}
	}
	return nil
}

func (m *memoryStore) SilentCandidates(_ context.Context, before time.Time) ([]Node, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []Node
	for _, n := range m.nodes {
		if !n.Silent && n.LastSeen.Before(before) {
			out = append(out, n)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

func (m *memoryStore) SetMute(_ context.Context, mu Mute) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.mutes[key(mu.NodeID, mu.Alert)] = mu.Until
	return nil
}

func (m *memoryStore) ClearMute(_ context.Context, nodeID, alert string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.mutes, key(nodeID, alert))
	return nil
}

func (m *memoryStore) Muted(_ context.Context, nodeID, alert string, now time.Time) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, k := range []string{key(nodeID, alert), key(nodeID, "all")} {
		if until, ok := m.mutes[k]; ok && until.After(now) {
			return true, nil
		}
	}
	return false, nil
}

func (m *memoryStore) LastSent(_ context.Context, nodeID, alert string) (alertproto.State, time.Time, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	ls, ok := m.lastSent[key(nodeID, alert)]
	if !ok {
		return "", time.Time{}, nil
	}
	return ls.state, ls.at, nil
}

func (m *memoryStore) SetLastSent(_ context.Context, nodeID, alert string, state alertproto.State, at time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.lastSent[key(nodeID, alert)] = struct {
		state alertproto.State
		at    time.Time
	}{state, at}
	return nil
}

func (m *memoryStore) Close() error { return nil }
