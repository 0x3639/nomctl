package node

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

func rpcServer(t *testing.T) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		var req struct {
			ID     any    `json:"id"`
			Method string `json:"method"`
		}
		if err := json.Unmarshal(raw, &req); err != nil {
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
		case "embedded.pillar.getByName":
			var full struct {
				Params []string `json:"params"`
			}
			_ = json.Unmarshal(raw, &full)
			if len(full.Params) == 1 && full.Params[0] == "MyPillar" {
				result = `{"name":"MyPillar","rank":11,"ownerAddress":"z1own","producerAddress":"z1prod","currentStats":{"producedMomentums":118,"expectedMomentums":121},"weight":"1234"}`
			} else {
				result = `null`
			}
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
	if err != nil || m.Height != 100 || m.Timestamp != 1700000000 || m.Time().Unix() != 1700000000 {
		t.Fatalf("Frontier = %+v, %v", m, err)
	}
}

func TestPillarByName(t *testing.T) {
	srv := rpcServer(t)
	defer srv.Close()
	c := New(srv.URL)
	p, err := c.PillarByName(context.Background(), "MyPillar")
	if err != nil || p == nil || p.Rank != 11 || p.CurrentStats == nil || p.CurrentStats.ProducedMomentums != 118 || p.CurrentStats.ExpectedMomentums != 121 || p.Weight != "1234" {
		t.Fatalf("pillar = %+v, %v", p, err)
	}
	p, err = c.PillarByName(context.Background(), "Nobody")
	if err != nil || p != nil {
		t.Fatalf("unknown pillar should be nil, nil: %+v %v", p, err)
	}
	c.PillarName = "MyPillar"
	snap := c.Snapshot(context.Background())
	if snap.Err != nil || snap.Pillar == nil || snap.Pillar.Name != "MyPillar" {
		t.Fatalf("snapshot pillar: %+v", snap)
	}
	c.PillarName = "Nobody"
	snap = c.Snapshot(context.Background())
	if snap.Err != nil || snap.Pillar != nil || snap.PillarErr != nil {
		t.Fatalf("snapshot unknown pillar: %+v", snap)
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
	if err := c.call(context.Background(), "nope", &out); err == nil {
		t.Error("HTTP 404 must be an error")
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
