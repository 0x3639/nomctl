package alerts

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/0x3639/nomctl/internal/alertproto"
	"github.com/0x3639/nomctl/internal/metrics"
)

// fakeRelay records signed requests and can be switched to reject.
type fakeRelay struct {
	mu         sync.Mutex
	secret     []byte
	alerts     []alertproto.AlertRequest
	heartbeats int
	reject     bool
	now        func() time.Time
}

func (f *fakeRelay) handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		body, _ := io.ReadAll(r.Body)
		ts, _ := strconv.ParseInt(r.Header.Get(alertproto.HeaderTimestamp), 10, 64)
		if f.reject || r.Header.Get(alertproto.HeaderNode) != "n_1" ||
			alertproto.Verify(f.secret, ts, body, r.Header.Get(alertproto.HeaderSignature), f.now()) != nil {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"error":"unknown node"}`))
			return
		}
		switch r.URL.Path {
		case "/v1/alert":
			var a alertproto.AlertRequest
			_ = json.Unmarshal(body, &a)
			f.alerts = append(f.alerts, a)
		case "/v1/heartbeat":
			f.heartbeats++
		}
		w.WriteHeader(http.StatusNoContent)
	})
}

func (f *fakeRelay) names() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []string
	for _, a := range f.alerts {
		out = append(out, a.Alert+":"+string(a.State))
	}
	return out
}

// scriptedSampler returns samples from a script, then keeps returning the last.
type scriptedSampler struct {
	now     func() time.Time
	samples []func(t time.Time) metrics.Sample
	i       int
}

func (s *scriptedSampler) Take(context.Context) metrics.Sample {
	fn := s.samples[min(s.i, len(s.samples)-1)]
	s.i++
	return fn(s.now())
}

func downSample(t time.Time) metrics.Sample {
	s := healthy(t)
	s.Service.ActiveState, s.Service.SubState, s.Service.NRestarts = "inactive", "dead", 1
	return s
}

func newTestDaemon(t *testing.T, relay *fakeRelay, script ...func(time.Time) metrics.Sample) (*Daemon, *time.Time) {
	t.Helper()
	now := base
	clock := func() time.Time { return now }
	relay.now = clock
	srv := httptest.NewServer(relay.handler())
	t.Cleanup(srv.Close)
	cfg := DefaultConfig()
	cfg.RelayURL, cfg.NodeID, cfg.Secret, cfg.Name = srv.URL, "n_1", base64.StdEncoding.EncodeToString(relay.secret), "pillar-1"
	cfgPath := filepath.Join(t.TempDir(), "alerts.json")
	if err := cfg.Save(cfgPath); err != nil {
		t.Fatal(err)
	}
	client, err := NewClient(cfg, "test")
	if err != nil {
		t.Fatal(err)
	}
	client.Now = clock
	sampler := &scriptedSampler{now: clock, samples: script}
	d := NewDaemon(cfg, cfgPath, filepath.Join(t.TempDir(), "state.json"), sampler, client)
	d.now = clock
	return d, &now
}

func steps(d *Daemon, now *time.Time, n int) {
	for range n {
		d.Step(context.Background())
		*now = now.Add(30 * time.Second)
	}
}

func TestDaemonTransitions(t *testing.T) {
	relay := &fakeRelay{secret: []byte("secret")}
	d, now := newTestDaemon(t, relay, healthy, healthy, downSample, downSample, downSample, healthy, healthy, healthy)
	steps(d, now, 1)
	if got := relay.names(); len(got) != 1 || got[0] != "started:info" {
		t.Fatalf("first step must send only the started info, got %v", got)
	}
	steps(d, now, 4) // healthy, down, down, down
	if got := relay.names(); len(got) != 2 || got[1] != "service_down:firing" {
		t.Fatalf("after two down samples expect one firing, got %v", got)
	}
	steps(d, now, 3) // healthy x3
	if got := relay.names(); len(got) != 3 || got[2] != "service_down:ok" {
		t.Fatalf("recovery must send ok once, got %v", got)
	}
	relay.mu.Lock()
	if relay.alerts[1].Severity != alertproto.Critical || relay.alerts[1].Title != "service down" || relay.alerts[1].Detail == "" || relay.alerts[2].Title != "service back up" {
		t.Errorf("alert content: %+v", relay.alerts[1:])
	}
	hb := relay.heartbeats
	relay.mu.Unlock()
	if hb != 8 {
		t.Errorf("heartbeats = %d, want one per step", hb)
	}
	st, err := LoadState(d.statePath)
	if err != nil || st.Alerts["service_down"].Firing || st.LastHeartbeatOK.IsZero() || st.Unpaired {
		t.Errorf("state file: %+v %v", st, err)
	}
}

func TestDaemonReminder(t *testing.T) {
	relay := &fakeRelay{secret: []byte("secret")}
	d, now := newTestDaemon(t, relay, downSample)
	steps(d, now, 3) // started, then firing at step 3
	if got := relay.names(); len(got) != 2 {
		t.Fatalf("got %v", got)
	}
	steps(d, now, 18) // up to 9.5 minutes after the firing: no reminder yet
	if got := relay.names(); len(got) != 2 {
		t.Fatalf("no reminder before 10 minutes, got %v", got)
	}
	steps(d, now, 2)
	if got := relay.names(); len(got) != 3 || got[2] != "service_down:firing" {
		t.Fatalf("reminder after 10 minutes, got %v", got)
	}
}

func TestDaemonAlreadyDownAtStart(t *testing.T) {
	// A node that is already down when the daemon starts: the first step only
	// establishes the baseline (and announces itself); the condition is
	// confirmed on the second sample and alerts then, so an operator whose
	// daemon restarted while the node was down still hears about it.
	relay := &fakeRelay{secret: []byte("secret")}
	d, now := newTestDaemon(t, relay, downSample, downSample, healthy, healthy)
	steps(d, now, 1)
	if got := relay.names(); len(got) != 1 {
		t.Fatalf("first step sends only the announcement, got %v", got)
	}
	steps(d, now, 1)
	if got := relay.names(); len(got) != 2 || got[1] != "service_down:firing" {
		t.Fatalf("confirmed condition alerts on the second step, got %v", got)
	}
	steps(d, now, 2)
	if got := relay.names(); len(got) != 3 || got[2] != "service_down:ok" {
		t.Fatalf("recovery sends ok, got %v", got)
	}
}

func TestDaemonBaselineOKIsSilent(t *testing.T) {
	// Single-sample rules that are already false at start must not send "ok".
	relay := &fakeRelay{secret: []byte("secret")}
	d, now := newTestDaemon(t, relay, healthy)
	steps(d, now, 5)
	if got := relay.names(); len(got) != 1 {
		t.Fatalf("healthy node must produce no ok messages, got %v", got)
	}
}

func TestDaemonUnpaired(t *testing.T) {
	relay := &fakeRelay{secret: []byte("secret")}
	d, now := newTestDaemon(t, relay, healthy)
	steps(d, now, 1)
	relay.mu.Lock()
	relay.reject = true
	relay.mu.Unlock()
	steps(d, now, 2)
	st, _ := LoadState(d.statePath)
	if !st.Unpaired || st.LastError == "" {
		t.Errorf("401 must mark the daemon unpaired: %+v", st)
	}
	relay.mu.Lock()
	hb := relay.heartbeats
	relay.mu.Unlock()
	if hb != 1 {
		t.Errorf("no requests after unpaired, heartbeats = %d", hb)
	}
}

func TestDaemonReload(t *testing.T) {
	relay := &fakeRelay{secret: []byte("secret")}
	d, _ := newTestDaemon(t, relay, healthy)
	cfg := d.cfg
	if err := cfg.Set("disk_low.min_free_gb", "99"); err != nil {
		t.Fatal(err)
	}
	if err := cfg.Save(d.cfgPath); err != nil {
		t.Fatal(err)
	}
	reload := make(chan struct{}, 1)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- d.Run(ctx, reload) }()
	reload <- struct{}{}
	time.Sleep(100 * time.Millisecond)
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if d.cfg.Rules["disk_low"].Threshold("disk_low", "min_free_gb") != 99 {
		t.Error("reload did not apply the new threshold")
	}
}

// pillarSampler records the pillar name pushed by the daemon.
type pillarSampler struct {
	scriptedSampler
	name string
}

func (p *pillarSampler) SetPillarName(name string) { p.name = name }

func TestDaemonPassesPillarName(t *testing.T) {
	cfg := DefaultConfig()
	cfg.PillarName = "MyPillar"
	ps := &pillarSampler{scriptedSampler: scriptedSampler{now: time.Now, samples: []func(time.Time) metrics.Sample{healthy}}}
	client, err := NewClient(Config{RelayURL: "http://127.0.0.1:1", NodeID: "n", Secret: base64.StdEncoding.EncodeToString([]byte("s"))}, "t")
	if err != nil {
		t.Fatal(err)
	}
	d := NewDaemon(cfg, "", "", ps, client)
	if ps.name != "MyPillar" {
		t.Fatalf("sampler pillar = %q", ps.name)
	}
	cfg.PillarName = ""
	if err := d.apply(cfg); err != nil {
		t.Fatal(err)
	}
	if ps.name != "" {
		t.Fatal("reload must clear the pillar name")
	}
}

func TestInfoAlertsAreNotReminded(t *testing.T) {
	relay := &fakeRelay{secret: []byte("secret")}
	d, now := newTestDaemon(t, relay, healthy)
	d.cfg.Rules["update_available"] = RuleConfig{Enabled: true}
	NewerVersion = func(string, string) bool { return true }
	UpdateChecker = func() UpdateInfo { return UpdateInfo{NomctlLatest: "v9.9.9", NomctlRunning: "0.4.0"} }
	defer func() { NewerVersion = func(string, string) bool { return false }; UpdateChecker = nil }()
	steps(d, now, 2) // baseline (firing, silent) then... still firing: baseline already firing, so no transition
	// Make it transition: clear then set.
	UpdateChecker = func() UpdateInfo { return UpdateInfo{} }
	steps(d, now, 1)
	UpdateChecker = func() UpdateInfo { return UpdateInfo{NomctlLatest: "v9.9.9", NomctlRunning: "0.4.0"} }
	steps(d, now, 30) // 15 minutes: an info alert is sent once, never reminded
	got := relay.names()
	count := 0
	for _, n := range got {
		if n == "update_available:firing" {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("info alert sent %d times, want 1: %v", count, got)
	}
}
