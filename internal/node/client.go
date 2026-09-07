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
	// PillarName, when set, makes Snapshot also look up that pillar.
	PillarName string
}

// New returns a client with a 3 s per-call timeout.
func New(url string) *Client {
	return NewWithTimeout(url, DefaultTimeout)
}

// DefaultTimeout bounds each RPC call.
const DefaultTimeout = 3 * time.Second

// NewWithTimeout is New with a per-call timeout (NOMCTL_RPC_TIMEOUT).
func NewWithTimeout(url string, timeout time.Duration) *Client {
	if timeout <= 0 {
		timeout = DefaultTimeout
	}
	return &Client{URL: url, HTTP: &http.Client{Timeout: timeout}}
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

// PillarStats is the current-epoch production of a pillar.
type PillarStats struct {
	ProducedMomentums uint64 `json:"producedMomentums"`
	ExpectedMomentums uint64 `json:"expectedMomentums"`
}

// PillarInfo is the part of embedded.pillar.getByName nomctl uses. Field
// names mirror znn-sdk-go's embedded.PillarInfo.
type PillarInfo struct {
	Name            string       `json:"name"`
	Rank            int          `json:"rank"`
	OwnerAddress    string       `json:"ownerAddress"`
	ProducerAddress string       `json:"producerAddress"`
	CurrentStats    *PillarStats `json:"currentStats"`
	Weight          string       `json:"weight"`
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

func (c *Client) call(ctx context.Context, method string, out any, params ...any) error {
	if params == nil {
		params = []any{}
	}
	body, err := json.Marshal(rpcRequest{JSONRPC: "2.0", ID: 1, Method: method, Params: params})
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
	if string(r.Result) == "null" {
		return errNullResult
	}
	if err := json.Unmarshal(r.Result, out); err != nil {
		return fmt.Errorf("rpc %s: bad result: %w", method, err)
	}
	return nil
}

var errNullResult = fmt.Errorf("null result")

// PillarByName calls embedded.pillar.getByName. It returns (nil, nil) when
// no pillar has that name.
func (c *Client) PillarByName(ctx context.Context, name string) (*PillarInfo, error) {
	var v PillarInfo
	err := c.call(ctx, "embedded.pillar.getByName", &v, name)
	if err == errNullResult { //nolint:errorlint // sentinel returned directly
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &v, nil
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
	// Pillar is set when Client.PillarName is configured and the pillar
	// exists; PillarErr records a lookup failure without failing Snapshot.
	Pillar    *PillarInfo
	PillarErr error
	// FrontierErr records a failed ledger.getFrontierMomentum. That call
	// waits on the chain lock momentum insertion holds, so it times out on
	// a busy node while the stats calls still answer; it does not set Err.
	FrontierErr error
	// Err is the first failure of the stats calls; the other fields may
	// still be partially set.
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
	wg.Add(1)
	go func() {
		defer wg.Done()
		v, err := c.FrontierMomentum(ctx)
		mu.Lock()
		defer mu.Unlock()
		if err != nil {
			snap.FrontierErr = err
			return
		}
		snap.Frontier = v
	}()
	if c.PillarName != "" {
		wg.Add(1)
		go func() {
			defer wg.Done()
			p, err := c.PillarByName(ctx, c.PillarName)
			mu.Lock()
			snap.Pillar, snap.PillarErr = p, err
			mu.Unlock()
		}()
	}
	wg.Wait()
	return snap
}
