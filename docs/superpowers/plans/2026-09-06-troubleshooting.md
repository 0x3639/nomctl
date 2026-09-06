# Troubleshooting (live view + support bundle) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add `nomctl status`, `nomctl top` and `nomctl support-bundle`, all fed by one metrics sampler that reads /proc, cgroups, systemd and the node's local JSON-RPC.

**Architecture:** `internal/node` is a tiny JSON-RPC client. `internal/metrics` turns /proc, cgroup v2, `systemctl show` and the node client into a `Sample` struct with derived sync rate/ETA. `cmd/status` prints a Sample, `internal/tui/top` renders Samples in a bubbletea loop, and `internal/support` writes Samples plus journals, /proc dumps and log tails into a numbered directory and tarball.

**Tech Stack:** Go 1.24, cobra, bubbletea v1 + bubbles (progress, sparkline is hand-rolled), lipgloss, net/http, encoding/json. No new module dependencies.

**Spec:** `docs/superpowers/specs/2026-09-06-troubleshooting-design.md`

## Global Constraints

- Module path `github.com/0x3639/nomctl`; run every go command with `GOWORK=off` (a broken parent go.work exists on the dev machine; the Makefile sets it).
- `gofmt`, `go vet` and `golangci-lint run ./...` must pass after every task (`make lint`).
- Pure Go, `CGO_ENABLED=0`; `make cross` must still produce linux/amd64 and linux/arm64 binaries.
- Readers of /proc, cgroup and pressure files take a root path parameter so tests use fixture directories; nothing in `internal/metrics` may hard-code `/proc` except the default.
- Diagnostic commands (`status`, `top`, `support-bundle`) require root but never run pre-flight checks and never take the node-data lock.
- No file under the data directory is read except `<data dir>/log/*`; `config.json` and `wallet/` are never opened.
- Node RPC endpoint is `http://127.0.0.1:35997`, 3 s timeout per call.
- Commit after each task with the trailer used in this repo:
  `Co-Authored-By: Claude Fable 5.1 <noreply@anthropic.com>` and
  `Claude-Session: https://claude.ai/code/session_017Q2FWixuWXk7YGpgfMmuFX`.

---

## File structure

| Path | Responsibility |
|---|---|
| `internal/node/client.go` | JSON-RPC client: `Client`, `SyncInfo`, `NetworkInfo`, `ProcessInfo`, `OsInfo`, `FrontierMomentum`, `Snapshot` |
| `internal/node/client_test.go` | httptest-backed tests |
| `internal/metrics/proc.go` | `/proc/<pid>` readers and `/proc` host readers, all rooted |
| `internal/metrics/cgroup.go` | cgroup v2 file readers |
| `internal/metrics/systemd.go` | `systemctl show` parser |
| `internal/metrics/sample.go` | `Sample`, `Sampler`, derived values, `Format` (text) |
| `internal/metrics/testdata/proc/...` | fixture files |
| `internal/metrics/*_test.go` | unit tests |
| `cmd/status.go` | `nomctl status [--json]` |
| `cmd/top.go` | `nomctl top [--interval]` |
| `internal/tui/top.go` | bubbletea dashboard model |
| `internal/tui/top_test.go` | golden `View()` test |
| `internal/support/redact.go` | redaction regexes, crash-marker regex, peer IP redaction |
| `internal/support/logs.go` | app log inventory and tails |
| `internal/support/collect.go` | `Collect`, file writers, archive |
| `internal/support/watch.go` | `--watch` loop |
| `internal/support/*_test.go` | unit tests |
| `cmd/support.go` | `nomctl support-bundle` |
| `internal/tui/menu.go` | two new menu entries |
| `README.md` | Troubleshooting section, command table, differences |

---

### Task 1: Node JSON-RPC client

**Files:**
- Create: `internal/node/client.go`
- Test: `internal/node/client_test.go`

**Interfaces:**
- Produces:
  ```go
  const DefaultURL = "http://127.0.0.1:35997"
  type Client struct { URL string; HTTP *http.Client }
  func New(url string) *Client                       // 3 s timeout
  type SyncState int                                  // Unknown=0 Syncing=1 Done=2 NotEnoughPeers=3
  func (s SyncState) String() string                  // "unknown","syncing","synced","not enough peers"
  type SyncInfo struct { State SyncState `json:"state"`; CurrentHeight, TargetHeight uint64 }
  type Peer struct { PublicKey, IP, Name string }
  type NetworkInfo struct { NumPeers int; Peers []Peer; Self *Peer }
  type ProcessInfo struct { Version, Commit string }
  type OsInfo struct { NumGoroutine int `json:"numGoroutine"`; NumCPU int `json:"numCPU"` }
  type Momentum struct { Height uint64; Timestamp uint64; Hash string }
  func (c *Client) SyncInfo(ctx) (*SyncInfo, error)  // and the other four
  type Snapshot struct { Sync *SyncInfo; Network *NetworkInfo; Process *ProcessInfo; Os *OsInfo; Frontier *Momentum; Err error }
  func (c *Client) Snapshot(ctx) Snapshot            // all five in parallel; Err is the first error
  ```

- [ ] **Step 1: Write the failing test**

`internal/node/client_test.go`:
```go
package node

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func rpcServer(t *testing.T) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			ID     any    `json:"id"`
			Method string `json:"method"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
		var result string
		switch req.Method {
		case "stats.syncInfo":
			result = `{"state":1,"currentHeight":100,"targetHeight":200}`
		case "stats.networkInfo":
			result = `{"numPeers":2,"peers":[{"publicKey":"a","ip":"1.2.3.4","name":"x"},{"publicKey":"b","ip":"5.6.7.8","name":"y"}],"self":{"publicKey":"s","ip":"127.0.0.1","name":"*self*"}}`
		case "stats.processInfo":
			result = `{"version":"v0.0.7","commit":"abc"}`
		case "stats.osInfo":
			result = `{"numGoroutine":42,"numCPU":4}`
		case "ledger.getFrontierMomentum":
			result = `{"height":100,"timestamp":1700000000,"hash":"deadbeef"}`
		case "boom":
			_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"error":{"code":-32601,"message":"method not found"}}`))
			return
		default:
			http.Error(w, "unknown", 404)
			return
		}
		_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":` + result + `}`))
	}))
}

func TestCalls(t *testing.T) {
	srv := rpcServer(t)
	defer srv.Close()
	c := New(srv.URL)
	ctx := context.Background()

	s, err := c.SyncInfo(ctx)
	if err != nil || s.State != Syncing || s.CurrentHeight != 100 || s.TargetHeight != 200 {
		t.Fatalf("SyncInfo = %+v, %v", s, err)
	}
	if s.State.String() != "syncing" || SyncState(2).String() != "synced" || SyncState(3).String() != "not enough peers" || SyncState(9).String() != "unknown" {
		t.Error("SyncState strings wrong")
	}
	n, err := c.NetworkInfo(ctx)
	if err != nil || n.NumPeers != 2 || len(n.Peers) != 2 || n.Peers[0].IP != "1.2.3.4" {
		t.Fatalf("NetworkInfo = %+v, %v", n, err)
	}
	p, err := c.ProcessInfo(ctx)
	if err != nil || p.Version != "v0.0.7" {
		t.Fatalf("ProcessInfo = %+v, %v", p, err)
	}
	o, err := c.OsInfo(ctx)
	if err != nil || o.NumGoroutine != 42 {
		t.Fatalf("OsInfo = %+v, %v", o, err)
	}
	m, err := c.FrontierMomentum(ctx)
	if err != nil || m.Height != 100 || m.Timestamp != 1700000000 {
		t.Fatalf("Frontier = %+v, %v", m, err)
	}
}

func TestRPCError(t *testing.T) {
	srv := rpcServer(t)
	defer srv.Close()
	c := New(srv.URL)
	var out map[string]any
	err := c.call(context.Background(), "boom", &out)
	if err == nil || err.Error() != "rpc boom: method not found (-32601)" {
		t.Errorf("err = %v", err)
	}
}

func TestSnapshot(t *testing.T) {
	srv := rpcServer(t)
	defer srv.Close()
	snap := New(srv.URL).Snapshot(context.Background())
	if snap.Err != nil || snap.Sync == nil || snap.Network == nil || snap.Process == nil || snap.Os == nil || snap.Frontier == nil {
		t.Errorf("snapshot incomplete: %+v", snap)
	}
	down := New("http://127.0.0.1:1").Snapshot(context.Background())
	if down.Err == nil {
		t.Error("unreachable node must set Err")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `GOWORK=off go test ./internal/node/`
Expected: FAIL (package does not exist / undefined New).

- [ ] **Step 3: Implement the client**

`internal/node/client.go`:
```go
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
func (m *Momentum) Time() time.Time { return time.Unix(int64(m.Timestamp), 0) }

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
	fail := func(err error) {
		mu.Lock()
		if snap.Err == nil {
			snap.Err = err
		}
		mu.Unlock()
	}
	run := func(fn func() error) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := fn(); err != nil {
				fail(err)
			}
		}()
	}
	run(func() error { v, err := c.SyncInfo(ctx); if err == nil { snap.Sync = v }; return err })
	run(func() error { v, err := c.NetworkInfo(ctx); if err == nil { snap.Network = v }; return err })
	run(func() error { v, err := c.ProcessInfo(ctx); if err == nil { snap.Process = v }; return err })
	run(func() error { v, err := c.OsInfo(ctx); if err == nil { snap.Os = v }; return err })
	run(func() error { v, err := c.FrontierMomentum(ctx); if err == nil { snap.Frontier = v }; return err })
	wg.Wait()
	return snap
}
```
(Expand the one-line closures onto separate lines for gofmt/gocritic.)

- [ ] **Step 4: Run tests and lint**

Run: `GOWORK=off go test ./internal/node/ && GOWORK=off golangci-lint run ./internal/node/`
Expected: PASS, 0 issues.

- [ ] **Step 5: Commit**

```bash
git add internal/node && git commit -m "Add znnd JSON-RPC client"
```

---

### Task 2: /proc, cgroup and systemd readers

**Files:**
- Create: `internal/metrics/proc.go`, `internal/metrics/cgroup.go`, `internal/metrics/systemd.go`
- Create fixtures under `internal/metrics/testdata/proc/` (see step 1)
- Test: `internal/metrics/readers_test.go`

**Interfaces:**
- Produces:
  ```go
  type ProcStatus struct { Name string; State string; Threads int; FDSize int; VmRSS, VmSwap, VmPeak uint64 } // bytes
  func ReadProcStatus(root string, pid int) (ProcStatus, error)
  type ProcStat struct { UTime, STime uint64 }                 // clock ticks
  func ReadProcStat(root string, pid int) (ProcStat, error)
  type ProcIO struct { ReadBytes, WriteBytes uint64 }
  func ReadProcIO(root string, pid int) (ProcIO, error)
  func ReadFDLimit(root string, pid int) (soft uint64, err error)   // from limits "Max open files"
  func CountFDs(root string, pid int) (int, error)
  func ReadLoadAvg(root string) (l1, l5, l15 float64, err error)
  type MemInfo struct { Total, Available uint64 }              // bytes
  func ReadMemInfo(root string) (MemInfo, error)
  type Pressure struct { CPU, IO, Memory float64 }             // "some avg10"
  func ReadPressure(root string) Pressure                      // missing files -> 0
  type CgroupStats struct { MemoryCurrent, MemoryPeak, MemoryMax uint64; PidsCurrent int; Present bool }
  func ReadCgroup(cgroupRoot, controlGroup string) CgroupStats
  type ServiceProps struct { ActiveState, SubState, Result, ControlGroup string; MainPID, NRestarts int; ExecMainStart time.Time }
  func ParseServiceProps(show string) ServiceProps            // key=value lines
  func ReadServiceProps(unit string) (ServiceProps, error)    // runs systemctl show
  ```

- [ ] **Step 1: Create fixtures**

`internal/metrics/testdata/proc/1234/status`:
```
Name:	znnd
State:	S (sleeping)
Pid:	1234
Threads:	38
FDSize:	512
VmPeak:	 3200000 kB
VmRSS:	 1900000 kB
VmSwap:	       0 kB
```
`internal/metrics/testdata/proc/1234/stat` (fields 14/15 are utime/stime = 1000/500):
```
1234 (znnd) S 1 1234 1234 0 -1 4194560 100 0 0 0 1000 500 0 0 20 0 38 0 12345 3276800000 475000 18446744073709551615 1 1 0 0 0 0 0 0 0 0 0 0 17 1 0 0 0 0 0
```
`internal/metrics/testdata/proc/1234/io`:
```
rchar: 1
wchar: 2
syscr: 3
syscw: 4
read_bytes: 1048576
write_bytes: 2097152
cancelled_write_bytes: 0
```
`internal/metrics/testdata/proc/1234/limits`:
```
Limit                     Soft Limit           Hard Limit           Units
Max open files            32768                32768                files
Max processes             unlimited            unlimited            processes
```
`internal/metrics/testdata/proc/1234/fd/` containing three empty files named `0`, `1`, `2`.
`internal/metrics/testdata/proc/loadavg`: `1.20 0.90 0.80 2/500 9999`
`internal/metrics/testdata/proc/meminfo`:
```
MemTotal:        8000000 kB
MemFree:         1000000 kB
MemAvailable:    3100000 kB
```
`internal/metrics/testdata/proc/pressure/cpu`: `some avg10=2.10 avg60=1.00 avg300=0.50 total=1`
`internal/metrics/testdata/proc/pressure/io`: `some avg10=15.40 avg60=1.00 avg300=0.50 total=1` followed by a `full ...` line.
`internal/metrics/testdata/proc/pressure/memory`: `some avg10=0.00 avg60=0.00 avg300=0.00 total=0`
`internal/metrics/testdata/cgroup/system.slice/go-zenon.service/memory.current`: `1990000000`
`.../memory.peak`: `2100000000`; `.../memory.max`: `max`; `.../pids.current`: `38`.

- [ ] **Step 2: Write the failing tests**

`internal/metrics/readers_test.go`:
```go
package metrics

import (
	"testing"
	"time"
)

const root = "testdata/proc"

func TestProcReaders(t *testing.T) {
	st, err := ReadProcStatus(root, 1234)
	if err != nil || st.Name != "znnd" || st.Threads != 38 || st.VmRSS != 1900000*1024 || st.VmPeak != 3200000*1024 {
		t.Errorf("status = %+v, %v", st, err)
	}
	s, err := ReadProcStat(root, 1234)
	if err != nil || s.UTime != 1000 || s.STime != 500 {
		t.Errorf("stat = %+v, %v", s, err)
	}
	io, err := ReadProcIO(root, 1234)
	if err != nil || io.ReadBytes != 1048576 || io.WriteBytes != 2097152 {
		t.Errorf("io = %+v, %v", io, err)
	}
	lim, err := ReadFDLimit(root, 1234)
	if err != nil || lim != 32768 {
		t.Errorf("fd limit = %d, %v", lim, err)
	}
	n, err := CountFDs(root, 1234)
	if err != nil || n != 3 {
		t.Errorf("fds = %d, %v", n, err)
	}
	if _, err := ReadProcStatus(root, 1); err == nil {
		t.Error("missing pid must error")
	}
}

func TestHostReaders(t *testing.T) {
	l1, l5, l15, err := ReadLoadAvg(root)
	if err != nil || l1 != 1.2 || l5 != 0.9 || l15 != 0.8 {
		t.Errorf("loadavg = %v %v %v %v", l1, l5, l15, err)
	}
	m, err := ReadMemInfo(root)
	if err != nil || m.Total != 8000000*1024 || m.Available != 3100000*1024 {
		t.Errorf("meminfo = %+v, %v", m, err)
	}
	p := ReadPressure(root)
	if p.CPU != 2.1 || p.IO != 15.4 || p.Memory != 0 {
		t.Errorf("pressure = %+v", p)
	}
	if p := ReadPressure("testdata/nowhere"); p != (Pressure{}) {
		t.Errorf("missing pressure files must be zero, got %+v", p)
	}
}

func TestCgroup(t *testing.T) {
	c := ReadCgroup("testdata/cgroup", "/system.slice/go-zenon.service")
	if !c.Present || c.MemoryCurrent != 1990000000 || c.MemoryPeak != 2100000000 || c.MemoryMax != 0 || c.PidsCurrent != 38 {
		t.Errorf("cgroup = %+v", c)
	}
	if ReadCgroup("testdata/cgroup", "").Present {
		t.Error("empty control group must not be present")
	}
}

func TestParseServiceProps(t *testing.T) {
	p := ParseServiceProps("ActiveState=active\nSubState=running\nMainPID=1234\nNRestarts=2\nResult=success\nControlGroup=/system.slice/go-zenon.service\nExecMainStartTimestamp=Sat 2026-09-06 10:00:00 UTC\n")
	if p.ActiveState != "active" || p.MainPID != 1234 || p.NRestarts != 2 || p.ControlGroup != "/system.slice/go-zenon.service" {
		t.Errorf("props = %+v", p)
	}
	want := time.Date(2026, 9, 6, 10, 0, 0, 0, time.UTC)
	if !p.ExecMainStart.Equal(want) {
		t.Errorf("start = %v, want %v", p.ExecMainStart, want)
	}
	if ParseServiceProps("ExecMainStartTimestamp=\n").ExecMainStart.IsZero() == false {
		t.Error("empty timestamp must be zero")
	}
}
```

- [ ] **Step 3: Run tests to verify they fail**

Run: `GOWORK=off go test ./internal/metrics/`
Expected: FAIL, undefined functions.

- [ ] **Step 4: Implement the readers**

`internal/metrics/proc.go`:
```go
// Package metrics samples the node process, its host and its RPC into one
// Sample used by nomctl status, nomctl top and the support bundle.
package metrics

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// DefaultProcRoot is where /proc is mounted.
const DefaultProcRoot = "/proc"

// ProcStatus is the subset of /proc/<pid>/status nomctl uses; sizes in bytes.
type ProcStatus struct {
	Name    string
	State   string
	Threads int
	FDSize  int
	VmRSS   uint64
	VmSwap  uint64
	VmPeak  uint64
}

func pidPath(root string, pid int, file string) string {
	return filepath.Join(root, strconv.Itoa(pid), file)
}

// ReadProcStatus parses /proc/<pid>/status.
func ReadProcStatus(root string, pid int) (ProcStatus, error) {
	var st ProcStatus
	f, err := os.Open(pidPath(root, pid, "status"))
	if err != nil {
		return st, err
	}
	defer func() { _ = f.Close() }()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		key, val, ok := strings.Cut(sc.Text(), ":")
		if !ok {
			continue
		}
		val = strings.TrimSpace(val)
		switch key {
		case "Name":
			st.Name = val
		case "State":
			st.State = val
		case "Threads":
			st.Threads, _ = strconv.Atoi(val)
		case "FDSize":
			st.FDSize, _ = strconv.Atoi(val)
		case "VmRSS":
			st.VmRSS = kbField(val)
		case "VmSwap":
			st.VmSwap = kbField(val)
		case "VmPeak":
			st.VmPeak = kbField(val)
		}
	}
	return st, sc.Err()
}

// kbField parses "1234 kB" into bytes.
func kbField(s string) uint64 {
	n, _ := strconv.ParseUint(strings.TrimSuffix(strings.TrimSpace(s), " kB"), 10, 64)
	return n * 1024
}

// ProcStat is the CPU part of /proc/<pid>/stat, in clock ticks.
type ProcStat struct {
	UTime uint64
	STime uint64
}

// ReadProcStat parses fields 14 and 15 of /proc/<pid>/stat.
func ReadProcStat(root string, pid int) (ProcStat, error) {
	data, err := os.ReadFile(pidPath(root, pid, "stat"))
	if err != nil {
		return ProcStat{}, err
	}
	// The command name is in parentheses and may contain spaces; skip past it.
	s := string(data)
	i := strings.LastIndex(s, ")")
	if i < 0 {
		return ProcStat{}, errors.New("malformed stat")
	}
	fields := strings.Fields(s[i+1:])
	// fields[0] is state (field 3); utime is field 14 -> index 11.
	if len(fields) < 13 {
		return ProcStat{}, errors.New("malformed stat")
	}
	u, _ := strconv.ParseUint(fields[11], 10, 64)
	st, _ := strconv.ParseUint(fields[12], 10, 64)
	return ProcStat{UTime: u, STime: st}, nil
}

// ProcIO is the storage part of /proc/<pid>/io.
type ProcIO struct {
	ReadBytes  uint64
	WriteBytes uint64
}

// ReadProcIO parses /proc/<pid>/io.
func ReadProcIO(root string, pid int) (ProcIO, error) {
	var io ProcIO
	f, err := os.Open(pidPath(root, pid, "io"))
	if err != nil {
		return io, err
	}
	defer func() { _ = f.Close() }()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		key, val, _ := strings.Cut(sc.Text(), ":")
		n, _ := strconv.ParseUint(strings.TrimSpace(val), 10, 64)
		switch key {
		case "read_bytes":
			io.ReadBytes = n
		case "write_bytes":
			io.WriteBytes = n
		}
	}
	return io, sc.Err()
}

// ReadFDLimit returns the soft "Max open files" limit of the process.
func ReadFDLimit(root string, pid int) (uint64, error) {
	f, err := os.Open(pidPath(root, pid, "limits"))
	if err != nil {
		return 0, err
	}
	defer func() { _ = f.Close() }()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := sc.Text()
		if !strings.HasPrefix(line, "Max open files") {
			continue
		}
		fields := strings.Fields(strings.TrimPrefix(line, "Max open files"))
		if len(fields) < 1 {
			break
		}
		if fields[0] == "unlimited" {
			return 0, nil
		}
		return strconv.ParseUint(fields[0], 10, 64)
	}
	return 0, fmt.Errorf("Max open files not found for pid %d", pid)
}

// CountFDs counts entries in /proc/<pid>/fd.
func CountFDs(root string, pid int) (int, error) {
	entries, err := os.ReadDir(pidPath(root, pid, "fd"))
	if err != nil {
		return 0, err
	}
	return len(entries), nil
}

// ReadLoadAvg parses /proc/loadavg.
func ReadLoadAvg(root string) (l1, l5, l15 float64, err error) {
	data, err := os.ReadFile(filepath.Join(root, "loadavg"))
	if err != nil {
		return 0, 0, 0, err
	}
	fields := strings.Fields(string(data))
	if len(fields) < 3 {
		return 0, 0, 0, errors.New("malformed loadavg")
	}
	l1, _ = strconv.ParseFloat(fields[0], 64)
	l5, _ = strconv.ParseFloat(fields[1], 64)
	l15, _ = strconv.ParseFloat(fields[2], 64)
	return l1, l5, l15, nil
}

// MemInfo is the subset of /proc/meminfo nomctl uses, in bytes.
type MemInfo struct {
	Total     uint64
	Available uint64
}

// ReadMemInfo parses /proc/meminfo.
func ReadMemInfo(root string) (MemInfo, error) {
	var m MemInfo
	f, err := os.Open(filepath.Join(root, "meminfo"))
	if err != nil {
		return m, err
	}
	defer func() { _ = f.Close() }()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		key, val, _ := strings.Cut(sc.Text(), ":")
		switch key {
		case "MemTotal":
			m.Total = kbField(val)
		case "MemAvailable":
			m.Available = kbField(val)
		}
	}
	if m.Total == 0 {
		return m, errors.New("MemTotal not found")
	}
	return m, sc.Err()
}

// Pressure holds the "some avg10" values from /proc/pressure, in percent.
type Pressure struct {
	CPU    float64
	IO     float64
	Memory float64
}

// ReadPressure reads /proc/pressure/{cpu,io,memory}; missing files give 0.
func ReadPressure(root string) Pressure {
	return Pressure{
		CPU:    pressureSomeAvg10(filepath.Join(root, "pressure", "cpu")),
		IO:     pressureSomeAvg10(filepath.Join(root, "pressure", "io")),
		Memory: pressureSomeAvg10(filepath.Join(root, "pressure", "memory")),
	}
}

func pressureSomeAvg10(path string) float64 {
	data, err := os.ReadFile(path)
	if err != nil {
		return 0
	}
	for _, line := range strings.Split(string(data), "\n") {
		if !strings.HasPrefix(line, "some ") {
			continue
		}
		for _, kv := range strings.Fields(line)[1:] {
			if v, ok := strings.CutPrefix(kv, "avg10="); ok {
				f, _ := strconv.ParseFloat(v, 64)
				return f
			}
		}
	}
	return 0
}
```

`internal/metrics/cgroup.go`:
```go
package metrics

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// DefaultCgroupRoot is the cgroup v2 mount point.
const DefaultCgroupRoot = "/sys/fs/cgroup"

// CgroupStats is what nomctl reads from the service's cgroup. MemoryMax is 0
// when unlimited.
type CgroupStats struct {
	Present       bool
	MemoryCurrent uint64
	MemoryPeak    uint64
	MemoryMax     uint64
	PidsCurrent   int
}

// ReadCgroup reads the cgroup files under cgroupRoot+controlGroup.
func ReadCgroup(cgroupRoot, controlGroup string) CgroupStats {
	var c CgroupStats
	if controlGroup == "" {
		return c
	}
	dir := filepath.Join(cgroupRoot, controlGroup)
	if st, err := os.Stat(dir); err != nil || !st.IsDir() {
		return c
	}
	c.Present = true
	c.MemoryCurrent = cgroupUint(dir, "memory.current")
	c.MemoryPeak = cgroupUint(dir, "memory.peak")
	c.MemoryMax = cgroupUint(dir, "memory.max") // "max" parses as 0
	c.PidsCurrent = int(cgroupUint(dir, "pids.current"))
	return c
}

func cgroupUint(dir, file string) uint64 {
	data, err := os.ReadFile(filepath.Join(dir, file))
	if err != nil {
		return 0
	}
	n, _ := strconv.ParseUint(strings.TrimSpace(string(data)), 10, 64)
	return n
}
```

`internal/metrics/systemd.go`:
```go
package metrics

import (
	"strconv"
	"strings"
	"time"

	"github.com/0x3639/nomctl/internal/execx"
)

// ServiceProps is the subset of `systemctl show` nomctl uses.
type ServiceProps struct {
	ActiveState   string
	SubState      string
	Result        string
	ControlGroup  string
	MainPID       int
	NRestarts     int
	ExecMainStart time.Time
}

// serviceProperties is the property list requested from systemctl.
var serviceProperties = []string{"ActiveState", "SubState", "Result", "ControlGroup", "MainPID", "NRestarts", "ExecMainStartTimestamp"}

// ReadServiceProps runs systemctl show for the unit.
func ReadServiceProps(unit string) (ServiceProps, error) {
	args := []string{"show", unit}
	for _, p := range serviceProperties {
		args = append(args, "-p", p)
	}
	out, err := execx.Output("systemctl", args...)
	if err != nil {
		return ServiceProps{}, err
	}
	return ParseServiceProps(out), nil
}

// ParseServiceProps parses key=value lines as printed by systemctl show.
func ParseServiceProps(show string) ServiceProps {
	var p ServiceProps
	for _, line := range strings.Split(show, "\n") {
		key, val, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		switch key {
		case "ActiveState":
			p.ActiveState = val
		case "SubState":
			p.SubState = val
		case "Result":
			p.Result = val
		case "ControlGroup":
			p.ControlGroup = val
		case "MainPID":
			p.MainPID, _ = strconv.Atoi(val)
		case "NRestarts":
			p.NRestarts, _ = strconv.Atoi(val)
		case "ExecMainStartTimestamp":
			p.ExecMainStart = parseSystemdTime(val)
		}
	}
	return p
}

// parseSystemdTime parses "Sat 2026-09-06 10:00:00 UTC" style timestamps.
func parseSystemdTime(s string) time.Time {
	s = strings.TrimSpace(s)
	if s == "" {
		return time.Time{}
	}
	for _, layout := range []string{"Mon 2006-01-02 15:04:05 MST", "2006-01-02 15:04:05 MST"} {
		if t, err := time.Parse(layout, s); err == nil {
			return t
		}
	}
	return time.Time{}
}
```

- [ ] **Step 5: Run tests and lint**

Run: `GOWORK=off go test ./internal/metrics/ && GOWORK=off golangci-lint run ./internal/metrics/`
Expected: PASS, 0 issues. Note: `git` does not track empty directories; the `fd/` fixture files must be non-empty or named files with a byte in them (write `x`).

- [ ] **Step 6: Commit**

```bash
git add internal/metrics && git commit -m "Add /proc, cgroup and systemd readers for metrics"
```

---

### Task 3: Sample, Sampler, derived values and text format

**Files:**
- Create: `internal/metrics/sample.go`
- Test: `internal/metrics/sample_test.go`

**Interfaces:**
- Consumes: Task 1 `node.Client`, `node.Snapshot`; Task 2 readers.
- Produces:
  ```go
  type Sample struct {
      Taken   time.Time
      Service ServiceSample   // Unit, Found, ActiveState, SubState, Result, MainPID, NRestarts, Since time.Time, Error string
      Process ProcessSample   // Present, CPUPercent float64, RSS, VmSwap, VmPeak uint64, Threads, OpenFDs int, FDLimit uint64, ReadBytes, WriteBytes uint64, Cgroup CgroupStats
      Host    HostSample      // Load1, Load5, Load15, MemTotal, MemAvailable, DataDir string, DataDirFree, DataDirTotal uint64, Pressure
      Node    NodeSample      // Reachable bool, Error string, URL string, State node.SyncState, StateText string, CurrentHeight, TargetHeight uint64, NumPeers int, Version, Commit string, Goroutines int, FrontierHeight uint64, FrontierTime time.Time, MomentumsPerSec float64, ETA time.Duration, ETAKnown bool, FrontierAge time.Duration, Stalled bool
  }
  type Sampler struct { ... }
  func NewSampler(cfg config.Config) *Sampler        // uses DefaultProcRoot, DefaultCgroupRoot, node.DefaultURL
  func (s *Sampler) Take(ctx context.Context) Sample  // never returns an error
  func Format(s Sample) string                        // the nomctl status text block
  const StalledAfter = 2 * time.Minute
  func rate(points []heightPoint) float64            // unexported, tested
  ```
  Sampler fields for tests: `ProcRoot, CgroupRoot string; Node *node.Client; Now func() time.Time; ClockTicks float64 (default 100); readProps func(unit string) (ServiceProps, error); diskFree func(path string) (free, total uint64, err error)`.

- [ ] **Step 1: Write the failing tests**

`internal/metrics/sample_test.go`:
```go
package metrics

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/0x3639/nomctl/internal/config"
	"github.com/0x3639/nomctl/internal/node"
)

func fakeNode(t *testing.T, height uint64) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct{ Method string }
		_ = json.NewDecoder(r.Body).Decode(&req)
		var res string
		switch req.Method {
		case "stats.syncInfo":
			res = `{"state":1,"currentHeight":` + itoa(height) + `,"targetHeight":2000}`
		case "stats.networkInfo":
			res = `{"numPeers":14,"peers":[],"self":null}`
		case "stats.processInfo":
			res = `{"version":"v0.0.7","commit":"a1b2c3d"}`
		case "stats.osInfo":
			res = `{"numGoroutine":42,"numCPU":4}`
		case "ledger.getFrontierMomentum":
			res = `{"height":` + itoa(height) + `,"timestamp":1700000000,"hash":"h"}`
		}
		_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":` + res + `}`))
	}))
}

func itoa(n uint64) string { return strings.TrimSpace(strings.Repeat(" ", 0) + json.Number(fmtUint(n)).String()) }

func testSampler(t *testing.T, url string) *Sampler {
	t.Helper()
	cfg := config.Default()
	cfg.ZnnDir = "testdata"
	s := NewSampler(cfg)
	s.ProcRoot = "testdata/proc"
	s.CgroupRoot = "testdata/cgroup"
	s.Node = node.New(url)
	s.readProps = func(string) (ServiceProps, error) {
		return ServiceProps{ActiveState: "active", SubState: "running", MainPID: 1234, NRestarts: 1,
			ControlGroup: "/system.slice/go-zenon.service", ExecMainStart: time.Unix(1700000000, 0)}, nil
	}
	s.diskFree = func(string) (uint64, uint64, error) { return 210 << 30, 500 << 30, nil }
	return s
}

func TestTakeAndCPU(t *testing.T) {
	srv := fakeNode(t, 1000)
	defer srv.Close()
	s := testSampler(t, srv.URL)
	now := time.Unix(1700000100, 0)
	s.Now = func() time.Time { return now }

	first := s.Take(context.Background())
	if !first.Service.Found || first.Service.MainPID != 1234 || !first.Process.Present {
		t.Fatalf("service/process not sampled: %+v", first)
	}
	if first.Process.CPUPercent != 0 {
		t.Error("first sample has no CPU baseline, must be 0")
	}
	if first.Process.RSS != 1900000*1024 || first.Process.OpenFDs != 3 || first.Process.FDLimit != 32768 || first.Process.Cgroup.PidsCurrent != 38 {
		t.Errorf("process = %+v", first.Process)
	}
	if first.Host.Load1 != 1.2 || first.Host.DataDirFree != 210<<30 || first.Host.Pressure.IO != 15.4 {
		t.Errorf("host = %+v", first.Host)
	}
	if !first.Node.Reachable || first.Node.State != node.Syncing || first.Node.CurrentHeight != 1000 || first.Node.NumPeers != 14 || first.Node.Version != "v0.0.7" {
		t.Errorf("node = %+v", first.Node)
	}
	if first.Node.FrontierAge != 100*time.Second || first.Node.Stalled {
		t.Errorf("frontier age = %v stalled=%v", first.Node.FrontierAge, first.Node.Stalled)
	}

	// Second sample 10 s later with the same ticks: CPU stays 0 but is computed.
	now = now.Add(10 * time.Second)
	second := s.Take(context.Background())
	if second.Process.CPUPercent != 0 {
		t.Errorf("no tick change means 0%%, got %v", second.Process.CPUPercent)
	}
}

func TestRateAndETA(t *testing.T) {
	base := time.Unix(1700000000, 0)
	pts := []heightPoint{{base, 1000}, {base.Add(10 * time.Second), 1050}, {base.Add(20 * time.Second), 1100}}
	if r := rate(pts); r < 4.99 || r > 5.01 {
		t.Errorf("rate = %v, want 5", r)
	}
	if rate(pts[:1]) != 0 {
		t.Error("one point has no rate")
	}
	srv := fakeNode(t, 1000)
	defer srv.Close()
	s := testSampler(t, srv.URL)
	now := base
	s.Now = func() time.Time { return now }
	s.Take(context.Background())
	srv.Close()
	srv2 := fakeNode(t, 1100)
	defer srv2.Close()
	s.Node = node.New(srv2.URL)
	now = now.Add(20 * time.Second)
	smp := s.Take(context.Background())
	if smp.Node.MomentumsPerSec < 4.99 || smp.Node.MomentumsPerSec > 5.01 {
		t.Errorf("mom/s = %v", smp.Node.MomentumsPerSec)
	}
	if !smp.Node.ETAKnown || smp.Node.ETA != 180*time.Second {
		t.Errorf("ETA = %v known=%v (900 left at 5/s)", smp.Node.ETA, smp.Node.ETAKnown)
	}
}

func TestStalledAndUnreachable(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct{ Method string }
		_ = json.NewDecoder(r.Body).Decode(&req)
		res := `{}`
		switch req.Method {
		case "stats.syncInfo":
			res = `{"state":2,"currentHeight":2000,"targetHeight":2000}`
		case "ledger.getFrontierMomentum":
			res = `{"height":2000,"timestamp":1700000000,"hash":"h"}`
		case "stats.networkInfo":
			res = `{"numPeers":1,"peers":[]}`
		}
		_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":` + res + `}`))
	}))
	defer srv.Close()
	s := testSampler(t, srv.URL)
	s.Now = func() time.Time { return time.Unix(1700000000+200, 0) }
	smp := s.Take(context.Background())
	if !smp.Node.Stalled || smp.Node.StateText != "synced" || smp.Node.ETAKnown {
		t.Errorf("expected stalled synced node: %+v", smp.Node)
	}

	s.Node = node.New("http://127.0.0.1:1")
	down := s.Take(context.Background())
	if down.Node.Reachable || down.Node.Error == "" {
		t.Errorf("unreachable node not reported: %+v", down.Node)
	}
	text := Format(down)
	if !strings.Contains(text, "node rpc unreachable") || !strings.Contains(text, "go-zenon active (running)") {
		t.Errorf("Format:\n%s", text)
	}
}

func TestFormatHealthy(t *testing.T) {
	srv := fakeNode(t, 1000)
	defer srv.Close()
	s := testSampler(t, srv.URL)
	s.Now = func() time.Time { return time.Unix(1700000100, 0) }
	text := Format(s.Take(context.Background()))
	for _, want := range []string{"Service   go-zenon active (running), pid 1234, 1 restarts", "syncing 1,000 / 2,000 (50.0%)", "Peers     14 connected", "open files 3 / 32768", "Pressure  cpu 2.1%, io 15.4%, mem 0.0%"} {
		if !strings.Contains(text, want) {
			t.Errorf("missing %q in:\n%s", want, text)
		}
	}
}
```
Replace the `itoa` helper with `strconv.FormatUint(n, 10)` (import strconv) — written inline above only to keep the snippet short; use strconv in the real file.

- [ ] **Step 2: Run tests to verify they fail**

Run: `GOWORK=off go test ./internal/metrics/`
Expected: FAIL, undefined Sample/Sampler.

- [ ] **Step 3: Implement sample.go**

```go
package metrics

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/0x3639/nomctl/internal/config"
	"github.com/0x3639/nomctl/internal/fsx"
	"github.com/0x3639/nomctl/internal/node"
)

// StalledAfter is how old the frontier momentum may be on a node that
// reports itself synced before it is flagged as stalled.
const StalledAfter = 2 * time.Minute

// rateWindow is how many (time, height) points feed the sync rate.
const rateWindow = 30

// ServiceSample is the systemd view of the node.
type ServiceSample struct {
	Unit        string
	Found       bool
	ActiveState string
	SubState    string
	Result      string
	MainPID     int
	NRestarts   int
	Since       time.Time
	Error       string `json:",omitempty"`
}

// ProcessSample is the /proc and cgroup view of the node process.
type ProcessSample struct {
	Present    bool
	CPUPercent float64
	RSS        uint64
	VmSwap     uint64
	VmPeak     uint64
	Threads    int
	OpenFDs    int
	FDLimit    uint64
	ReadBytes  uint64
	WriteBytes uint64
	Cgroup     CgroupStats
}

// HostSample is the machine-wide view.
type HostSample struct {
	Load1, Load5, Load15 float64
	MemTotal             uint64
	MemAvailable         uint64
	DataDir              string
	DataDirFree          uint64
	DataDirTotal         uint64
	Pressure             Pressure
}

// NodeSample is the RPC view plus derived values.
type NodeSample struct {
	URL             string
	Reachable       bool
	Error           string `json:",omitempty"`
	State           node.SyncState
	StateText       string
	CurrentHeight   uint64
	TargetHeight    uint64
	NumPeers        int
	Version         string
	Commit          string
	Goroutines      int
	FrontierHeight  uint64
	FrontierTime    time.Time
	FrontierAge     time.Duration
	MomentumsPerSec float64
	ETA             time.Duration
	ETAKnown        bool
	Stalled         bool
}

// Sample is one observation of everything.
type Sample struct {
	Taken   time.Time
	Service ServiceSample
	Process ProcessSample
	Host    HostSample
	Node    NodeSample
}

type heightPoint struct {
	t time.Time
	h uint64
}

// Sampler takes Samples and keeps the little state needed for rates.
type Sampler struct {
	ProcRoot   string
	CgroupRoot string
	Node       *node.Client
	Now        func() time.Time
	ClockTicks float64

	unit      string
	dataDir   string
	readProps func(unit string) (ServiceProps, error)
	diskFree  func(path string) (free, total uint64, err error)

	prevTicks uint64
	prevTime  time.Time
	prevPID   int
	heights   []heightPoint
}

// NewSampler configures a Sampler for the node described by cfg.
func NewSampler(cfg config.Config) *Sampler {
	return &Sampler{
		ProcRoot:   DefaultProcRoot,
		CgroupRoot: DefaultCgroupRoot,
		Node:       node.New(node.DefaultURL),
		Now:        time.Now,
		ClockTicks: 100,
		unit:       cfg.ServiceName,
		dataDir:    cfg.ZnnDir,
		readProps:  ReadServiceProps,
		diskFree:   diskFree,
	}
}

func diskFree(path string) (uint64, uint64, error) {
	availKB, usedPct, err := fsx.DiskFree(path)
	if err != nil {
		return 0, 0, err
	}
	free := uint64(availKB) * 1024
	if usedPct >= 100 {
		return free, free, nil
	}
	total := uint64(float64(free) / (1 - float64(usedPct)/100))
	return free, total, nil
}

// Take samples everything now. Failures are recorded inside the Sample.
func (s *Sampler) Take(ctx context.Context) Sample {
	now := s.Now()
	smp := Sample{Taken: now}
	smp.Service = s.takeService()
	if smp.Service.MainPID > 0 {
		smp.Process = s.takeProcess(smp.Service.MainPID, smp.Service.ControlGroupForCgroup(), now)
	} else {
		s.prevPID = 0
	}
	smp.Host = s.takeHost()
	smp.Node = s.takeNode(ctx, now)
	return smp
}
```
Continue in the same file:
```go
// ControlGroupForCgroup is the cgroup path used for cgroup files.
func (s ServiceSample) ControlGroupForCgroup() string { return s.controlGroup }
```
Add `controlGroup string` (unexported, `json:"-"`) to ServiceSample and set it in takeService. Then:
```go
func (s *Sampler) takeService() ServiceSample {
	out := ServiceSample{Unit: s.unit}
	props, err := s.readProps(s.unit)
	if err != nil {
		out.Error = err.Error()
		return out
	}
	out.Found = props.ActiveState != "" && props.ActiveState != "inactive" || props.MainPID > 0 || props.ControlGroup != ""
	if props.ActiveState == "" {
		out.Error = "unit not found"
	}
	out.ActiveState = props.ActiveState
	out.SubState = props.SubState
	out.Result = props.Result
	out.MainPID = props.MainPID
	out.NRestarts = props.NRestarts
	out.Since = props.ExecMainStart
	out.controlGroup = props.ControlGroup
	return out
}

func (s *Sampler) takeProcess(pid int, controlGroup string, now time.Time) ProcessSample {
	var p ProcessSample
	st, err := ReadProcStatus(s.ProcRoot, pid)
	if err != nil {
		return p
	}
	p.Present = true
	p.RSS, p.VmSwap, p.VmPeak, p.Threads = st.VmRSS, st.VmSwap, st.VmPeak, st.Threads
	if stat, err := ReadProcStat(s.ProcRoot, pid); err == nil {
		ticks := stat.UTime + stat.STime
		if s.prevPID == pid && !s.prevTime.IsZero() && now.After(s.prevTime) && ticks >= s.prevTicks {
			secs := now.Sub(s.prevTime).Seconds()
			p.CPUPercent = float64(ticks-s.prevTicks) / s.ClockTicks / secs * 100
		}
		s.prevTicks, s.prevTime, s.prevPID = ticks, now, pid
	}
	if io, err := ReadProcIO(s.ProcRoot, pid); err == nil {
		p.ReadBytes, p.WriteBytes = io.ReadBytes, io.WriteBytes
	}
	p.FDLimit, _ = ReadFDLimit(s.ProcRoot, pid)
	p.OpenFDs, _ = CountFDs(s.ProcRoot, pid)
	p.Cgroup = ReadCgroup(s.CgroupRoot, controlGroup)
	return p
}

func (s *Sampler) takeHost() HostSample {
	var h HostSample
	h.Load1, h.Load5, h.Load15, _ = ReadLoadAvg(s.ProcRoot)
	if m, err := ReadMemInfo(s.ProcRoot); err == nil {
		h.MemTotal, h.MemAvailable = m.Total, m.Available
	}
	h.DataDir = s.dataDir
	h.DataDirFree, h.DataDirTotal, _ = s.diskFree(s.dataDir)
	h.Pressure = ReadPressure(s.ProcRoot)
	return h
}

func (s *Sampler) takeNode(ctx context.Context, now time.Time) NodeSample {
	n := NodeSample{URL: s.Node.URL}
	snap := s.Node.Snapshot(ctx)
	if snap.Err != nil {
		n.Error = snap.Err.Error()
		s.heights = nil
		return n
	}
	n.Reachable = true
	n.State = snap.Sync.State
	n.StateText = snap.Sync.State.String()
	n.CurrentHeight, n.TargetHeight = snap.Sync.CurrentHeight, snap.Sync.TargetHeight
	n.NumPeers = snap.Network.NumPeers
	n.Version, n.Commit = snap.Process.Version, snap.Process.Commit
	n.Goroutines = snap.Os.NumGoroutine
	n.FrontierHeight = snap.Frontier.Height
	n.FrontierTime = snap.Frontier.Time()
	n.FrontierAge = now.Sub(n.FrontierTime)
	n.Stalled = n.State == node.Done && n.FrontierAge > StalledAfter

	s.heights = append(s.heights, heightPoint{now, n.CurrentHeight})
	if len(s.heights) > rateWindow {
		s.heights = s.heights[len(s.heights)-rateWindow:]
	}
	n.MomentumsPerSec = rate(s.heights)
	if n.State == node.Syncing && n.MomentumsPerSec > 0 && n.TargetHeight > n.CurrentHeight {
		n.ETA = time.Duration(float64(n.TargetHeight-n.CurrentHeight)/n.MomentumsPerSec) * time.Second
		n.ETAKnown = true
	}
	return n
}

// rate is the height change per second between the oldest and newest point.
func rate(points []heightPoint) float64 {
	if len(points) < 2 {
		return 0
	}
	first, last := points[0], points[len(points)-1]
	secs := last.t.Sub(first.t).Seconds()
	if secs <= 0 || last.h < first.h {
		return 0
	}
	return float64(last.h-first.h) / secs
}
```
Formatting helpers and `Format`:
```go
// Format renders the sample as the aligned text block of nomctl status.
func Format(s Sample) string {
	var b strings.Builder
	line := func(label, text string) { fmt.Fprintf(&b, "%-9s %s\n", label, text) }

	switch {
	case s.Service.Error != "" && !s.Service.Found:
		line("Service", fmt.Sprintf("%s: %s", s.Service.Unit, s.Service.Error))
	default:
		up := ""
		if !s.Service.Since.IsZero() && s.Service.ActiveState == "active" {
			up = ", up " + HumanDuration(s.Taken.Sub(s.Service.Since))
		}
		line("Service", fmt.Sprintf("%s %s (%s), pid %d, %d restarts%s", s.Service.Unit, s.Service.ActiveState, s.Service.SubState, s.Service.MainPID, s.Service.NRestarts, up))
	}

	if !s.Node.Reachable {
		line("Node", fmt.Sprintf("node rpc unreachable at %s: %s", strings.TrimPrefix(s.Node.URL, "http://"), s.Node.Error))
	} else {
		n := s.Node
		sync := n.StateText
		if n.TargetHeight > 0 {
			sync += fmt.Sprintf(" %s / %s (%.1f%%)", Commas(n.CurrentHeight), Commas(n.TargetHeight), float64(n.CurrentHeight)/float64(n.TargetHeight)*100)
		}
		if n.MomentumsPerSec > 0 {
			sync += fmt.Sprintf(", %.1f mom/s", n.MomentumsPerSec)
		}
		if n.ETAKnown {
			sync += ", ETA " + HumanDuration(n.ETA)
		}
		if n.Stalled {
			sync += " [STALLED]"
		}
		line("Node", fmt.Sprintf("znnd %s (%s), %s", n.Version, n.Commit, sync))
		line("Peers", fmt.Sprintf("%d connected", n.NumPeers))
		line("Frontier", fmt.Sprintf("height %s, %s ago", Commas(n.FrontierHeight), HumanDuration(n.FrontierAge)))
	}

	if s.Process.Present {
		p := s.Process
		fds := fmt.Sprintf("%d", p.OpenFDs)
		if p.FDLimit > 0 {
			fds += fmt.Sprintf(" / %d", p.FDLimit)
		}
		line("Process", fmt.Sprintf("cpu %.1f%%, rss %s, threads %d, open files %s", p.CPUPercent, HumanBytes(p.RSS), p.Threads, fds))
	} else {
		line("Process", "not running")
	}
	h := s.Host
	line("Host", fmt.Sprintf("load %.2f %.2f %.2f, mem %s / %s available, %s %s free", h.Load1, h.Load5, h.Load15, HumanBytes(h.MemAvailable), HumanBytes(h.MemTotal), h.DataDir, HumanBytes(h.DataDirFree)))
	line("Pressure", fmt.Sprintf("cpu %.1f%%, io %.1f%%, mem %.1f%%", h.Pressure.CPU, h.Pressure.IO, h.Pressure.Memory))
	return b.String()
}

// HumanBytes renders bytes as KiB/MiB/GiB with one decimal.
func HumanBytes(n uint64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := uint64(unit), 0
	for m := n / unit; m >= unit; m /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(n)/float64(div), "KMGTPE"[exp])
}

// HumanDuration renders a duration as "3d 4h", "4h 5m", "5m 6s" or "6s".
func HumanDuration(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	d = d.Round(time.Second)
	days := int(d.Hours()) / 24
	hours := int(d.Hours()) % 24
	mins := int(d.Minutes()) % 60
	secs := int(d.Seconds()) % 60
	switch {
	case days > 0:
		return fmt.Sprintf("%dd %dh", days, hours)
	case hours > 0:
		return fmt.Sprintf("%dh %dm", hours, mins)
	case mins > 0:
		return fmt.Sprintf("%dm %ds", mins, secs)
	default:
		return fmt.Sprintf("%ds", secs)
	}
}

// Commas renders 1234567 as "1,234,567".
func Commas(n uint64) string {
	s := fmt.Sprint(n)
	if len(s) <= 3 {
		return s
	}
	var b strings.Builder
	pre := len(s) % 3
	if pre > 0 {
		b.WriteString(s[:pre])
	}
	for i := pre; i < len(s); i += 3 {
		if b.Len() > 0 {
			b.WriteByte(',')
		}
		b.WriteString(s[i : i+3])
	}
	return b.String()
}
```

- [ ] **Step 4: Run tests and lint**

Run: `GOWORK=off go test ./internal/metrics/ && GOWORK=off golangci-lint run ./internal/metrics/`
Expected: PASS, 0 issues. Adjust the "Found" logic until `TestStalledAndUnreachable`'s Format assertion for `go-zenon active (running)` passes.

- [ ] **Step 5: Commit**

```bash
git add internal/metrics && git commit -m "Add metrics Sampler with sync rate, ETA and text format"
```

---

### Task 4: `nomctl status`

**Files:**
- Create: `cmd/status.go`
- Modify: `cmd/root.go` (add `annotationNoPreflight`, honour it in `setup`)
- Test: `cmd/status_test.go`

**Interfaces:**
- Consumes: `metrics.NewSampler`, `Sampler.Take`, `metrics.Format`.
- Produces: `const annotationNoPreflight = "nomctl.noPreflight"`, helper `diagnostic()` returning annotations `{root:true, noPreflight:true}`.

- [ ] **Step 1: Write the failing test**

`cmd/status_test.go`:
```go
package cmd

import "testing"

func TestDiagnosticCommandsSkipPreflight(t *testing.T) {
	for _, c := range []string{"status", "top", "support-bundle"} {
		sub, _, err := rootCmd.Find([]string{c})
		if err != nil || sub.Name() != c {
			t.Fatalf("%s not registered: %v", c, err)
		}
		if sub.Annotations[annotationRoot] != "true" || sub.Annotations[annotationNoPreflight] != "true" {
			t.Errorf("%s must require root and skip preflight: %v", c, sub.Annotations)
		}
	}
}
```
(`top` and `support-bundle` are added in Tasks 5 and 8; until then temporarily test only `status`, then extend the list.)

- [ ] **Step 2: Run test to verify it fails**

Run: `GOWORK=off go test ./cmd/ -run TestDiagnosticCommandsSkipPreflight`
Expected: FAIL, undefined annotationNoPreflight.

- [ ] **Step 3: Implement**

In `cmd/root.go` add after `annotationRoot`:
```go
// annotation key marking diagnostic commands that must never be blocked by
// the pre-flight checks.
const annotationNoPreflight = "nomctl.noPreflight"
```
Change the pre-flight condition in `setup` to:
```go
	if requiresRoot && !cfg.SkipPreflight && cmd.Annotations[annotationNoPreflight] != "true" {
```
Add next to `rootOnly()`:
```go
// diagnostic marks a command as privileged but exempt from pre-flight checks.
func diagnostic() map[string]string {
	return map[string]string{annotationRoot: "true", annotationNoPreflight: "true"}
}
```
`cmd/status.go`:
```go
package cmd

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/0x3639/nomctl/internal/metrics"
)

var flagStatusJSON bool

var statusCmd = &cobra.Command{
	Use:   "status",
	Short: "Show service, sync, process and host state in one screen",
	Long: `Prints one sample of everything nomctl top shows: systemd state, sync
state and heights from the node's local RPC, process CPU/memory/open files,
and host load, memory, disk and pressure. Safe to run at any time.`,
	Args:        cobra.NoArgs,
	Annotations: diagnostic(),
	RunE: func(cmd *cobra.Command, _ []string) error {
		sample := metrics.NewSampler(cfg).Take(context.Background())
		if flagStatusJSON {
			enc := json.NewEncoder(cmd.OutOrStdout())
			enc.SetIndent("", "  ")
			return enc.Encode(sample)
		}
		fmt.Fprint(cmd.OutOrStdout(), metrics.Format(sample))
		return nil
	},
}

func init() {
	statusCmd.Flags().BoolVar(&flagStatusJSON, "json", false, "print the sample as JSON")
	rootCmd.AddCommand(statusCmd)
}
```

- [ ] **Step 4: Run tests, lint, and try it**

Run: `GOWORK=off go test ./cmd/ && GOWORK=off golangci-lint run ./... && GOWORK=off go run . status --help | head -3`
Expected: PASS, 0 issues, help text printed. (Running `status` itself needs root and Linux.)

- [ ] **Step 5: Commit**

```bash
git add cmd && git commit -m "Add nomctl status"
```

---

### Task 5: `nomctl top` dashboard

**Files:**
- Create: `internal/tui/top.go`, `cmd/top.go`
- Test: `internal/tui/top_test.go`

**Interfaces:**
- Consumes: `metrics.Sampler`, `metrics.Sample`, `metrics.Format` helpers (`HumanBytes`, `HumanDuration`, `Commas`).
- Produces:
  ```go
  func Top(cfg config.Config, interval time.Duration) error        // runs the program
  type topModel struct{...}; func newTopModel(s *metrics.Sampler, interval time.Duration) topModel
  func renderTop(sample metrics.Sample, history []float64, width int, now time.Time) string   // pure, tested
  ```

- [ ] **Step 1: Write the failing test**

`internal/tui/top_test.go`:
```go
package tui

import (
	"strings"
	"testing"
	"time"

	"github.com/0x3639/nomctl/internal/metrics"
	"github.com/0x3639/nomctl/internal/node"
)

func fixedSample() metrics.Sample {
	taken := time.Date(2026, 9, 6, 12, 0, 0, 0, time.UTC)
	return metrics.Sample{
		Taken: taken,
		Service: metrics.ServiceSample{Unit: "go-zenon", Found: true, ActiveState: "active", SubState: "running", MainPID: 1234, NRestarts: 0, Since: taken.Add(-26 * time.Hour)},
		Process: metrics.ProcessSample{Present: true, CPUPercent: 42, RSS: 1900 << 20, Threads: 38, OpenFDs: 412, FDLimit: 32768},
		Host:    metrics.HostSample{Load1: 1.2, Load5: 0.9, Load15: 0.8, MemTotal: 8 << 30, MemAvailable: 3 << 30, DataDir: "/root/.znn", DataDirFree: 210 << 30, DataDirTotal: 500 << 30, Pressure: metrics.Pressure{CPU: 2.1, IO: 15.4}},
		Node: metrics.NodeSample{URL: node.DefaultURL, Reachable: true, State: node.Syncing, StateText: "syncing", CurrentHeight: 1234567, TargetHeight: 2000000, NumPeers: 14, Version: "v0.0.7", Commit: "a1b2c3d", FrontierHeight: 1234567, FrontierAge: 3 * time.Second, MomentumsPerSec: 5.2, ETA: 40 * time.Hour, ETAKnown: true},
	}
}

func TestRenderTop(t *testing.T) {
	out := renderTop(fixedSample(), []float64{1, 2, 3, 5.2}, 100, time.Date(2026, 9, 6, 12, 0, 1, 0, time.UTC))
	for _, want := range []string{"NODE", "PROCESS", "HOST", "syncing", "1,234,567", "2,000,000", "61.7%", "5.2 mom/s", "ETA 1d 16h", "14 peers", "42.0%", "1.9 GiB", "412 / 32768", "210.0 GiB", "q quit"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
	for _, line := range strings.Split(out, "\n") {
		if w := len([]rune(stripANSI(line))); w > 100 {
			t.Errorf("line wider than terminal (%d): %q", w, line)
		}
	}
}

func TestRenderTopUnreachable(t *testing.T) {
	s := fixedSample()
	s.Node = metrics.NodeSample{URL: node.DefaultURL, Error: "connection refused"}
	out := renderTop(s, nil, 80, s.Taken)
	if !strings.Contains(out, "unreachable") || !strings.Contains(out, "connection refused") {
		t.Errorf("unreachable node not shown:\n%s", out)
	}
}

func TestSparkline(t *testing.T) {
	if got := sparkline([]float64{0, 1, 2, 3, 4, 5, 6, 7}, 8); got != "▁▂▃▄▅▆▇█" {
		t.Errorf("sparkline = %q", got)
	}
	if got := sparkline(nil, 8); got != "" {
		t.Errorf("empty sparkline = %q", got)
	}
	if got := sparkline([]float64{1, 2, 3}, 2); len([]rune(got)) != 2 {
		t.Errorf("sparkline should keep the newest points: %q", got)
	}
}
```
`stripANSI` is a small test helper using `regexp.MustCompile("\x1b\\[[0-9;]*m")`.

- [ ] **Step 2: Run test to verify it fails**

Run: `GOWORK=off go test ./internal/tui/ -run 'TestRenderTop|TestSparkline'`
Expected: FAIL, undefined renderTop.

- [ ] **Step 3: Implement top.go**

```go
package tui

import (
	"context"
	"fmt"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/0x3639/nomctl/internal/config"
	"github.com/0x3639/nomctl/internal/metrics"
	"github.com/0x3639/nomctl/internal/ui"
)

// historyLen is how many rate points the sparkline keeps.
const historyLen = 60

// Top runs the live dashboard until q, Esc or Ctrl+C.
func Top(cfg config.Config, interval time.Duration) error {
	m := newTopModel(metrics.NewSampler(cfg), interval)
	_, err := tea.NewProgram(m, tea.WithAltScreen()).Run()
	return err
}

type sampleMsg metrics.Sample
type tickMsg time.Time

type topModel struct {
	sampler  *metrics.Sampler
	interval time.Duration
	sample   metrics.Sample
	history  []float64
	width    int
	sampling bool
}

func newTopModel(s *metrics.Sampler, interval time.Duration) topModel {
	return topModel{sampler: s, interval: interval, width: 80}
}

func (m topModel) Init() tea.Cmd { return tea.Batch(m.takeSample(), m.tick()) }

func (m topModel) tick() tea.Cmd {
	return tea.Tick(m.interval, func(t time.Time) tea.Msg { return tickMsg(t) })
}

func (m topModel) takeSample() tea.Cmd {
	s := m.sampler
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		return sampleMsg(s.Take(ctx))
	}
}

func (m topModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		switch msg.String() {
		case "q", "esc", "ctrl+c":
			return m, tea.Quit
		}
	case tea.WindowSizeMsg:
		m.width = msg.Width
	case tickMsg:
		if m.sampling {
			return m, m.tick()
		}
		m.sampling = true
		return m, tea.Batch(m.takeSample(), m.tick())
	case sampleMsg:
		m.sampling = false
		m.sample = metrics.Sample(msg)
		if m.sample.Node.Reachable {
			m.history = append(m.history, m.sample.Node.MomentumsPerSec)
			if len(m.history) > historyLen {
				m.history = m.history[len(m.history)-historyLen:]
			}
		}
	}
	return m, nil
}

func (m topModel) View() string {
	if m.sample.Taken.IsZero() {
		return "Sampling…\n"
	}
	return renderTop(m.sample, m.history, m.width, time.Now())
}

var (
	stylePanelTitle = lipgloss.NewStyle().Bold(true).Foreground(ui.ColorAccent)
	stylePanel      = lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(ui.ColorDim).Padding(0, 1)
	styleWarn       = lipgloss.NewStyle().Foreground(lipgloss.Color("214")).Bold(true)
	styleBad        = lipgloss.NewStyle().Foreground(lipgloss.Color("196")).Bold(true)
	styleFooter     = lipgloss.NewStyle().Foreground(ui.ColorMuted)
)

// renderTop draws the three panels for a sample. width is the terminal
// width; now is used for the "sampled Ns ago" footer.
func renderTop(s metrics.Sample, history []float64, width int, now time.Time) string {
	inner := max(40, width-4)
	panel := func(title string, lines ...string) string {
		body := stylePanelTitle.Render(title) + "\n" + strings.Join(lines, "\n")
		return stylePanel.Width(inner).Render(body)
	}

	var nodeLines []string
	n := s.Node
	if !n.Reachable {
		nodeLines = append(nodeLines, styleBad.Render("node rpc unreachable")+" at "+strings.TrimPrefix(n.URL, "http://")+": "+n.Error)
	} else {
		state := n.StateText
		if n.Stalled {
			state = styleBad.Render(state + " but stalled")
		}
		nodeLines = append(nodeLines, fmt.Sprintf("znnd %s (%s)   %s   %d peers", n.Version, n.Commit, state, n.NumPeers))
		if n.TargetHeight > 0 {
			pct := float64(n.CurrentHeight) / float64(n.TargetHeight)
			nodeLines = append(nodeLines, fmt.Sprintf("height %s / %s (%.1f%%)", metrics.Commas(n.CurrentHeight), metrics.Commas(n.TargetHeight), pct*100))
			nodeLines = append(nodeLines, progressBar(pct, min(60, inner-4)))
		}
		rateLine := fmt.Sprintf("%.1f mom/s", n.MomentumsPerSec)
		if n.ETAKnown {
			rateLine += "   ETA " + metrics.HumanDuration(n.ETA)
		}
		if sl := sparkline(history, min(30, inner-len(rateLine)-6)); sl != "" {
			rateLine += "   " + ui.StyleAccent.Render(sl)
		}
		nodeLines = append(nodeLines, rateLine)
		nodeLines = append(nodeLines, fmt.Sprintf("frontier %s, %s ago", metrics.Commas(n.FrontierHeight), metrics.HumanDuration(n.FrontierAge)))
	}

	var procLines []string
	svc := s.Service
	svcText := fmt.Sprintf("%s %s (%s)", svc.Unit, svc.ActiveState, svc.SubState)
	if svc.ActiveState != "active" {
		svcText = styleBad.Render(svcText)
	}
	if !svc.Since.IsZero() {
		svcText += fmt.Sprintf("   up %s", metrics.HumanDuration(s.Taken.Sub(svc.Since)))
	}
	restarts := fmt.Sprintf("%d restarts", svc.NRestarts)
	if svc.NRestarts > 0 {
		restarts = styleWarn.Render(restarts)
	}
	procLines = append(procLines, svcText+"   "+restarts)
	p := s.Process
	if p.Present {
		fds := fmt.Sprintf("%d", p.OpenFDs)
		if p.FDLimit > 0 {
			fds += fmt.Sprintf(" / %d", p.FDLimit)
			if float64(p.OpenFDs) > float64(p.FDLimit)*0.8 {
				fds = styleWarn.Render(fds)
			}
		}
		procLines = append(procLines, fmt.Sprintf("cpu %.1f%%   rss %s   threads %d   open files %s", p.CPUPercent, metrics.HumanBytes(p.RSS), p.Threads, fds))
		if p.Cgroup.Present {
			procLines = append(procLines, fmt.Sprintf("cgroup mem %s (peak %s)   pids %d", metrics.HumanBytes(p.Cgroup.MemoryCurrent), metrics.HumanBytes(p.Cgroup.MemoryPeak), p.Cgroup.PidsCurrent))
		}
	} else {
		procLines = append(procLines, styleBad.Render("process not running"))
	}

	h := s.Host
	hostLines := []string{
		fmt.Sprintf("load %.2f %.2f %.2f   mem %s / %s available", h.Load1, h.Load5, h.Load15, metrics.HumanBytes(h.MemAvailable), metrics.HumanBytes(h.MemTotal)),
		fmt.Sprintf("%s %s free of %s", h.DataDir, metrics.HumanBytes(h.DataDirFree), metrics.HumanBytes(h.DataDirTotal)),
		fmt.Sprintf("pressure cpu %.1f%%   io %.1f%%   mem %.1f%%", h.Pressure.CPU, h.Pressure.IO, h.Pressure.Memory),
	}

	footer := styleFooter.Render(fmt.Sprintf("sampled %s ago   q quit", metrics.HumanDuration(now.Sub(s.Taken))))
	return strings.Join([]string{panel("NODE", nodeLines...), panel("PROCESS", procLines...), panel("HOST", hostLines...), footer}, "\n") + "\n"
}

// progressBar renders a filled bar of the given width for pct in [0,1].
func progressBar(pct float64, width int) string {
	if width < 4 {
		width = 4
	}
	pct = min(max(pct, 0), 1)
	filled := int(pct * float64(width))
	return ui.StyleAccent.Render(strings.Repeat("█", filled)) + lipgloss.NewStyle().Foreground(ui.ColorDim).Render(strings.Repeat("░", width-filled))
}

var sparkRunes = []rune("▁▂▃▄▅▆▇█")

// sparkline renders the newest `width` values scaled to their maximum.
func sparkline(values []float64, width int) string {
	if len(values) == 0 || width <= 0 {
		return ""
	}
	if len(values) > width {
		values = values[len(values)-width:]
	}
	maxV := 0.0
	for _, v := range values {
		maxV = max(maxV, v)
	}
	var b strings.Builder
	for _, v := range values {
		idx := 0
		if maxV > 0 {
			idx = int(v / maxV * float64(len(sparkRunes)-1))
		}
		b.WriteRune(sparkRunes[idx])
	}
	return b.String()
}
```
`cmd/top.go`:
```go
package cmd

import (
	"errors"
	"time"

	"github.com/spf13/cobra"

	"github.com/0x3639/nomctl/internal/tui"
	"github.com/0x3639/nomctl/internal/ui"
)

var flagTopInterval time.Duration

var topCmd = &cobra.Command{
	Use:         "top",
	Short:       "Live dashboard of sync, process and host metrics",
	Args:        cobra.NoArgs,
	Annotations: diagnostic(),
	RunE: func(*cobra.Command, []string) error {
		if !ui.Interactive() {
			return errors.New("nomctl top needs a terminal; use nomctl status for scripts")
		}
		if flagTopInterval < 500*time.Millisecond {
			return errors.New("--interval must be at least 500ms")
		}
		return tui.Top(cfg, flagTopInterval)
	},
}

func init() {
	topCmd.Flags().DurationVar(&flagTopInterval, "interval", 2*time.Second, "refresh interval")
	rootCmd.AddCommand(topCmd)
}
```
Extend `TestDiagnosticCommandsSkipPreflight` to include `"top"`.

- [ ] **Step 4: Run tests and lint**

Run: `GOWORK=off go test ./... && GOWORK=off golangci-lint run ./...`
Expected: PASS, 0 issues.

- [ ] **Step 5: Commit**

```bash
git add cmd internal/tui && git commit -m "Add nomctl top live dashboard"
```

---

### Task 6: Support bundle helpers: redaction, crash markers, log tails

**Files:**
- Create: `internal/support/redact.go`, `internal/support/logs.go`
- Test: `internal/support/redact_test.go`, `internal/support/logs_test.go`

**Interfaces:**
- Produces:
  ```go
  func Redact(s string) string                                   // the script's two sed expressions
  var CrashMarkers *regexp.Regexp                                // the script's grep -Eai pattern
  func FilterCrashMarkers(r io.Reader, w io.Writer) error        // writes matching lines
  func RedactPeers(networkInfo []byte) ([]byte, error)           // replace peers[].ip and self.ip with "<redacted>"
  type LogFile struct { Path string; Size int64; ModTime time.Time }
  func ListLogs(logDir string) ([]LogFile, error)                // depth <= 3, newest first
  func TailLog(src, dst string, maxBytes int64) error            // gzip-aware last N bytes
  const MaxLogFiles = 30; const LogTailBytes = 4 << 20
  ```

- [ ] **Step 1: Write the failing tests**

`internal/support/redact_test.go`:
```go
package support

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

func TestRedact(t *testing.T) {
	cases := map[string]string{
		`--password=hunter2 --rpc.token: abc`:              `--password=<redacted> --rpc.token:<redacted>`,
		`Environment="GRAFANA_PASSWORD=secret" User=root`:   `Environment="GRAFANA_PASSWORD=<redacted>" User=root`,
		`Environment=API_KEY=k1 Environment=MNEMONIC=words`: `Environment=API_KEY=<redacted> Environment=MNEMONIC=<redacted>`,
		`plain line without secrets`:                       `plain line without secrets`,
	}
	for in, want := range cases {
		if got := Redact(in); got != want {
			t.Errorf("Redact(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestCrashMarkers(t *testing.T) {
	in := "ok line\nfatal error: runtime: out of memory\nanother ok\nznnd.service: Main process exited, code=killed\nToo Many Open Files\n"
	var out bytes.Buffer
	if err := FilterCrashMarkers(strings.NewReader(in), &out); err != nil {
		t.Fatal(err)
	}
	got := out.String()
	if strings.Contains(got, "ok line") || !strings.Contains(got, "out of memory") || !strings.Contains(got, "Main process exited") || !strings.Contains(got, "Too Many Open Files") {
		t.Errorf("markers:\n%s", got)
	}
}

func TestRedactPeers(t *testing.T) {
	in := []byte(`{"numPeers":2,"peers":[{"publicKey":"a","ip":"1.2.3.4","name":"x"},{"publicKey":"b","ip":"5.6.7.8","name":"y"}],"self":{"publicKey":"s","ip":"9.9.9.9","name":"*self*"}}`)
	out, err := RedactPeers(in)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(out, []byte("1.2.3.4")) || bytes.Contains(out, []byte("9.9.9.9")) {
		t.Errorf("ip leaked: %s", out)
	}
	var v struct {
		NumPeers int
		Peers    []map[string]string
		Self     map[string]string
	}
	if err := json.Unmarshal(out, &v); err != nil || v.NumPeers != 2 || len(v.Peers) != 2 || v.Peers[0]["ip"] != "<redacted>" || v.Peers[0]["publicKey"] != "a" || v.Self["ip"] != "<redacted>" {
		t.Errorf("redacted doc wrong: %s (%v)", out, err)
	}
}
```
`internal/support/logs_test.go`:
```go
package support

import (
	"bytes"
	"compress/gzip"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestListLogsNewestFirstAndDepth(t *testing.T) {
	dir := t.TempDir()
	deep := filepath.Join(dir, "a", "b", "c", "d")
	if err := os.MkdirAll(deep, 0o755); err != nil {
		t.Fatal(err)
	}
	write := func(p string, age time.Duration) {
		if err := os.WriteFile(p, []byte("log"), 0o644); err != nil {
			t.Fatal(err)
		}
		mt := time.Now().Add(-age)
		_ = os.Chtimes(p, mt, mt)
	}
	write(filepath.Join(dir, "old.log"), 2*time.Hour)
	write(filepath.Join(dir, "new.log"), time.Minute)
	write(filepath.Join(dir, "a", "b", "mid.log"), time.Hour)
	write(filepath.Join(deep, "toodeep.log"), 0)
	logs, err := ListLogs(dir)
	if err != nil || len(logs) != 3 || filepath.Base(logs[0].Path) != "new.log" || filepath.Base(logs[2].Path) != "old.log" {
		t.Errorf("logs = %+v, %v", logs, err)
	}
	if _, err := ListLogs(filepath.Join(dir, "missing")); err == nil {
		t.Error("missing dir must error")
	}
}

func TestTailLog(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "znnd.log")
	if err := os.WriteFile(src, bytes.Repeat([]byte("x"), 100), 0o644); err != nil {
		t.Fatal(err)
	}
	dst := filepath.Join(dir, "out.tail")
	if err := TailLog(src, dst, 10); err != nil {
		t.Fatal(err)
	}
	if data, _ := os.ReadFile(dst); len(data) != 10 {
		t.Errorf("tail size = %d", len(data))
	}
	gz := filepath.Join(dir, "old.log.gz")
	f, _ := os.Create(gz)
	w := gzip.NewWriter(f)
	_, _ = w.Write([]byte("hello gzip world"))
	_ = w.Close()
	_ = f.Close()
	if err := TailLog(gz, dst, 5); err != nil {
		t.Fatal(err)
	}
	if data, _ := os.ReadFile(dst); string(data) != "world" {
		t.Errorf("gz tail = %q", data)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `GOWORK=off go test ./internal/support/`
Expected: FAIL, undefined symbols.

- [ ] **Step 3: Implement**

`internal/support/redact.go`:
```go
// Package support builds a shareable diagnostics bundle for a node, a port of
// the collect-znnd-crash.sh script with node RPC and nomctl state added.
package support

import (
	"bufio"
	"encoding/json"
	"io"
	"regexp"
)

var (
	// reAssign matches password=..., token: ... and friends.
	reAssign = regexp.MustCompile(`(?i)((password|passwd|token|secret|api[-_]?key|authorization|mnemonic|private[-_]?key)[[:alnum:]_.-]*[=:])[[:graph:]]+`)
	// reEnv matches systemd Environment= settings carrying secrets.
	reEnv = regexp.MustCompile(`(?i)(Environment="?[^" ]*(PASSWORD|TOKEN|SECRET|API_KEY|PRIVATE_KEY|MNEMONIC)=)[^" ]+`)
)

// Redact masks secret-looking assignments, matching the script's sed rules.
func Redact(s string) string {
	s = reAssign.ReplaceAllString(s, "${1}<redacted>")
	return reEnv.ReplaceAllString(s, "${1}<redacted>")
}

// CrashMarkers matches lines worth reading first after a crash.
var CrashMarkers = regexp.MustCompile(`(?i)panic|fatal|runtime:|goroutine|deadlock|segmentation|signal|out of memory|oom|killed process|memory cgroup|too many open files|no space left|corrupt|leveldb|resource temporarily unavailable|control process exited|main process exited|failed with result|scheduled restart|watchdog|stack trace`)

// FilterCrashMarkers copies the lines of r that match CrashMarkers to w.
func FilterCrashMarkers(r io.Reader, w io.Writer) error {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 1024*1024), 1024*1024)
	for sc.Scan() {
		if CrashMarkers.Match(sc.Bytes()) {
			if _, err := w.Write(append(sc.Bytes(), '\n')); err != nil {
				return err
			}
		}
	}
	return sc.Err()
}

// RedactPeers replaces every peer IP (and self) in a stats.networkInfo
// document with "<redacted>", keeping counts, keys and names.
func RedactPeers(networkInfo []byte) ([]byte, error) {
	var doc map[string]any
	if err := json.Unmarshal(networkInfo, &doc); err != nil {
		return nil, err
	}
	if peers, ok := doc["peers"].([]any); ok {
		for _, p := range peers {
			if m, ok := p.(map[string]any); ok {
				m["ip"] = "<redacted>"
			}
		}
	}
	if self, ok := doc["self"].(map[string]any); ok {
		self["ip"] = "<redacted>"
	}
	return json.MarshalIndent(doc, "", "  ")
}
```
`internal/support/logs.go`:
```go
package support

import (
	"compress/gzip"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// Limits carried over from the script.
const (
	MaxLogFiles  = 30
	LogTailBytes = 4 << 20
	maxLogDepth  = 3
)

// LogFile describes one file under the node's log directory.
type LogFile struct {
	Path    string
	Size    int64
	ModTime time.Time
}

// ListLogs returns regular files up to three levels deep, newest first.
func ListLogs(logDir string) ([]LogFile, error) {
	if _, err := os.Stat(logDir); err != nil {
		return nil, err
	}
	var logs []LogFile
	err := filepath.WalkDir(logDir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return nil // unreadable entries are skipped
		}
		rel, _ := filepath.Rel(logDir, path)
		depth := len(strings.Split(rel, string(filepath.Separator)))
		if d.IsDir() {
			if path != logDir && depth >= maxLogDepth {
				return filepath.SkipDir
			}
			return nil
		}
		info, err := d.Info()
		if err != nil || !info.Mode().IsRegular() {
			return nil
		}
		logs = append(logs, LogFile{Path: path, Size: info.Size(), ModTime: info.ModTime()})
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Slice(logs, func(i, j int) bool { return logs[i].ModTime.After(logs[j].ModTime) })
	return logs, nil
}

// TailLog writes the last maxBytes of src (decompressing .gz) to dst.
func TailLog(src, dst string, maxBytes int64) error {
	f, err := os.Open(src)
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()
	var r io.Reader = f
	if strings.HasSuffix(src, ".gz") {
		gz, err := gzip.NewReader(f)
		if err != nil {
			return err
		}
		defer func() { _ = gz.Close() }()
		r = gz
	} else if info, err := f.Stat(); err == nil && info.Size() > maxBytes {
		if _, err := f.Seek(info.Size()-maxBytes, io.SeekStart); err != nil {
			return err
		}
	}
	data, err := io.ReadAll(r)
	if err != nil {
		return err
	}
	if int64(len(data)) > maxBytes {
		data = data[int64(len(data))-maxBytes:]
	}
	return os.WriteFile(dst, data, 0o600)
}
```

- [ ] **Step 4: Run tests and lint**

Run: `GOWORK=off go test ./internal/support/ && GOWORK=off golangci-lint run ./internal/support/`
Expected: PASS, 0 issues.

- [ ] **Step 5: Commit**

```bash
git add internal/support && git commit -m "Add support bundle redaction, crash markers and log tails"
```

---

### Task 7: Support bundle collection and archive

**Files:**
- Create: `internal/support/collect.go`
- Test: `internal/support/collect_test.go`

**Interfaces:**
- Consumes: Task 6 helpers; `metrics.Sampler`; `node.Client`; `execx`; `config.Config`.
- Produces:
  ```go
  type Options struct { OutputDir string; Since string; Version string }   // Version = nomctl version string
  type Result struct { Dir, Archive, Markers string }
  func DefaultOutputDir(now time.Time) string       // /root/nomctl-support-<host>-<YYYYMMDDTHHMMSSZ>
  func Collect(ctx context.Context, cfg config.Config, opts Options) (Result, error)
  func writeArchive(dir string) (string, error)     // dir.tar.gz, mode 0600
  ```
  Collect must also be usable after a watch (Task 8) that already wrote files 00 and 01 into `opts.OutputDir`; it must not truncate them.

- [ ] **Step 1: Write the failing test**

`internal/support/collect_test.go`:
```go
package support

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/0x3639/nomctl/internal/config"
	"github.com/0x3639/nomctl/internal/execx"
)

// stubTools puts fake systemctl/journalctl/etc on PATH that echo their args.
func stubTools(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	for _, tool := range []string{"systemctl", "journalctl", "free", "df", "ps", "coredumpctl", "uname", "uptime"} {
		script := "#!/bin/sh\necho \"" + tool + " $*\"\n"
		if tool == "systemctl" {
			script = "#!/bin/sh\ncase \"$1\" in show) echo 'ActiveState=active'; echo 'MainPID=0'; echo 'ControlGroup=';; *) echo \"systemctl $*\";; esac\n"
		}
		if err := os.WriteFile(filepath.Join(dir, tool), []byte(script), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	execx.Configure(false, nil)
}

func TestCollectWritesEveryFile(t *testing.T) {
	stubTools(t)
	cfg := config.Default()
	cfg.ZnnDir = t.TempDir()
	if err := os.MkdirAll(filepath.Join(cfg.ZnnDir, "log"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cfg.ZnnDir, "log", "znnd.log"), []byte("INFO ok\nFATAL panic: boom\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cfg.ZnnDir, "config.json"), []byte(`{"secret":1}`), 0o600); err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(t.TempDir(), "bundle")
	res, err := Collect(context.Background(), cfg, Options{OutputDir: out, Since: "1 hour ago", Version: "test"})
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"02-summary.txt", "03-service-status.txt", "04-service-properties.txt", "05-service-unit.txt", "06-service-journal.log", "07-kernel-journal.log", "08-system-warnings.log", "09-live-process.txt", "09-cgroup.txt", "10-host-resources.txt", "11-app-log-inventory.txt", "13-crash-markers.log", "14-coredumps-list.txt", "16-binary.txt", "17-oom-and-boots.txt", "18-node-rpc.json", "19-nomctl.txt", "20-status.txt"} {
		if _, err := os.Stat(filepath.Join(out, name)); err != nil {
			t.Errorf("missing %s", name)
		}
	}
	if _, err := os.Stat(filepath.Join(out, "12-app-log-tails", "01-znnd.log.tail")); err != nil {
		t.Error("missing log tail")
	}
	markers, _ := os.ReadFile(res.Markers)
	if !strings.Contains(string(markers), "panic: boom") {
		t.Errorf("crash markers should include the app log line: %s", markers)
	}
	if st, _ := os.Stat(out); st.Mode().Perm() != 0o700 {
		t.Errorf("dir mode = %o", st.Mode().Perm())
	}
	st, err := os.Stat(res.Archive)
	if err != nil || st.Mode().Perm() != 0o600 {
		t.Errorf("archive: %v mode %v", err, st)
	}
	names := tarNames(t, res.Archive)
	if !names["bundle/02-summary.txt"] || names["bundle/config.json"] {
		t.Errorf("archive contents wrong: %v", names)
	}
	found := false
	for n := range names {
		found = found || strings.Contains(n, "config.json")
	}
	if found {
		t.Error("config.json must never be collected")
	}
	rpc, _ := os.ReadFile(filepath.Join(out, "18-node-rpc.json"))
	if !strings.Contains(string(rpc), "error") {
		t.Errorf("unreachable node must be recorded as error: %s", rpc)
	}
}

func tarNames(t *testing.T, archive string) map[string]bool {
	t.Helper()
	f, err := os.Open(archive)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		t.Fatal(err)
	}
	tr := tar.NewReader(gz)
	names := map[string]bool{}
	for {
		h, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		names[h.Name] = true
	}
	return names
}

func TestDefaultOutputDir(t *testing.T) {
	d := DefaultOutputDir(time.Date(2026, 9, 6, 1, 2, 3, 0, time.UTC))
	if !strings.HasPrefix(d, "/root/nomctl-support-") || !strings.HasSuffix(d, "-20260906T010203Z") {
		t.Errorf("dir = %q", d)
	}
}
```
The test's node client must point at a closed port: set `Options.NodeURL` (add this unexported-by-default field: `NodeURL string` defaulting to `node.DefaultURL`) to `http://127.0.0.1:1` in the test.

- [ ] **Step 2: Run test to verify it fails**

Run: `GOWORK=off go test ./internal/support/ -run TestCollect`
Expected: FAIL, undefined Collect.

- [ ] **Step 3: Implement collect.go**

```go
package support

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/0x3639/nomctl/internal/backup"
	"github.com/0x3639/nomctl/internal/config"
	"github.com/0x3639/nomctl/internal/execx"
	"github.com/0x3639/nomctl/internal/fsx"
	"github.com/0x3639/nomctl/internal/metrics"
	"github.com/0x3639/nomctl/internal/node"
)

// Options tunes a collection run.
type Options struct {
	OutputDir string
	Since     string
	Version   string
	NodeURL   string
}

// Result is where the bundle landed.
type Result struct {
	Dir     string
	Archive string
	Markers string
}

// DefaultOutputDir is /root/nomctl-support-<host>-<UTC stamp>.
func DefaultOutputDir(now time.Time) string {
	host, err := os.Hostname()
	if err != nil || host == "" {
		host = "unknown-host"
	}
	if i := strings.IndexByte(host, '.'); i > 0 {
		host = host[:i]
	}
	return filepath.Join("/root", fmt.Sprintf("nomctl-support-%s-%s", host, now.UTC().Format("20060102T150405Z")))
}

// bundle carries state through the collection steps.
type bundle struct {
	dir  string
	cfg  config.Config
	opts Options
	unit string
}

// write creates (or truncates) a file in the bundle with the given content.
func (b *bundle) write(name, content string) {
	if err := os.WriteFile(filepath.Join(b.dir, name), []byte(content), 0o600); err != nil {
		slog.Warn("bundle: cannot write " + name + ": " + err.Error())
	}
}

// capture runs a command and stores its output (or error) in a file. It
// mirrors the script's capture(): a header, then stdout+stderr, best effort.
func (b *bundle) capture(name string, redact bool, cmd string, args ...string) string {
	out, err := execx.New(cmd, args...).Output()
	header := fmt.Sprintf("command: %s %s\ncollected: %s\n\n", cmd, strings.Join(args, " "), time.Now().UTC().Format(time.RFC3339))
	if err != nil {
		out += "\n[error] " + err.Error()
	}
	if redact {
		out = Redact(out)
	}
	b.write(name, header+out+"\n")
	return out
}

// Collect gathers everything into opts.OutputDir and archives it.
func Collect(ctx context.Context, cfg config.Config, opts Options) (Result, error) {
	if opts.OutputDir == "" {
		opts.OutputDir = DefaultOutputDir(time.Now())
	}
	if opts.Since == "" {
		opts.Since = "12 hours ago"
	}
	if opts.NodeURL == "" {
		opts.NodeURL = node.DefaultURL
	}
	if err := os.MkdirAll(opts.OutputDir, 0o700); err != nil {
		return Result{}, fmt.Errorf("create %s: %w", opts.OutputDir, err)
	}
	_ = os.Chmod(opts.OutputDir, 0o700)
	b := &bundle{dir: opts.OutputDir, cfg: cfg, opts: opts, unit: cfg.ServiceUnit()}

	sampler := metrics.NewSampler(cfg)
	sampler.Node = node.New(opts.NodeURL)
	sample := sampler.Take(ctx)

	b.summary(sample)
	b.service()
	b.journals()
	b.process(sample)
	b.hostResources()
	b.appLogs()
	b.crashMarkers()
	b.coredumps()
	b.binary(sample)
	b.oomAndBoots()
	b.nodeRPC(ctx, opts.NodeURL)
	b.nomctl(sample)
	b.write("20-status.txt", metrics.Format(sample))

	archive, err := writeArchive(opts.OutputDir)
	if err != nil {
		return Result{}, err
	}
	return Result{Dir: opts.OutputDir, Archive: archive, Markers: filepath.Join(opts.OutputDir, "13-crash-markers.log")}, nil
}

func (b *bundle) summary(s metrics.Sample) {
	host, _ := os.Hostname()
	uname, _ := execx.Output("uname", "-a")
	uptime, _ := execx.Output("uptime")
	var sb strings.Builder
	fmt.Fprintf(&sb, "host=%s\ncollected=%s\nnomctl=%s\nservice=%s\ndata-dir=%s\njournal-since=%s\nuid=%d\n\n%s\n%s\n",
		host, time.Now().UTC().Format(time.RFC3339), b.opts.Version, b.unit, b.cfg.ZnnDir, b.opts.Since, os.Getuid(), uname, uptime)
	if os.Geteuid() != 0 {
		sb.WriteString("WARNING: not running as root; journal, kernel, /proc, or coredump evidence may be incomplete.\n")
	}
	if !s.Service.Found {
		sb.WriteString("WARNING: unit " + b.unit + " not found.\n")
	}
	b.write("02-summary.txt", sb.String())
}

var showProperties = []string{"Id", "Description", "LoadState", "ActiveState", "SubState", "Result", "MainPID", "ControlPID", "NRestarts", "Restart", "RestartUSec", "Type", "User", "Group", "ExecStart", "ExecStop", "ExecStopPost", "ControlGroup", "ExecMainStartTimestamp", "ExecMainExitTimestamp", "ExecMainCode", "ExecMainStatus", "WatchdogUSec", "WatchdogTimestamp", "OOMPolicy", "MemoryCurrent", "MemoryPeak", "MemoryMax", "TasksCurrent", "TasksMax", "LimitNOFILE", "CPUUsageNSec"}

func (b *bundle) service() {
	b.capture("03-service-status.txt", false, "systemctl", "status", b.unit, "--full", "--no-pager")
	args := []string{"show", b.unit}
	for _, p := range showProperties {
		args = append(args, "-p", p)
	}
	b.capture("04-service-properties.txt", true, "systemctl", args...)
	b.capture("05-service-unit.txt", true, "systemctl", "cat", b.unit)
}

func (b *bundle) journals() {
	since := b.opts.Since
	b.capture("06-service-journal.log", false, "journalctl", "-u", b.unit, "--since", since, "-o", "short-iso-precise", "--no-pager")
	b.capture("07-kernel-journal.log", false, "journalctl", "-k", "--since", since, "-o", "short-iso-precise", "--no-pager")
	b.capture("08-system-warnings.log", false, "journalctl", "--since", since, "-p", "warning..alert", "-o", "short-iso-precise", "--no-pager")
}

func (b *bundle) process(s metrics.Sample) {
	pid := s.Service.MainPID
	var sb strings.Builder
	fmt.Fprintf(&sb, "pid=%d\ncollected=%s\n\n", pid, time.Now().UTC().Format(time.RFC3339))
	procDir := filepath.Join(metrics.DefaultProcRoot, strconv.Itoa(pid))
	if pid <= 0 || !fsx.IsDir(procDir) {
		sb.WriteString("znnd main process is not currently available\n")
	} else {
		exe, _ := os.Readlink(filepath.Join(procDir, "exe"))
		fmt.Fprintf(&sb, "== executable ==\n%s\n\n", exe)
		for _, f := range []string{"status", "limits", "io", "cgroup"} {
			data, err := os.ReadFile(filepath.Join(procDir, f))
			if err != nil {
				data = []byte("[error] " + err.Error())
			}
			fmt.Fprintf(&sb, "== %s ==\n%s\n\n", f, data)
		}
		fmt.Fprintf(&sb, "== file descriptors ==\n%d\n", s.Process.OpenFDs)
	}
	b.write("09-live-process.txt", sb.String())

	cg := s.Process.Cgroup
	var cb strings.Builder
	fmt.Fprintf(&cb, "ControlGroup=%s\npresent=%v\n", s.Service.ControlGroupForCgroup(), cg.Present)
	if cg.Present {
		fmt.Fprintf(&cb, "memory.current=%d\nmemory.peak=%d\nmemory.max=%d\npids.current=%d\n", cg.MemoryCurrent, cg.MemoryPeak, cg.MemoryMax, cg.PidsCurrent)
		dir := filepath.Join(metrics.DefaultCgroupRoot, s.Service.ControlGroupForCgroup())
		for _, f := range []string{"memory.events", "memory.events.local", "pids.events", "cpu.stat", "io.stat"} {
			if data, err := os.ReadFile(filepath.Join(dir, f)); err == nil {
				fmt.Fprintf(&cb, "== %s ==\n%s\n", f, data)
			}
		}
	}
	b.write("09-cgroup.txt", cb.String())
}

func (b *bundle) hostResources() {
	var sb strings.Builder
	section := func(title string, cmd string, args ...string) {
		out, err := execx.Output(cmd, args...)
		if err != nil {
			out += "\n[error] " + err.Error()
		}
		fmt.Fprintf(&sb, "== %s ==\n%s\n\n", title, out)
	}
	section("memory", "free", "-h")
	if data, err := os.ReadFile("/proc/meminfo"); err == nil {
		fmt.Fprintf(&sb, "%s\n", data)
	}
	sb.WriteString("== pressure ==\n")
	for _, f := range []string{"/proc/pressure/cpu", "/proc/pressure/io", "/proc/pressure/memory"} {
		if data, err := os.ReadFile(f); err == nil {
			fmt.Fprintf(&sb, "-- %s\n%s", f, data)
		}
	}
	sb.WriteString("\n")
	section("filesystems", "df", "-hT")
	section("inodes", "df", "-i")
	section("top processes by RSS", "sh", "-c", "ps -eo pid,ppid,user,stat,%cpu,%mem,rss,vsz,nlwp,etimes,comm --sort=-rss | head -50")
	section("shell limits", "sh", "-c", "ulimit -a")
	b.write("10-host-resources.txt", sb.String())
}

func (b *bundle) appLogs() {
	logDir := filepath.Join(b.cfg.ZnnDir, "log")
	var inv strings.Builder
	fmt.Fprintf(&inv, "data-dir=%s\nlog-dir=%s\nConfig contents are intentionally not collected.\n\n", b.cfg.ZnnDir, logDir)
	logs, err := ListLogs(logDir)
	if err != nil {
		inv.WriteString("Log directory not found or unreadable: " + err.Error() + "\n")
		b.write("11-app-log-inventory.txt", inv.String())
		return
	}
	for _, l := range logs {
		fmt.Fprintf(&inv, "%s|%d|%s\n", l.ModTime.UTC().Format(time.RFC3339), l.Size, l.Path)
	}
	b.write("11-app-log-inventory.txt", inv.String())
	tails := filepath.Join(b.dir, "12-app-log-tails")
	if err := os.MkdirAll(tails, 0o700); err != nil {
		return
	}
	for i, l := range logs {
		if i >= MaxLogFiles {
			break
		}
		dst := filepath.Join(tails, fmt.Sprintf("%02d-%s.tail", i+1, filepath.Base(l.Path)))
		if err := TailLog(l.Path, dst, LogTailBytes); err != nil {
			slog.Warn("bundle: cannot tail " + l.Path + ": " + err.Error())
		}
	}
}

func (b *bundle) crashMarkers() {
	out, err := os.OpenFile(filepath.Join(b.dir, "13-crash-markers.log"), os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return
	}
	defer func() { _ = out.Close() }()
	var sources []string
	for _, n := range []string{"06-service-journal.log", "07-kernel-journal.log"} {
		sources = append(sources, filepath.Join(b.dir, n))
	}
	tails, _ := filepath.Glob(filepath.Join(b.dir, "12-app-log-tails", "*"))
	sources = append(sources, tails...)
	for _, src := range sources {
		f, err := os.Open(src)
		if err != nil {
			continue
		}
		_ = FilterCrashMarkers(f, out)
		_ = f.Close()
	}
}

func (b *bundle) coredumps() {
	if !execx.Exists("coredumpctl") {
		b.write("14-coredumps-list.txt", "coredumpctl is not installed\n")
		return
	}
	b.capture("14-coredumps-list.txt", false, "coredumpctl", "--since", b.opts.Since, "list", b.cfg.BinaryName, "--no-pager")
	b.capture("15-coredumps-info.txt", false, "coredumpctl", "--since", b.opts.Since, "info", b.cfg.BinaryName, "--no-pager")
}

func (b *bundle) binary(s metrics.Sample) {
	var sb strings.Builder
	exe := ""
	if s.Service.MainPID > 0 {
		exe, _ = os.Readlink(filepath.Join(metrics.DefaultProcRoot, strconv.Itoa(s.Service.MainPID), "exe"))
	}
	if exe == "" {
		exe = b.cfg.BinaryPath()
	}
	fmt.Fprintf(&sb, "executable=%s\n", exe)
	if st, err := os.Stat(exe); err == nil {
		fmt.Fprintf(&sb, "size=%d\nmodified=%s\nmode=%s\n", st.Size(), st.ModTime().UTC().Format(time.RFC3339), st.Mode())
		if sum, err := backup.SHA256File(exe); err == nil {
			fmt.Fprintf(&sb, "sha256=%s\n", sum)
		}
		if goBin := b.cfg.GoBinary(); fsx.Exists(goBin) {
			if out, err := execx.Output(goBin, "version", "-m", exe); err == nil {
				fmt.Fprintf(&sb, "\n%s\n", out)
			}
		}
	} else {
		fmt.Fprintf(&sb, "[error] %s\n", err)
	}
	b.write("16-binary.txt", sb.String())
}

func (b *bundle) oomAndBoots() {
	var sb strings.Builder
	for _, c := range []struct {
		title string
		cmd   string
		args  []string
	}{
		{"boot history", "journalctl", []string{"--list-boots", "--no-pager"}},
		{"systemd-oomd", "systemctl", []string{"status", "systemd-oomd", "--full", "--no-pager"}},
	} {
		out, err := execx.Output(c.cmd, c.args...)
		if err != nil {
			out += "\n[error] " + err.Error()
		}
		fmt.Fprintf(&sb, "== %s ==\n%s\n\n", c.title, out)
	}
	if execx.Exists("oomctl") {
		out, _ := execx.Output("oomctl")
		fmt.Fprintf(&sb, "== oomctl ==\n%s\n", out)
	}
	b.write("17-oom-and-boots.txt", sb.String())
}

func (b *bundle) nodeRPC(ctx context.Context, url string) {
	c := node.New(url)
	doc := map[string]any{"url": url, "collected": time.Now().UTC().Format(time.RFC3339)}
	call := func(key string, fn func() (any, error)) {
		v, err := fn()
		if err != nil {
			doc[key] = map[string]string{"error": err.Error()}
			return
		}
		doc[key] = v
	}
	call("syncInfo", func() (any, error) { return c.SyncInfo(ctx) })
	call("processInfo", func() (any, error) { return c.ProcessInfo(ctx) })
	call("osInfo", func() (any, error) { return c.OsInfo(ctx) })
	call("frontierMomentum", func() (any, error) { return c.FrontierMomentum(ctx) })
	call("networkInfo", func() (any, error) {
		n, err := c.NetworkInfo(ctx)
		if err != nil {
			return nil, err
		}
		raw, err := json.Marshal(n)
		if err != nil {
			return nil, err
		}
		red, err := RedactPeers(raw)
		if err != nil {
			return nil, err
		}
		return json.RawMessage(red), nil
	})
	data, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		data = []byte(`{"error":"` + err.Error() + `"}`)
	}
	b.write("18-node-rpc.json", string(data)+"\n")
}

func (b *bundle) nomctl(s metrics.Sample) {
	var sb strings.Builder
	fmt.Fprintf(&sb, "version=%s\nconfig=%s\n\n", b.opts.Version, b.cfg.Redacted())
	timers, err := execx.Output("systemctl", "list-timers", backup.TimerName+".timer", "--all", "--no-pager")
	if err != nil {
		timers += "\n[error] " + err.Error()
	}
	fmt.Fprintf(&sb, "== backup timer ==\n%s\n\n", timers)
	fmt.Fprintf(&sb, "== last 200 lines of %s ==\n", b.cfg.LogFile)
	if data, err := os.ReadFile(b.cfg.LogFile); err == nil {
		lines := strings.Split(strings.TrimRight(string(data), "\n"), "\n")
		if len(lines) > 200 {
			lines = lines[len(lines)-200:]
		}
		sb.WriteString(Redact(strings.Join(lines, "\n")) + "\n")
	} else {
		fmt.Fprintf(&sb, "[error] %s\n", err)
	}
	_ = s
	b.write("19-nomctl.txt", sb.String())
}

// writeArchive tars dir into dir.tar.gz (entries prefixed with the base name).
func writeArchive(dir string) (string, error) {
	archive := strings.TrimSuffix(dir, "/") + ".tar.gz"
	f, err := os.OpenFile(archive, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return "", err
	}
	gz := gzip.NewWriter(f)
	tw := tar.NewWriter(gz)
	base := filepath.Base(dir)
	err = filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(dir, path)
		if err != nil {
			return err
		}
		name := filepath.ToSlash(filepath.Join(base, rel))
		hdr, err := tar.FileInfoHeader(info, "")
		if err != nil {
			return err
		}
		hdr.Name = name
		if info.IsDir() {
			hdr.Name += "/"
		}
		if err := tw.WriteHeader(hdr); err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return nil
		}
		src, err := os.Open(path)
		if err != nil {
			return err
		}
		defer func() { _ = src.Close() }()
		_, err = io.Copy(tw, src)
		return err
	})
	if err != nil {
		_ = tw.Close()
		_ = gz.Close()
		_ = f.Close()
		_ = os.Remove(archive)
		return "", err
	}
	if err := tw.Close(); err != nil {
		return "", err
	}
	if err := gz.Close(); err != nil {
		return "", err
	}
	if err := f.Close(); err != nil {
		return "", err
	}
	return archive, nil
}
```
Remove the `_ = s` placeholder by dropping the unused parameter from `nomctl()`.

- [ ] **Step 4: Run tests and lint**

Run: `GOWORK=off go test ./internal/support/ && GOWORK=off golangci-lint run ./...`
Expected: PASS, 0 issues. On macOS the `/proc` reads simply record "not currently available", which the test tolerates.

- [ ] **Step 5: Commit**

```bash
git add internal/support && git commit -m "Add support bundle collection and archive"
```

---

### Task 8: `--watch` and the `support-bundle` command

**Files:**
- Create: `internal/support/watch.go`, `cmd/support.go`
- Test: `internal/support/watch_test.go`
- Modify: `cmd/status_test.go` (add `"support-bundle"` to the list)

**Interfaces:**
- Consumes: `metrics.Sampler`, `metrics.Format`, `execx.New(...).Start`-style child for the journal follower (add `func (c *Cmd) StartToFile(path string) (*exec.Cmd, error)` to execx).
- Produces:
  ```go
  type WatchOptions struct { Poll, Timeout time.Duration; Out string }   // Out = bundle dir
  type restartDetector struct{ initialRestarts, initialPID int; seenDown bool }
  func (d *restartDetector) restarted(props metrics.ServiceSample) (bool, string)   // pure, tested
  func Watch(ctx context.Context, cfg config.Config, opts WatchOptions) error        // returns when restart/timeout/ctx done
  ```

- [ ] **Step 1: Write the failing test**

`internal/support/watch_test.go`:
```go
package support

import (
	"testing"

	"github.com/0x3639/nomctl/internal/metrics"
)

func TestRestartDetector(t *testing.T) {
	d := &restartDetector{initialRestarts: 2, initialPID: 100}
	if ok, _ := d.restarted(metrics.ServiceSample{NRestarts: 2, MainPID: 100}); ok {
		t.Error("unchanged state is not a restart")
	}
	if ok, why := d.restarted(metrics.ServiceSample{NRestarts: 3, MainPID: 100}); !ok || why == "" {
		t.Error("NRestarts growing is a restart")
	}
	d = &restartDetector{initialRestarts: 0, initialPID: 100}
	if ok, _ := d.restarted(metrics.ServiceSample{NRestarts: 0, MainPID: 0}); ok {
		t.Error("process down alone is not yet a restart")
	}
	if ok, _ := d.restarted(metrics.ServiceSample{NRestarts: 0, MainPID: 200}); !ok {
		t.Error("new pid after being down is a restart")
	}
	d = &restartDetector{initialRestarts: 0, initialPID: 100}
	if ok, _ := d.restarted(metrics.ServiceSample{NRestarts: 0, MainPID: 200}); ok {
		t.Error("pid change without seeing it down is ignored (matches the script)")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `GOWORK=off go test ./internal/support/ -run TestRestartDetector`
Expected: FAIL, undefined restartDetector.

- [ ] **Step 3: Implement watch.go, the execx helper and the command**

Add to `internal/execx/execx.go`:
```go
// StartToFile starts the command with stdout and stderr appended to path and
// returns the running process for the caller to stop and wait on.
func (c *Cmd) StartToFile(path string) (*exec.Cmd, error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return nil, err
	}
	cmd := c.build()
	cmd.Stdout = f
	cmd.Stderr = f
	if err := cmd.Start(); err != nil {
		_ = f.Close()
		return nil, &Error{Cmd: c.String(), Err: err}
	}
	go func() { _ = cmd.Wait(); _ = f.Close() }()
	return cmd, nil
}
```
Because the goroutine calls Wait, callers must only `Process.Signal`/`Kill` and then wait on a done channel; simplest is to have StartToFile return a `func() error` stop closure instead. Implement it that way:
```go
func (c *Cmd) StartToFile(path string) (stop func(), err error)
```
where stop sends SIGTERM, waits up to 2 s, then kills.

`internal/support/watch.go`:
```go
package support

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	"github.com/0x3639/nomctl/internal/config"
	"github.com/0x3639/nomctl/internal/execx"
	"github.com/0x3639/nomctl/internal/metrics"
)

// WatchOptions tunes the pre-collection watch.
type WatchOptions struct {
	Poll    time.Duration
	Timeout time.Duration
	Out     string
}

type restartDetector struct {
	initialRestarts int
	initialPID      int
	seenDown        bool
}

// restarted implements the script's rule: NRestarts grew, or the PID was
// seen at 0 and is now a different non-zero value.
func (d *restartDetector) restarted(s metrics.ServiceSample) (bool, string) {
	if s.NRestarts > d.initialRestarts {
		return true, fmt.Sprintf("systemd restart count changed: %d -> %d", d.initialRestarts, s.NRestarts)
	}
	if s.MainPID == 0 {
		d.seenDown = true
		return false, ""
	}
	if d.seenDown && s.MainPID != d.initialPID {
		return true, fmt.Sprintf("process restarted: %d -> %d", d.initialPID, s.MainPID)
	}
	return false, ""
}

// Watch samples the service every Poll into 01-runtime-watch.log and
// follows its journal into 00-live-journal.log until systemd restarts it,
// ctx is cancelled, or Timeout elapses. It then takes one final sample.
func Watch(ctx context.Context, cfg config.Config, opts WatchOptions) error {
	if err := os.MkdirAll(opts.Out, 0o700); err != nil {
		return err
	}
	sampler := metrics.NewSampler(cfg)
	first := sampler.Take(ctx)
	det := &restartDetector{initialRestarts: first.Service.NRestarts, initialPID: first.Service.MainPID}

	stopJournal, err := execx.New("journalctl", "-fu", cfg.ServiceUnit(), "-o", "short-iso-precise", "--no-pager").
		StartToFile(filepath.Join(opts.Out, "00-live-journal.log"))
	if err != nil {
		slog.Warn("cannot follow journal: " + err.Error())
		stopJournal = func() {}
	}
	defer stopJournal()

	log, err := os.OpenFile(filepath.Join(opts.Out, "01-runtime-watch.log"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return err
	}
	defer func() { _ = log.Close() }()
	record := func(s metrics.Sample) {
		fmt.Fprintf(log, "===== %s =====\n%s\n", s.Taken.UTC().Format(time.RFC3339), metrics.Format(s))
	}
	record(first)

	slog.Info(fmt.Sprintf("Watching %s (pid %d, restart count %d); sampling every %s. Press Ctrl+C to stop and collect.", cfg.ServiceUnit(), first.Service.MainPID, first.Service.NRestarts, opts.Poll))
	start := time.Now()
	ticker := time.NewTicker(opts.Poll)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			slog.Info("Watch interrupted; collecting the bundle now.")
			return nil
		case <-ticker.C:
		}
		s := sampler.Take(ctx)
		record(s)
		if ok, why := det.restarted(s.Service); ok {
			slog.Info("Detected " + why)
			time.Sleep(2 * time.Second)
			record(sampler.Take(ctx))
			return nil
		}
		if opts.Timeout > 0 && time.Since(start) >= opts.Timeout {
			slog.Info("Watch timeout reached without observing a restart.")
			return nil
		}
	}
}
```
`cmd/support.go`:
```go
package cmd

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"time"

	"github.com/spf13/cobra"

	"github.com/0x3639/nomctl/internal/logx"
	"github.com/0x3639/nomctl/internal/support"
)

var (
	flagSupportOut     string
	flagSupportSince   string
	flagSupportWatch   bool
	flagSupportPoll    time.Duration
	flagSupportTimeout time.Duration
)

var supportCmd = &cobra.Command{
	Use:   "support-bundle",
	Short: "Collect diagnostics into a shareable .tar.gz",
	Long: `Collects systemd state, journals, process and cgroup details, host
resources, node log tails, a node RPC snapshot (peer IPs redacted) and
nomctl's own state into a directory and a .tar.gz beside it. Read-only:
it never stops the node or touches its data. Config contents are never
collected. With --watch it first samples the live process until systemd
restarts it, so the bundle contains the moments before a crash.`,
	Args:        cobra.NoArgs,
	Annotations: diagnostic(),
	RunE: func(cmd *cobra.Command, _ []string) error {
		out := flagSupportOut
		if out == "" {
			out = support.DefaultOutputDir(time.Now())
		}
		ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
		defer stop()
		if flagSupportWatch {
			if flagSupportPoll < time.Second {
				return fmt.Errorf("--poll must be at least 1s")
			}
			if err := support.Watch(ctx, cfg, support.WatchOptions{Poll: flagSupportPoll, Timeout: flagSupportTimeout, Out: out}); err != nil {
				return err
			}
			stop()
			ctx = context.Background()
		}
		res, err := support.Collect(ctx, cfg, support.Options{OutputDir: out, Since: flagSupportSince, Version: versionString()})
		if err != nil {
			return err
		}
		logx.Success("Support bundle created")
		fmt.Fprintf(cmd.OutOrStdout(), "\nDiagnostics directory: %s\nBundle:                %s\nCrash markers:         %s\n\n", res.Dir, res.Archive, res.Markers)
		fmt.Fprintln(cmd.OutOrStdout(), "The bundle may contain node addresses, peer counts, paths and service arguments.\nReview it before sharing publicly. Configuration contents are not collected.")
		return nil
	},
}

func init() {
	supportCmd.Flags().StringVar(&flagSupportOut, "output", "", "diagnostics directory (default /root/nomctl-support-<host>-<time>)")
	supportCmd.Flags().StringVar(&flagSupportSince, "since", "12 hours ago", "journal window passed to journalctl --since")
	supportCmd.Flags().BoolVar(&flagSupportWatch, "watch", false, "sample the live process until systemd restarts it, then collect")
	supportCmd.Flags().DurationVar(&flagSupportPoll, "poll", 10*time.Second, "watch sampling interval")
	supportCmd.Flags().DurationVar(&flagSupportTimeout, "timeout", 0, "stop watching after this long (0 = no timeout)")
	rootCmd.AddCommand(supportCmd)
}
```
Add `"support-bundle"` to `TestDiagnosticCommandsSkipPreflight`.

- [ ] **Step 4: Run tests and lint**

Run: `GOWORK=off go test ./... && GOWORK=off golangci-lint run ./...`
Expected: PASS, 0 issues.

- [ ] **Step 5: Commit**

```bash
git add cmd internal/support internal/execx && git commit -m "Add nomctl support-bundle with --watch"
```

---

### Task 9: Menu entries

**Files:**
- Modify: `internal/tui/menu.go` (MenuOptions, Dispatch), `internal/tui/menu_test.go`

**Interfaces:**
- Consumes: `Top(cfg, 2*time.Second)`, `support.Collect`, `support.DefaultOutputDir`.
- Produces: `ActionStatus Action = "status"`, `ActionSupport Action = "support"`.

- [ ] **Step 1: Update the failing test**

In `menu_test.go` change `TestMenuOptions` expectations: 12 options; `opts[5].Value == "status"`, `opts[6].Value == "support"`, `opts[11].Value == "exit"`.

- [ ] **Step 2: Run test to verify it fails**

Run: `GOWORK=off go test ./internal/tui/ -run TestMenuOptions`
Expected: FAIL (10 options).

- [ ] **Step 3: Implement**

Add constants `ActionStatus Action = "status"` and `ActionSupport Action = "support"`. In `MenuOptions` insert after the monitor entry:
```go
		{ActionStatus, "Live node dashboard (sync, CPU, memory)"},
		{ActionSupport, "Create a support bundle for troubleshooting"},
```
In `Dispatch`:
```go
	case ActionStatus:
		return Top(*cfg, 2*time.Second)
	case ActionSupport:
		return SupportBundle(*cfg)
```
Add:
```go
// SupportBundle collects a bundle with defaults and prints where it went.
func SupportBundle(cfg config.Config) error {
	res, err := support.Collect(context.Background(), cfg, support.Options{Version: Version})
	if err != nil {
		return err
	}
	ui.Success("Support bundle created")
	fmt.Fprintf(os.Stderr, "\nDiagnostics directory: %s\nBundle:                %s\nCrash markers:         %s\n\nReview the bundle before sharing; configuration contents are never collected.\n", res.Dir, res.Archive, res.Markers)
	return nil
}
```
`Version` is a new exported package variable in `internal/tui` (`var Version = "dev"`) that `cmd/root.go` sets in `setup()` via `tui.Version = versionString()`; this avoids `internal/tui` importing `cmd`.

- [ ] **Step 4: Run tests and lint**

Run: `GOWORK=off go test ./... && GOWORK=off golangci-lint run ./...`
Expected: PASS, 0 issues.

- [ ] **Step 5: Commit**

```bash
git add internal/tui cmd && git commit -m "Add status and support entries to the menu"
```

---

### Task 10: README troubleshooting section and release notes

**Files:**
- Modify: `README.md` (Commands table, new Troubleshooting section, differences list)

- [ ] **Step 1: Commands table**

Add rows after `nomctl analytics install`:
```
| `nomctl status [--json]` | One-screen summary: service state, sync state and heights, momentums/s and ETA, peers, process CPU/memory/open files, host load/memory/disk/pressure |
| `nomctl top [--interval 2s]` | The same, refreshed live; `q` to quit |
| `nomctl support-bundle [--watch] [--since "12 hours ago"] [--output DIR] [--poll 10s] [--timeout 0]` | Collect diagnostics into `/root/nomctl-support-<host>-<time>` and a `.tar.gz` beside it |
```

- [ ] **Step 2: Troubleshooting section** (insert before "## Building")

```markdown
## Troubleshooting

### First look

```bash
sudo nomctl status
```

```
Service   go-zenon active (running), pid 1234, 0 restarts, up 3d 4h
Node      znnd v0.0.7 (a1b2c3d), syncing 1,234,567 / 2,000,000 (61.7%), 5.2 mom/s, ETA 1d 16h
Peers     14 connected
Frontier  height 1,234,567, 3s ago
Process   cpu 42.0%, rss 1.9 GiB, threads 38, open files 412 / 32768
Host      load 1.20 0.90 0.80, mem 3.1 GiB / 7.8 GiB available, /root/.znn 210.0 GiB free
Pressure  cpu 2.1%, io 15.4%, mem 0.0%
```

- **Service**: `active (running)` with a restart count that is not climbing is healthy. A growing count means a crash loop; collect a bundle with `--watch`.
- **Node**: `syncing` with a rate above zero means progress. `synced` with a frontier older than two minutes is shown as `[STALLED]`; restart the service. `not enough peers` usually means port 35995/TCP is blocked inbound or the host has no outbound connectivity.
- **`node rpc unreachable`**: the process is not up, or its HTTP RPC (port 35997) is disabled in `config.json`. The rest of the output is still valid.
- **Process**: open files near the 32768 limit or memory close to the host total predict the two most common crashes.
- **Host**: less than 15 GB free on the data directory stops backups and will eventually stop the node; IO pressure above ~50% on a syncing node means the disk is the bottleneck.

`sudo nomctl top` shows the same values live, with a sync progress bar and a sparkline of the sync rate. `sudo nomctl status --json` gives the raw sample for scripts.

### Common signatures

| What you see | Likely cause | What to do |
|---|---|---|
| `too many open files` in the journal | file descriptor limit reached | the unit sets `LimitNOFILE=32768`; check `open files` in `status`; if the unit was edited, `sudo nomctl deploy` rewrites it |
| `out of memory`, `oom-kill`, `Main process exited, code=killed, status=9/KILL` in the kernel journal | host RAM exhausted | 4 GiB is the minimum; check `MemoryPeak` in the bundle's `04-service-properties.txt` |
| `no space left on device` | data or backup filesystem full | `df -h`; prune backups (`nomctl backup --max-backups N`) or grow the disk |
| `not enough peers` for more than a few minutes | firewall | allow 35995/TCP inbound; confirm outbound Internet |
| `synced` but frontier age keeps growing | stalled node | `sudo nomctl restart`; if it recurs, collect a bundle |
| `leveldb`/`corrupt` errors after an unclean shutdown | damaged chain database | `sudo nomctl restore` from a backup, or `sudo nomctl resync` |
| restarts climbing, nothing obvious in the journal | crash loop | `sudo nomctl support-bundle --watch` and share the bundle |

### Support bundle

```bash
sudo nomctl support-bundle                  # snapshot now
sudo nomctl support-bundle --watch          # wait for the next crash, then snapshot
sudo nomctl support-bundle --since "2 days ago" --output /root/bundle-1
```

The bundle contains the service status, unit and properties (secrets redacted), the service, kernel and system-warning journals for the window, `/proc` and cgroup details of the live process, host memory, disk, pressure and process list, the newest node log files (last 4 MiB of up to 30 files), a grep of crash markers across all of it, coredump information, the node binary's hash, a node RPC snapshot with peer IPs redacted, and nomctl's own version, effective configuration (password redacted) and log tail.

It never contains `config.json`, the wallet directory, or any file under the data directory other than `log/`. It may contain the host name, node addresses, file paths and peer counts; skim `13-crash-markers.log` and `06-service-journal.log` before sharing publicly. The directory is created `0700` and the archive `0600`.

With `--watch`, nomctl samples the process every `--poll` seconds and follows the journal until systemd restarts the unit (or `--timeout`, or Ctrl+C), writes those samples to `01-runtime-watch.log` and `00-live-journal.log`, then collects the rest. It is read-only and safe to leave running on a production node.
```

- [ ] **Step 3: Differences list**

Append:
```
- New in v0.2.0: `status`, `top` and `support-bundle` (the latter a port of the standalone collect-znnd-crash.sh script, with node RPC and nomctl state added, `--service`/`--data` replaced by `NOMCTL_SERVICE_NAME`/`NOMCTL_ZNN_DIR`, and the default output directory under `/root`).
```

- [ ] **Step 4: Verify and commit**

Run: `GOWORK=off make lint test cross`
Expected: all pass, two binaries.

```bash
git add README.md && git commit -m "Document status, top and support-bundle"
```

---

## Self-review

- Spec coverage: commands (Tasks 4, 5, 8), menu (9), node client (1), sampler and derived values (2, 3), top layout with progress bar and sparkline (5), bundle files 00–20 (7, 8), redaction and never-config rule (6, 7), watch rules (8), error handling (never fatal on node down: 3, 7), testing list (each task), README (10). Pre-flight exemption and no-lock rule: Task 4 annotation, and none of the new commands call `withLock`.
- Type consistency: `metrics.Sample` fields used in Tasks 5, 7 and 8 match Task 3; `ServiceSample.ControlGroupForCgroup()` is defined in Task 3 and used in Task 7; `support.Options.NodeURL` defined in Task 7 and used by its test; `execx.Cmd.StartToFile` returns `(func(), error)` and is used that way in Task 8.
- Placeholders: none remaining; the `_ = s` note in Task 7 is resolved by removing the parameter.
