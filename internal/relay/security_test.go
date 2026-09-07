package relay

import (
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/0x3639/nomctl/internal/alertproto"
)

// A transport failure must not hand the bot token to the node.
func TestDeliveryErrorNeverCarriesToken(t *testing.T) {
	h := newHarness(t)
	creds := h.pair(1, "a")
	const token = "123456:SECRET-BOT-TOKEN"
	failing := &flakySender{fail: fmt.Errorf("Post \"https://api.telegram.org/bot%s/sendMessage\": dial tcp: lookup api.telegram.org: no such host", token)}
	h.srv.tg = failing
	status, body := h.signed(creds, "/v1/alert", alert("disk_low", alertproto.Firing))
	if status != http.StatusBadGateway {
		t.Fatalf("status = %d", status)
	}
	if strings.Contains(string(body), token) || strings.Contains(string(body), "telegram.org") {
		t.Fatalf("response leaks transport detail: %s", body)
	}
	if !strings.Contains(string(body), "delivery failed") {
		t.Fatalf("node must still learn to retry: %s", body)
	}
}

// The Telegram client itself strips the token from transport errors.
func TestTelegramRedactsTransportErrors(t *testing.T) {
	const token = "999:TOKEN"
	tg := NewTelegram(token)
	tg.BaseURL = "http://127.0.0.1:1" // nothing listens
	tg.HTTP.Timeout = time.Second
	_, err := tg.call(t.Context(), "sendMessage", map[string]any{}, false)
	if err == nil {
		t.Fatal("expected a transport error")
	}
	if strings.Contains(err.Error(), token) {
		t.Fatalf("token in error: %v", err)
	}
	if !strings.Contains(err.Error(), "telegram sendMessage") {
		t.Fatalf("cause lost: %v", err)
	}
	// Even a message that embeds the token elsewhere is scrubbed.
	if got := tg.redact("x", errors.New("boom "+token)).Error(); strings.Contains(got, token) {
		t.Fatalf("redact missed token: %s", got)
	}
}

// Unknown alert names, bad states and oversized text are rejected before
// anything is stored, muted or not.
func TestAlertValidation(t *testing.T) {
	h := newHarness(t)
	creds := h.pair(1, "a")
	if err := h.srv.store.SetMute(t.Context(), Mute{NodeID: creds.NodeID, Alert: "all", Until: h.now.Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}
	bad := []alertproto.AlertRequest{
		{Alert: strings.Repeat("x", 4096), State: alertproto.Firing, Severity: alertproto.Warning},
		{Alert: "made_up", State: alertproto.Firing, Severity: alertproto.Warning},
		{Alert: "disk_low", State: "weird", Severity: alertproto.Warning},
		{Alert: "disk_low", State: alertproto.Firing, Severity: "loud"},
		{Alert: "disk_low", State: alertproto.Firing, Severity: alertproto.Warning, Detail: strings.Repeat("d", alertproto.MaxDetailLen+1)},
	}
	for _, a := range bad {
		if status, _ := h.signed(creds, "/v1/alert", a); status != http.StatusBadRequest {
			t.Errorf("%.20s: status = %d", a.Alert, status)
		}
	}
	if st, _, _ := h.srv.store.LastSent(t.Context(), creds.NodeID, "made_up"); st != "" {
		t.Error("rejected alert was persisted")
	}
	for _, name := range []string{"disk_low", "started", "test"} {
		a := alertproto.AlertRequest{Alert: name, State: alertproto.Info, Severity: alertproto.InfoSev, Title: "t"}
		if name == "disk_low" {
			a.State, a.Severity = alertproto.Firing, alertproto.Warning
		}
		if status, body := h.signed(creds, "/v1/alert", a); status != http.StatusNoContent {
			t.Errorf("%s: status = %d %s", name, status, body)
		}
	}
}

// Unauthenticated requests with made-up node ids are limited per IP and
// never grow the per-node limiter.
func TestUnauthenticatedRequestsAreBounded(t *testing.T) {
	h := newHarness(t)
	for i := 0; i < admitRatePerMin+5; i++ {
		req, _ := http.NewRequest(http.MethodPost, h.http.URL+"/v1/heartbeat", strings.NewReader("{}"))
		req.Header.Set(alertproto.HeaderNode, fmt.Sprintf("n_%016x", i))
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		_ = res.Body.Close()
		if i < admitRatePerMin && res.StatusCode != http.StatusUnauthorized {
			t.Fatalf("request %d: %d", i, res.StatusCode)
		}
		if i >= admitRatePerMin && res.StatusCode != http.StatusTooManyRequests {
			t.Fatalf("request %d should be admission-limited, got %d", i, res.StatusCode)
		}
	}
	if n := len(h.srv.nodeLimit.counts); n != 0 {
		t.Errorf("per-node limiter grew to %d entries from unauthenticated traffic", n)
	}
	if n := len(h.srv.admitLimit.counts); n != 1 {
		t.Errorf("admission limiter should hold one IP, has %d", n)
	}
}

func TestNodeIDFormat(t *testing.T) {
	if !validNodeID(newNodeID()) {
		t.Error("generated id rejected")
	}
	for _, bad := range []string{"", "n_", "n", "x_0123456789abcdef", "n_0123456789ABCDEF", "n_0123456789abcde", "n_0123456789abcdef0", strings.Repeat("n", 18)} {
		if validNodeID(bad) {
			t.Errorf("%q accepted", bad)
		}
	}
}

// The limiter's memory is bounded and a full map is swept at most once a
// minute rather than on every admission.
func TestRateLimiterBounded(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	r := newRateLimiter(10, func() time.Time { return now })
	for i := 0; i < limiterMaxKeys; i++ {
		if !r.allow(fmt.Sprint(i)) {
			t.Fatalf("key %d refused before the cap", i)
		}
	}
	if r.allow("one-more") {
		t.Fatal("new key admitted past the cap")
	}
	if !r.allow("5") {
		t.Fatal("known key refused at the cap")
	}
	now = now.Add(61 * time.Second)
	if !r.allow("fresh") {
		t.Fatal("expired keys were not swept once the window passed")
	}
	if len(r.counts) != 1 {
		t.Errorf("after sweep %d keys, want 1", len(r.counts))
	}
}
