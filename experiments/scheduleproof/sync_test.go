package scheduleproof

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/zenon-network/go-zenon/chain/genesis"
	g "github.com/zenon-network/go-zenon/chain/genesis/mock"
	"github.com/zenon-network/go-zenon/chain/nom"
	"github.com/zenon-network/go-zenon/rpc/api"
	"github.com/zenon-network/go-zenon/vm/embedded/definition"
	"github.com/zenon-network/go-zenon/zenon/mock"
)

type testSource struct {
	z             mock.MockZenon
	behind        uint64
	broken        bool
	short         bool
	detailsStarts []uint64
	genesisReads  int
	afterDetails  func()
}

func (s *testSource) Name() string { return "controlled-ledger" }
func (s *testSource) Frontier(ctx context.Context) (*nom.Momentum, error) {
	if s.broken {
		return nil, fmt.Errorf("source interrupted")
	}
	if s.behind > 0 {
		return s.Header(ctx, s.behind)
	}
	return s.z.Chain().GetFrontierMomentumStore().GetFrontierMomentum()
}
func (s *testSource) Header(ctx context.Context, h uint64) (*nom.Momentum, error) {
	if h == 1 {
		s.genesisReads++
	}
	if s.broken {
		return nil, fmt.Errorf("source interrupted")
	}
	return s.z.Chain().GetFrontierMomentumStore().GetMomentumByHeight(h)
}
func (s *testSource) Details(ctx context.Context, h, n uint64) ([]json.RawMessage, error) {
	s.detailsStarts = append(s.detailsStarts, h)
	response, err := api.NewLedgerApi(s.z).GetDetailedMomentumsByHeight(h, n)
	if err != nil {
		return nil, err
	}
	result := make([]json.RawMessage, len(response.List))
	for i, v := range response.List {
		result[i], err = json.Marshal(v)
		if err != nil {
			return nil, err
		}
	}
	if s.short && len(result) > 0 {
		result = result[:len(result)-1]
	}
	if s.afterDetails != nil {
		s.afterDetails()
	}
	return result, nil
}
func openTestReplica(t *testing.T, path string) *Replica {
	t.Helper()
	r, err := OpenReplica(path, genesis.NewGenesis(g.EmbeddedGenesis))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(r.Close)
	return r
}
func testSyncer(t *testing.T, z mock.MockZenon, r *Replica) (*Syncer, *testSource) {
	t.Helper()
	src := &testSource{z: z}
	s := NewSyncer(r, src)
	s.Batch = 100
	s.Now = func() time.Time {
		m, _ := z.Chain().GetFrontierMomentumStore().GetFrontierMomentum()
		return m.Timestamp.Add(time.Second)
	}
	return s, src
}
func stepOK(t *testing.T, s *Syncer) {
	t.Helper()
	if err := s.Step(context.Background()); err != nil {
		t.Fatal(err)
	}
}
func savedStatus(t *testing.T, r *Replica) Status {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(r.root, "status.json"))
	if err != nil {
		t.Fatal(err)
	}
	var s Status
	if err = json.Unmarshal(raw, &s); err != nil {
		t.Fatal(err)
	}
	return s
}

func TestSyncResumesAndInvalidatesReadyOnRestart(t *testing.T) {
	z := source(t)
	z.InsertMomentumsTo(91)
	path := t.TempDir()
	r := openTestReplica(t, path)
	s, _ := testSyncer(t, z, r)
	s.Batch = 30
	stepOK(t, s)
	if r.Mirror.Frontier().Height != 31 || s.Status.State != "catching-up" {
		t.Fatal("first bounded batch")
	}
	r.Close()
	r = openTestReplica(t, path)
	if savedStatus(t, r).State != "starting" {
		t.Fatal("reused old qualification")
	}
	s, src := testSyncer(t, z, r)
	stepOK(t, s)
	if src.detailsStarts[0] != 32 || s.Status.State != "ready" {
		t.Fatal("did not resume from committed state")
	}
	if s.Status.Compared == 0 || s.Status.Compared != s.Status.Matched {
		t.Fatal("schedule comparison missing")
	}
	if len(s.Status.Schedules) != 2 {
		t.Fatal("missing current/next schedules")
	}
	before := s.Status.Schedules
	r.Close()
	r = openTestReplica(t, path)
	if len(savedStatus(t, r).Schedules) != 0 {
		t.Fatal("published schedules before revalidation")
	}
	s, src = testSyncer(t, z, r)
	stepOK(t, s)
	if len(src.detailsStarts) != 0 || !reflect.DeepEqual(before, s.Status.Schedules) {
		t.Fatal("restart replayed history or changed schedule")
	}
	if err := s.Stop(); err != nil {
		t.Fatal(err)
	}
	if savedStatus(t, r).State != "stopped" || len(savedStatus(t, r).Schedules) != 0 {
		t.Fatal("kept live schedules after stop")
	}
}
func TestSyncAutomaticForkReconciliation(t *testing.T) {
	z := source(t)
	z.InsertMomentumsTo(45)
	anchor := z.Chain().GetFrontierMomentumStore().Identifier()
	settle(t, z, call(g.User1.Address, definition.DelegateMethodName, g.Pillar2Name), nil)
	z.InsertMomentumsTo(91)
	r := openTestReplica(t, t.TempDir())
	s, _ := testSyncer(t, z, r)
	stepOK(t, s)
	oldProof := s.Status.Schedules[0].Proof
	lock := z.Chain().AcquireInsert("controlled fork")
	err := z.Chain().RollbackTo(lock, anchor)
	lock.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	settle(t, z, call(g.User1.Address, definition.DelegateMethodName, g.Pillar3Name), nil)
	z.InsertMomentumsTo(91)
	stepOK(t, s)
	if s.Status.State != "ready" || s.Status.Forks != 1 || s.Status.Rewound != 46 || s.Status.Schedules[0].Proof.Hash == oldProof.Hash {
		t.Fatal("fork not reconciled")
	}
	checkRound(t, z, r.Mirror, 3)
}
func TestSourceFailureStalenessAndBounds(t *testing.T) {
	z := source(t)
	z.InsertMomentumsTo(91)
	r := openTestReplica(t, t.TempDir())
	s, src := testSyncer(t, z, r)
	stepOK(t, s)
	tip := r.Mirror.Frontier()
	src.broken = true
	if err := s.Step(context.Background()); err == nil {
		t.Fatal("accepted source failure")
	}
	if len(savedStatus(t, r).Schedules) != 0 {
		t.Fatal("kept ready result after failure")
	}
	src.broken = false
	stepOK(t, s)
	src.behind = 30
	stepOK(t, s)
	if s.Status.State != "source-behind" || r.Mirror.Frontier() != tip {
		t.Fatal("rewound to lagging source")
	}
	src.behind = 0
	now := s.Now()
	s.Now = func() time.Time { return now.Add(time.Hour) }
	stepOK(t, s)
	if s.Status.State != "stale" || len(s.Status.Schedules) != 0 {
		t.Fatal("served stale schedules")
	}
	s.Now = func() time.Time { return now.Add(-time.Hour) }
	stepOK(t, s)
	if s.Status.State != "stale" {
		t.Fatal("accepted future source timestamp")
	}
}
func TestIncompleteBatchAndCancelledSyncResume(t *testing.T) {
	z := source(t)
	z.InsertMomentumsTo(61)
	r := openTestReplica(t, t.TempDir())
	s, src := testSyncer(t, z, r)
	src.short = true
	if err := s.Step(context.Background()); err == nil || r.Mirror.Frontier().Height != 1 {
		t.Fatal("accepted short batch")
	}
	src.short = false
	ctx, cancel := context.WithCancel(context.Background())
	src.afterDetails = cancel
	if err := s.Step(ctx); err == nil || r.Mirror.Frontier().Height != 1 {
		t.Fatal("ignored cancellation")
	}
	src.afterDetails = nil
	stepOK(t, s)
	if s.Status.State != "ready" {
		t.Fatal("did not recover")
	}
}
func TestForkDepthLimitDoesNotDiscardState(t *testing.T) {
	z := source(t)
	z.InsertMomentumsTo(45)
	anchor := z.Chain().GetFrontierMomentumStore().Identifier()
	settle(t, z, call(g.User1.Address, definition.DelegateMethodName, g.Pillar2Name), nil)
	z.InsertMomentumsTo(91)
	r := openTestReplica(t, t.TempDir())
	s, _ := testSyncer(t, z, r)
	stepOK(t, s)
	tip := r.Mirror.Frontier()
	lock := z.Chain().AcquireInsert("controlled fork")
	err := z.Chain().RollbackTo(lock, anchor)
	lock.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	settle(t, z, call(g.User1.Address, definition.DelegateMethodName, g.Pillar3Name), nil)
	z.InsertMomentumsTo(91)
	s.MaxReorg = 5
	if err = s.Step(context.Background()); err == nil || r.Mirror.Frontier() != tip || len(s.Status.Schedules) != 0 {
		t.Fatal("unbounded fork handling")
	}
}
func TestReplicaOwnershipLockAndIdentity(t *testing.T) {
	g := genesis.NewGenesis(g.EmbeddedGenesis)
	path := t.TempDir()
	r, err := OpenReplica(path, g)
	if err != nil {
		t.Fatal(err)
	}
	if another, e := OpenReplica(path, g); e == nil {
		another.Close()
		t.Fatal("opened concurrent writer")
	}
	r.Close()
	raw, err := os.ReadFile(filepath.Join(path, "replica.json"))
	if err != nil {
		t.Fatal(err)
	}
	var id replicaIdentity
	json.Unmarshal(raw, &id)
	id.Engine = "unsupported"
	if err = atomicJSON(filepath.Join(path, "replica.json"), id); err != nil {
		t.Fatal(err)
	}
	if another, e := OpenReplica(path, g); e == nil {
		another.Close()
		t.Fatal("accepted engine mismatch")
	}
	other := t.TempDir()
	os.WriteFile(filepath.Join(other, "config.json"), []byte("unrelated"), 0600)
	if _, e := OpenReplica(other, g); e == nil {
		t.Fatal("adopted unrelated node directory")
	}
	link := filepath.Join(t.TempDir(), "alias")
	if err = os.Symlink(other, link); err != nil {
		t.Fatal(err)
	}
	if _, e := OpenReplica(link, g); e == nil {
		t.Fatal("followed data symlink")
	}
}

func TestReadOnlyRPCTransportAndLimits(t *testing.T) {
	allowed := map[string]bool{"ledger.getFrontierMomentum": true, "ledger.getMomentumsByHeight": true, "ledger.getDetailedMomentumsByHeight": true}
	seen := map[string]bool{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var q struct {
			ID     uint64 `json:"id"`
			Method string `json:"method"`
		}
		json.NewDecoder(r.Body).Decode(&q)
		if !allowed[q.Method] {
			t.Errorf("unexpected method %s", q.Method)
		}
		seen[q.Method] = true
		var result interface{} = map[string]interface{}{"list": []interface{}{map[string]interface{}{"height": 1}}}
		if q.Method == "ledger.getFrontierMomentum" {
			result = map[string]interface{}{"height": 1}
		}
		json.NewEncoder(w).Encode(map[string]interface{}{"jsonrpc": "2.0", "id": q.ID, "result": result})
	}))
	defer server.Close()
	src, err := NewRPCSource(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = src.Frontier(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err = src.Header(context.Background(), 1); err != nil {
		t.Fatal(err)
	}
	if _, err = src.Details(context.Background(), 1, 1); err != nil {
		t.Fatal(err)
	}
	if len(seen) != 3 || src.Calls != 3 || src.Bytes == 0 {
		t.Fatal("missing RPC measurements")
	}
	if _, err = src.Details(context.Background(), 1, 101); err == nil {
		t.Fatal("unbounded request")
	}
}

func TestCrashHelper(t *testing.T) {
	if os.Getenv("SCHEDULEPROOF_CRASH_HELPER") == "" {
		t.Skip("subprocess helper")
	}
	raw, err := os.ReadFile("testdata/mainnet-2-61.json")
	if err != nil {
		t.Fatal(err)
	}
	var fixture struct {
		Recent struct {
			List []json.RawMessage `json:"list"`
		} `json:"recent"`
	}
	if err = json.Unmarshal(raw, &fixture); err != nil {
		t.Fatal(err)
	}
	g, err := genesis.MakeEmbeddedGenesisConfig()
	if err != nil {
		t.Fatal(err)
	}
	r, err := OpenReplica(os.Getenv("SCHEDULEPROOF_CRASH_DIR"), g)
	if err != nil {
		t.Fatal(err)
	}
	for i, item := range fixture.Recent.List {
		pause := func() { fmt.Println("CRASH_WINDOW"); var one [1]byte; os.Stdin.Read(one[:]) }
		if i == 39 && os.Getenv("SCHEDULEPROOF_CRASH_HELPER") == "before" {
			r.Mirror.manager.beforeWrite = pause
		}
		if err = r.Mirror.ApplyJSON(item); err != nil {
			t.Fatal(err)
		}
		if i == 39 && os.Getenv("SCHEDULEPROOF_CRASH_HELPER") == "after" {
			pause()
		}
	}
	t.Fatal("parent did not kill helper")
}
func TestKilledProcessResumesAtomicCommit(t *testing.T) {
	for _, when := range []string{"before", "after"} {
		t.Run(when, func(t *testing.T) {
			dir := t.TempDir()
			cmd := exec.Command(os.Args[0], "-test.run=^TestCrashHelper$")
			cmd.Env = append(os.Environ(), "SCHEDULEPROOF_CRASH_HELPER="+when, "SCHEDULEPROOF_CRASH_DIR="+dir)
			stdin, err := cmd.StdinPipe()
			if err != nil {
				t.Fatal(err)
			}
			defer stdin.Close()
			out, err := cmd.StdoutPipe()
			if err != nil {
				t.Fatal(err)
			}
			cmd.Stderr = os.Stderr
			if err = cmd.Start(); err != nil {
				t.Fatal(err)
			}
			defer func() { cmd.Process.Kill(); cmd.Wait() }()
			ready := make(chan bool, 1)
			go func() {
				scan := bufio.NewScanner(out)
				for scan.Scan() {
					if scan.Text() == "CRASH_WINDOW" {
						ready <- true
						return
					}
				}
				ready <- false
			}()
			select {
			case ok := <-ready:
				if !ok {
					t.Fatal("helper failed before crash window")
				}
			case <-time.After(30 * time.Second):
				t.Fatal("helper timeout")
			}
			if err = cmd.Process.Kill(); err != nil {
				t.Fatal(err)
			}
			cmd.Wait()
			g, err := genesis.MakeEmbeddedGenesisConfig()
			if err != nil {
				t.Fatal(err)
			}
			r, err := OpenReplica(dir, g)
			if err != nil {
				t.Fatal(err)
			}
			defer r.Close()
			expected := uint64(40)
			if when == "after" {
				expected = 41
			}
			if r.Mirror.Frontier().Height != expected {
				t.Fatalf("resume height %d, want %d", r.Mirror.Frontier().Height, expected)
			}
			raw, _ := os.ReadFile("testdata/mainnet-2-61.json")
			var fixture struct {
				Recent struct {
					List []json.RawMessage `json:"list"`
				} `json:"recent"`
			}
			json.Unmarshal(raw, &fixture)
			for _, item := range fixture.Recent.List[expected-1:] {
				if err = r.Mirror.ApplyJSON(item); err != nil {
					t.Fatal(err)
				}
			}
			if r.Mirror.Frontier().Height != 61 {
				t.Fatal("failed to resume after kill")
			}
		})
	}
}

func TestSourceChangesDuringBatchNeverPublishesReady(t *testing.T) {
	z := source(t)
	z.InsertMomentumsTo(45)
	anchor := z.Chain().GetFrontierMomentumStore().Identifier()
	settle(t, z, call(g.User1.Address, definition.DelegateMethodName, g.Pillar2Name), nil)
	z.InsertMomentumsTo(91)
	r := openTestReplica(t, t.TempDir())
	s, src := testSyncer(t, z, r)
	src.afterDetails = func() {
		lock := z.Chain().AcquireInsert("change while reading")
		err := z.Chain().RollbackTo(lock, anchor)
		lock.Unlock()
		if err != nil {
			t.Fatal(err)
		}
		settle(t, z, call(g.User1.Address, definition.DelegateMethodName, g.Pillar3Name), nil)
		z.InsertMomentumsTo(91)
	}
	if err := s.Step(context.Background()); err == nil || len(s.Status.Schedules) != 0 {
		t.Fatal("accepted mixed chain sample")
	}
	src.afterDetails = nil
	stepOK(t, s)
	if s.Status.State != "ready" || s.Status.Forks != 1 {
		t.Fatal("did not reconcile changed source")
	}
}

func TestGenesisDownloadIsNotRepeatedPerBatch(t *testing.T) {
	z := source(t)
	z.InsertMomentumsTo(61)
	r := openTestReplica(t, t.TempDir())
	s, src := testSyncer(t, z, r)
	s.Batch = 20
	stepOK(t, s)
	initialReads := src.genesisReads
	stepOK(t, s)
	stepOK(t, s)
	if src.genesisReads != initialReads || s.Status.State != "ready" {
		t.Fatal("genesis refetched or sync failed")
	}
}
