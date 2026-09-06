// Package node is a minimal JSON-RPC 2.0 client for the znnd endpoints that
// nomctl reads: sync state, peers, versions and the frontier momentum.
package node

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sync"
	"time"
)

// DefaultURL is znnd's default HTTP RPC endpoint on the local host.
const DefaultURL = "http://127.0.0.1:35997"

// Client talks to one znnd RPC endpoint.
type Client struct {
	URL  string
	HTTP *http.Client
}

// New returns a client with a 3 s per-call timeout.
func New(url string) *Client {
	return &Client{URL: url, HTTP: &http.Client{Timeout: 3 * time.Second}}
}

// SyncState mirrors protocol.SyncState in go-zenon.
type SyncState int

// Sync states.
const (
	Unknown SyncState = iota
	Syncing
	Done
	NotEnoughPeers
)

func (s SyncState) String() string {
	switch s {
	case Syncing:
		return "syncing"
	case Done:
		return "synced"
	case NotEnoughPeers:
		return "not enough peers"
	default:
		return "unknown"
	}
}

// SyncInfo is stats.syncInfo.
type SyncInfo struct {
	State         SyncState `json:"state"`
	CurrentHeight uint64    `json:"currentHeight"`
	TargetHeight  uint64    `json:"targetHeight"`
}

// Peer is one entry of stats.networkInfo.
type Peer struct {
	PublicKey string `json:"publicKey"`
	IP        string `json:"ip"`
	Name      string `json:"name"`
}

// NetworkInfo is stats.networkInfo.
type NetworkInfo struct {
	NumPeers int    `json:"numPeers"`
	Peers    []Peer `json:"peers"`
	Self     *Peer  `json:"self"`
}

// ProcessInfo is stats.processInfo.
type ProcessInfo struct {
	Version string `json:"version"`
	Commit  string `json:"commit"`
}

// OsInfo is the part of stats.osInfo nomctl uses.
type OsInfo struct {
	NumGoroutine int `json:"numGoroutine"`
	NumCPU       int `json:"numCPU"`
}

// Momentum is the part of ledger.getFrontierMomentum nomctl uses.
type Momentum struct {
	Height    uint64 `json:"height"`
	Timestamp uint64 `json:"timestamp"`
	Hash      string `json:"hash"`
}

// Time converts the momentum timestamp.
func (m *Momentum) Time() time.Time { return time.Unix(int64(m.Timestamp), 0) } //nolint:gosec // unix seconds fit

type rpcRequest struct {
	JSONRPC string `json:"jsonrpc"`
	ID      int    `json:"id"`
	Method  string `json:"method"`
	Params  []any  `json:"params"`
}

type rpcResponse struct {
	Result json.RawMessage `json:"result"`
	Error  *struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

func (c *Client) call(ctx context.Context, method string, out any) error {
	body, err := json.Marshal(rpcRequest{JSONRPC: "2.0", ID: 1, Method: method, Params: []any{}})
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.URL, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return fmt.Errorf("rpc %s: %w", method, err)
	}
	defer func() { _ = resp.Body.Close() }()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return fmt.Errorf("rpc %s: %w", method, err)
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("rpc %s: HTTP %d", method, resp.StatusCode)
	}
	var r rpcResponse
	if err := json.Unmarshal(data, &r); err != nil {
		return fmt.Errorf("rpc %s: bad response: %w", method, err)
	}
	if r.Error != nil {
		return fmt.Errorf("rpc %s: %s (%d)", method, r.Error.Message, r.Error.Code)
	}
	if err := json.Unmarshal(r.Result, out); err != nil {
		return fmt.Errorf("rpc %s: bad result: %w", method, err)
	}
	return nil
}

// SyncInfo calls stats.syncInfo.
func (c *Client) SyncInfo(ctx context.Context) (*SyncInfo, error) {
	var v SyncInfo
	return &v, c.call(ctx, "stats.syncInfo", &v)
}

// NetworkInfo calls stats.networkInfo.
func (c *Client) NetworkInfo(ctx context.Context) (*NetworkInfo, error) {
	var v NetworkInfo
	return &v, c.call(ctx, "stats.networkInfo", &v)
}

// ProcessInfo calls stats.processInfo.
func (c *Client) ProcessInfo(ctx context.Context) (*ProcessInfo, error) {
	var v ProcessInfo
	return &v, c.call(ctx, "stats.processInfo", &v)
}

// OsInfo calls stats.osInfo.
func (c *Client) OsInfo(ctx context.Context) (*OsInfo, error) {
	var v OsInfo
	return &v, c.call(ctx, "stats.osInfo", &v)
}

// FrontierMomentum calls ledger.getFrontierMomentum.
func (c *Client) FrontierMomentum(ctx context.Context) (*Momentum, error) {
	var v Momentum
	return &v, c.call(ctx, "ledger.getFrontierMomentum", &v)
}

// Snapshot is every call nomctl needs, taken together.
type Snapshot struct {
	Sync     *SyncInfo
	Network  *NetworkInfo
	Process  *ProcessInfo
	Os       *OsInfo
	Frontier *Momentum
	// Err is the first failure; the other fields may still be partially set.
	Err error
}

// Snapshot performs all calls in parallel.
func (c *Client) Snapshot(ctx context.Context) Snapshot {
	var snap Snapshot
	var mu sync.Mutex
	var wg sync.WaitGroup
	run := func(fn func() error) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			err := fn()
			mu.Lock()
			defer mu.Unlock()
			if err != nil && snap.Err == nil {
				snap.Err = err
			}
		}()
	}
	run(func() error {
		v, err := c.SyncInfo(ctx)
		if err == nil {
			mu.Lock()
			snap.Sync = v
			mu.Unlock()
		}
		return err
	})
	run(func() error {
		v, err := c.NetworkInfo(ctx)
		if err == nil {
			mu.Lock()
			snap.Network = v
			mu.Unlock()
		}
		return err
	})
	run(func() error {
		v, err := c.ProcessInfo(ctx)
		if err == nil {
			mu.Lock()
			snap.Process = v
			mu.Unlock()
		}
		return err
	})
	run(func() error {
		v, err := c.OsInfo(ctx)
		if err == nil {
			mu.Lock()
			snap.Os = v
			mu.Unlock()
		}
		return err
	})
	run(func() error {
		v, err := c.FrontierMomentum(ctx)
		if err == nil {
			mu.Lock()
			snap.Frontier = v
			mu.Unlock()
		}
		return err
	})
	wg.Wait()
	return snap
}
