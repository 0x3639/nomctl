package relay

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/0x3639/nomctl/internal/alertproto"
)

// flakySender fails while fail is set and counts attempts and deliveries.
type flakySender struct {
	fail      error
	attempts  int
	delivered int
}

func (m *flakySender) Send(context.Context, int64, string) error {
	m.attempts++
	if m.fail != nil {
		return m.fail
	}
	m.delivered++
	return nil
}

func flakyServer(t *testing.T, proxies string) (*Server, *flakySender, Node, *time.Time) {
	t.Helper()
	now := time.Date(2026, 9, 6, 12, 0, 0, 0, time.UTC)
	m := &flakySender{}
	cidrs, err := ParseCIDRs(proxies)
	if err != nil {
		t.Fatal(err)
	}
	s := NewServer(NewMemoryStore(), m, Options{Now: func() time.Time { return now }, TrustedProxies: cidrs})
	n := Node{ID: "n_r", ChatID: 1, Name: "r", Secret: []byte("x"), Created: now, LastSeen: now}
	if err := s.store.CreateNode(context.Background(), n); err != nil {
		t.Fatal(err)
	}
	return s, m, n, &now
}

// A recovery whose Telegram send failed must be retried by a later ok.
func TestFailedRecoveryIsRetried(t *testing.T) {
	s, m, n, now := flakyServer(t, "")
	ctx := context.Background()
	if err := s.deliver(ctx, n, alert("service_down", alertproto.Firing)); err != nil {
		t.Fatal(err)
	}
	m.fail = errors.New("telegram outage")
	*now = now.Add(time.Minute)
	if err := s.deliver(ctx, n, alert("service_down", alertproto.OK)); err == nil {
		t.Fatal("failed send must surface as an error")
	}
	m.fail = nil
	*now = now.Add(time.Minute)
	if err := s.deliver(ctx, n, alert("service_down", alertproto.OK)); err != nil {
		t.Fatal(err)
	}
	if m.delivered != 2 {
		t.Fatalf("delivered=%d attempts=%d", m.delivered, m.attempts)
	}
	// And a firing send that failed is retried by the next firing report.
	m.fail = errors.New("outage")
	_ = s.deliver(ctx, n, alert("disk_low", alertproto.Firing))
	m.fail = nil
	if err := s.deliver(ctx, n, alert("disk_low", alertproto.Firing)); err != nil || m.delivered != 3 {
		t.Fatalf("firing retry: %v delivered=%d", err, m.delivered)
	}
}

// A muted alert is recorded so the later recovery is still delivered.
func TestMutedAlertStillRecordsState(t *testing.T) {
	s, m, n, _ := flakyServer(t, "")
	ctx := context.Background()
	if err := s.store.SetMute(ctx, Mute{NodeID: n.ID, Alert: "disk_low", Until: time.Now().Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}
	if err := s.deliver(ctx, n, alert("disk_low", alertproto.Firing)); err != nil || m.attempts != 0 {
		t.Fatalf("muted: %v attempts=%d", err, m.attempts)
	}
	_ = s.store.ClearMute(ctx, n.ID, "disk_low")
	if err := s.deliver(ctx, n, alert("disk_low", alertproto.OK)); err != nil || m.delivered != 1 {
		t.Fatalf("recovery after mute: %v delivered=%d", err, m.delivered)
	}
}

// node_silent must be retried on the next tick if Telegram was down.
func TestFailedSilentAlertIsRetried(t *testing.T) {
	s, m, _, now := flakyServer(t, "")
	ctx := context.Background()
	m.fail = errors.New("telegram outage")
	*now = now.Add(6 * time.Minute)
	if err := s.CheckSilent(ctx); err != nil {
		t.Fatal(err)
	}
	m.fail = nil
	*now = now.Add(time.Minute)
	if err := s.CheckSilent(ctx); err != nil {
		t.Fatal(err)
	}
	if m.delivered != 1 || m.attempts != 2 {
		t.Fatalf("delivered=%d attempts=%d", m.delivered, m.attempts)
	}
	*now = now.Add(time.Minute)
	_ = s.CheckSilent(ctx)
	if m.delivered != 1 {
		t.Fatal("silent must be reported once")
	}
}

// Any command from the chat clears the blocked flag on its nodes.
func TestIncomingCommandClearsBlocked(t *testing.T) {
	s, m, n, _ := flakyServer(t, "")
	ctx := context.Background()
	m.fail = ErrBlocked
	_ = s.deliver(ctx, n, alert("service_down", alertproto.Firing))
	got, _ := s.store.GetNode(ctx, n.ID)
	if !got.Blocked {
		t.Fatal("403 must mark the node blocked")
	}
	m.fail = nil
	if err := s.HandleUpdate(ctx, Update{ChatID: 1, Text: "/nodes"}); err != nil {
		t.Fatal(err)
	}
	got, _ = s.store.GetNode(ctx, n.ID)
	if got.Blocked {
		t.Fatal("a command from the chat must unblock its nodes")
	}
	if err := s.HandleUpdate(ctx, Update{ChatID: 2, Text: "/nodes"}); err != nil {
		t.Fatal(err)
	}
}

// X-Forwarded-For is ignored unless the peer is a trusted proxy, so the
// pairing limit cannot be bypassed by rotating the header.
func TestForwardedForOnlyFromTrustedProxy(t *testing.T) {
	pair := func(s *Server, remote, xff string) int {
		req := httptest.NewRequest(http.MethodPost, "/v1/pair", strings.NewReader(`{"code":"invalid","name":"r"}`))
		req.RemoteAddr = remote
		if xff != "" {
			req.Header.Set("X-Forwarded-For", xff)
		}
		w := httptest.NewRecorder()
		s.Handler().ServeHTTP(w, req)
		return w.Code
	}
	s, _, _, _ := flakyServer(t, "")
	var code int
	for i := range pairRatePerMin + 1 {
		code = pair(s, "192.0.2.1:1234", strings.Repeat("9", i+1))
	}
	if code != http.StatusTooManyRequests {
		t.Fatalf("untrusted header must not bypass the limit: %d", code)
	}

	s, _, _, _ = flakyServer(t, "10.0.0.0/8")
	// Behind the trusted proxy, distinct real clients are limited separately.
	for i := range pairRatePerMin {
		if code = pair(s, "10.0.0.5:1", "203.0.113.7"); code == http.StatusTooManyRequests {
			t.Fatalf("client A limited early at %d", i)
		}
	}
	if code = pair(s, "10.0.0.5:1", "203.0.113.7"); code != http.StatusTooManyRequests {
		t.Fatalf("client A should be limited now: %d", code)
	}
	if code = pair(s, "10.0.0.5:1", "203.0.113.8"); code == http.StatusTooManyRequests {
		t.Fatal("client B must have its own budget")
	}
	// A forged left-most entry behind a trusted proxy is ignored: the
	// right-most untrusted hop wins.
	if got := s.clientIP(&http.Request{RemoteAddr: "10.0.0.5:1", Header: http.Header{"X-Forwarded-For": {"1.1.1.1, 203.0.113.9, 10.0.0.2"}}}); got != "203.0.113.9" {
		t.Fatalf("clientIP = %s", got)
	}
	if _, err := ParseCIDRs("nope"); err == nil {
		t.Fatal("bad CIDR must error")
	}
}

// A failed delivery is reported to the node as 502 so the daemon retries.
func TestAlertEndpointReportsDeliveryFailure(t *testing.T) {
	h := newHarness(t)
	creds := h.pair(1, "a")
	failing := &flakySender{fail: errors.New("telegram outage")}
	h.srv.tg = failing
	if status, _ := h.signed(creds, "/v1/alert", alert("disk_low", alertproto.Firing)); status != http.StatusBadGateway {
		t.Fatalf("status = %d", status)
	}
	failing.fail = nil
	if status, _ := h.signed(creds, "/v1/alert", alert("disk_low", alertproto.Firing)); status != http.StatusNoContent || failing.delivered != 1 {
		t.Fatalf("retry: status=%d delivered=%d", status, failing.delivered)
	}
}

// failingStore wraps a Store and fails SetLastSent on demand.
type failingStore struct {
	Store
	failSet bool
}

func (f *failingStore) SetLastSent(ctx context.Context, nodeID, alert string, state alertproto.State, at time.Time) error {
	if f.failSet {
		return errors.New("disk full")
	}
	return f.Store.SetLastSent(ctx, nodeID, alert, state, at)
}

// A failed last-sent record must be reported so the node retries; otherwise
// the relay never learns the alert fired and suppresses the recovery.
func TestSetLastSentFailureIsReported(t *testing.T) {
	now := time.Date(2026, 9, 6, 12, 0, 0, 0, time.UTC)
	fs := &failingStore{Store: NewMemoryStore()}
	m := &flakySender{}
	s := NewServer(fs, m, Options{Now: func() time.Time { return now }})
	n := Node{ID: "n", ChatID: 1, Name: "n", Secret: []byte("x"), Created: now, LastSeen: now}
	ctx := context.Background()
	if err := fs.CreateNode(ctx, n); err != nil {
		t.Fatal(err)
	}
	if err := fs.SetMute(ctx, Mute{NodeID: n.ID, Alert: "disk_low", Until: now.Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}
	fs.failSet = true
	if err := s.deliver(ctx, n, alert("disk_low", alertproto.Firing)); err == nil {
		t.Fatal("store failure must be reported")
	}
	fs.failSet = false
	if err := s.deliver(ctx, n, alert("disk_low", alertproto.Firing)); err != nil {
		t.Fatal(err)
	}
	_ = fs.ClearMute(ctx, n.ID, "disk_low")
	if err := s.deliver(ctx, n, alert("disk_low", alertproto.OK)); err != nil || m.delivered != 1 {
		t.Fatalf("recovery after retried record: %v delivered=%d", err, m.delivered)
	}
}
