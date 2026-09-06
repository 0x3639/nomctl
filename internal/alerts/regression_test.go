package alerts

import (
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/0x3639/nomctl/internal/alertproto"
	"github.com/0x3639/nomctl/internal/metrics"
)

type roundTrip func(*http.Request) (*http.Response, error)

func (f roundTrip) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func response(code int, body string) *http.Response {
	return &http.Response{StatusCode: code, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header)}
}

// A recovery whose POST failed is retried on following healthy samples.
func TestFailedRecoveryIsRetriedByDaemon(t *testing.T) {
	relay := &fakeRelay{secret: []byte("secret")}
	d, now := newTestDaemon(t, relay, healthy, downSample, downSample, healthy, healthy, healthy)
	base := d.client.HTTP.Transport
	if base == nil {
		base = http.DefaultTransport
	}
	failed := false
	okAttempts := 0
	d.client.HTTP.Transport = roundTrip(func(r *http.Request) (*http.Response, error) {
		if r.URL.Path == "/v1/alert" {
			body, _ := io.ReadAll(r.Body)
			r.Body = io.NopCloser(strings.NewReader(string(body)))
			var a alertproto.AlertRequest
			_ = json.Unmarshal(body, &a)
			if a.Alert == "service_down" && a.State == alertproto.OK {
				okAttempts++
				if !failed {
					failed = true
					return response(503, `{"error":"temporary outage"}`), nil
				}
			}
		}
		return base.RoundTrip(r)
	})
	steps(d, now, 6)
	if okAttempts != 2 {
		t.Fatalf("recovery attempts = %d, want a retry after the 503 (messages %v)", okAttempts, relay.names())
	}
	if got := relay.names(); got[len(got)-1] != "service_down:ok" {
		t.Fatalf("relay never received the recovery: %v", got)
	}
	if st := d.state.Alerts["service_down"]; !st.Acked || st.Firing {
		t.Fatalf("state after retry: %+v", st)
	}
}

// A firing transition whose POST failed is also retried, and not doubled.
func TestFailedFiringIsRetriedOnce(t *testing.T) {
	relay := &fakeRelay{secret: []byte("secret")}
	d, now := newTestDaemon(t, relay, healthy, downSample, downSample, downSample, downSample)
	base := d.client.HTTP.Transport
	if base == nil {
		base = http.DefaultTransport
	}
	fails := 1
	d.client.HTTP.Transport = roundTrip(func(r *http.Request) (*http.Response, error) {
		if r.URL.Path == "/v1/alert" && fails > 0 {
			body, _ := io.ReadAll(r.Body)
			r.Body = io.NopCloser(strings.NewReader(string(body)))
			if strings.Contains(string(body), `"service_down"`) {
				fails--
				return response(502, `{"error":"delivery failed"}`), nil
			}
		}
		return base.RoundTrip(r)
	})
	steps(d, now, 5)
	if got := relay.names(); len(got) != 2 || got[1] != "service_down:firing" {
		t.Fatalf("exactly one firing must reach the relay after the retry: %v", got)
	}
}

// A 401 caused by clock skew must not latch the daemon as unpaired.
func TestClockSkewDoesNotUnpair(t *testing.T) {
	relay := &fakeRelay{secret: []byte("secret")}
	d, now := newTestDaemon(t, relay, healthy)
	d.client.HTTP.Transport = roundTrip(func(*http.Request) (*http.Response, error) {
		return response(401, `{"error":"request timestamp outside the replay window"}`), nil
	})
	steps(d, now, 2)
	if d.state.Unpaired || d.state.LastError == "" {
		t.Fatalf("clock skew: %+v", d.state)
	}
	d.client.HTTP.Transport = roundTrip(func(*http.Request) (*http.Response, error) {
		return response(401, `{"error":"unknown node"}`), nil
	})
	steps(d, now, 1)
	if !d.state.Unpaired {
		t.Fatal("'unknown node' is the unpaired signal")
	}
}

// Reload with new credentials rebuilds the client and clears Unpaired.
func TestReloadRebuildsClient(t *testing.T) {
	relay := &fakeRelay{secret: []byte("secret")}
	d, now := newTestDaemon(t, relay, healthy)
	steps(d, now, 1)
	d.state.Unpaired = true
	cfg := d.cfg
	cfg.Secret = base64.StdEncoding.EncodeToString([]byte("new-secret"))
	cfg.NodeID = "n_2"
	if err := d.apply(cfg); err != nil {
		t.Fatal(err)
	}
	if d.state.Unpaired || d.client.NodeID != "n_2" || string(d.client.Secret) != "new-secret" {
		t.Fatalf("client not rebuilt: unpaired=%v id=%s", d.state.Unpaired, d.client.NodeID)
	}
	cfg.Secret = "not base64!"
	if err := d.apply(cfg); err == nil {
		t.Fatal("bad credentials must be rejected, keeping the old client")
	}
}

// Duration thresholds are bounded by the retained history so an accepted
// value can always fire.
func TestLongWindowsAreBounded(t *testing.T) {
	cfg := DefaultConfig()
	if err := cfg.Set("rpc_unreachable.minutes", "60"); err == nil {
		t.Fatal("60 minutes exceeds the history window and must be rejected")
	}
	if err := cfg.Set("rpc_unreachable.minutes", "30"); err != nil {
		t.Fatal(err)
	}
	var h []metrics.Sample
	for i := range 150 {
		now := base.Add(time.Duration(i) * 30 * time.Second)
		s := healthy(now)
		s.Node.Reachable = false
		h = trimHistory(append(h, s), now)
	}
	if !rpcUnreachable(h, cfg.Rules["rpc_unreachable"]).Firing {
		t.Fatal("the maximum accepted window must fire after a longer outage")
	}
	// A hand-edited file with a too-long window is clamped on load.
	path := t.TempDir() + "/alerts.json"
	if err := writeFile(path, `{"alerts":{"sync_behind":{"enabled":true,"thresholds":{"minutes":90}}}}`); err != nil {
		t.Fatal(err)
	}
	loaded, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Rules["sync_behind"].Thresholds["minutes"] != MaxWindowMinutes {
		t.Fatalf("minutes = %v, want clamp to %d", loaded.Rules["sync_behind"].Thresholds["minutes"], MaxWindowMinutes)
	}
}

func TestPeerDetailMatchesTrigger(t *testing.T) {
	r := rule(t, "not_enough_peers")
	cfg := DefaultConfig().Rules["not_enough_peers"]
	h := series(12, func(_ int, s *metrics.Sample) { s.Node.State = 3; s.Node.NumPeers = 14 })
	res := r.Evaluate(h, cfg)
	if !res.Firing || !strings.Contains(res.Detail, "node reports 'not enough peers' (14 connected)") {
		t.Fatalf("detail = %q", res.Detail)
	}
}
