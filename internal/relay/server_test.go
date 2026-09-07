package relay

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/0x3639/nomctl/internal/alertproto"
)

// memSender records messages per chat.
type memSender struct {
	mu   sync.Mutex
	msgs []sentMessage
}

func (m *memSender) Send(_ context.Context, chatID int64, text string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.msgs = append(m.msgs, sentMessage{ChatID: chatID, Text: text})
	return nil
}

func (m *memSender) forChat(chat int64) []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []string
	for _, s := range m.msgs {
		if s.ChatID == chat {
			out = append(out, s.Text)
		}
	}
	return out
}

func (m *memSender) reset() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.msgs = nil
}

type harness struct {
	t      *testing.T
	srv    *Server
	sender *memSender
	http   *httptest.Server
	now    time.Time
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	h := &harness{t: t, sender: &memSender{}, now: time.Date(2026, 9, 6, 12, 0, 0, 0, time.UTC)}
	h.srv = NewServer(NewMemoryStore(), h.sender, Options{Now: func() time.Time { return h.now }})
	h.http = httptest.NewServer(h.srv.Handler())
	t.Cleanup(h.http.Close)
	return h
}

// pairCode simulates /start and extracts the code from the reply.
func (h *harness) pairCode(chat int64) string {
	h.t.Helper()
	if err := h.srv.HandleUpdate(context.Background(), Update{ChatID: chat, Text: "/start"}); err != nil {
		h.t.Fatal(err)
	}
	msgs := h.sender.forChat(chat)
	last := msgs[len(msgs)-1]
	i := strings.Index(last, "`")
	if i < 0 {
		h.t.Fatalf("no code in %q", last)
	}
	return last[i+1 : i+9]
}

func (h *harness) post(path string, body any, headers map[string]string) (int, []byte) {
	h.t.Helper()
	data, _ := json.Marshal(body)
	req, _ := http.NewRequest(http.MethodPost, h.http.URL+path, bytes.NewReader(data))
	req.Header.Set("Content-Type", "application/json")
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		h.t.Fatal(err)
	}
	defer func() { _ = res.Body.Close() }()
	var buf bytes.Buffer
	_, _ = buf.ReadFrom(res.Body)
	return res.StatusCode, buf.Bytes()
}

func (h *harness) pair(chat int64, name string) alertproto.PairResponse {
	h.t.Helper()
	code := h.pairCode(chat)
	status, body := h.post("/v1/pair", alertproto.PairRequest{Code: code, Name: name, Host: "host-" + name, Version: "t"}, nil)
	if status != http.StatusOK {
		h.t.Fatalf("pair: %d %s", status, body)
	}
	var resp alertproto.PairResponse
	_ = json.Unmarshal(body, &resp)
	return resp
}

func (h *harness) signed(creds alertproto.PairResponse, path string, body any) (int, []byte) {
	h.t.Helper()
	data, _ := json.Marshal(body)
	secret, _ := base64.StdEncoding.DecodeString(creds.Secret)
	ts := h.now.Unix()
	return h.post(path, body, map[string]string{
		alertproto.HeaderNode:      creds.NodeID,
		alertproto.HeaderTimestamp: strconv.FormatInt(ts, 10),
		alertproto.HeaderSignature: alertproto.Sign(secret, ts, data),
	})
}

func alert(name string, state alertproto.State) alertproto.AlertRequest {
	info, _ := alertproto.Lookup(name)
	title := info.Title
	if state == alertproto.OK {
		title = info.OKTitle
	}
	return alertproto.AlertRequest{Alert: name, State: state, Severity: info.Severity, Title: title, Detail: "detail"}
}

func TestPairing(t *testing.T) {
	h := newHarness(t)
	creds := h.pair(1, "pillar-1")
	if creds.NodeID == "" || creds.Secret == "" {
		t.Fatalf("creds = %+v", creds)
	}
	msgs := h.sender.forChat(1)
	if len(msgs) != 2 || !strings.Contains(msgs[1], "paired") {
		t.Errorf("chat should get a code and a paired notice: %v", msgs)
	}
	code := h.pairCode(1)
	if status, _ := h.post("/v1/pair", alertproto.PairRequest{Code: code, Name: "pillar-1", Host: "h"}, nil); status != http.StatusConflict {
		t.Errorf("duplicate name = %d", status)
	}
	if status, _ := h.post("/v1/pair", alertproto.PairRequest{Code: code, Name: "other", Host: "h"}, nil); status != http.StatusBadRequest {
		t.Errorf("reused code = %d", status)
	}
	code = h.pairCode(1)
	h.now = h.now.Add(11 * time.Minute)
	if status, _ := h.post("/v1/pair", alertproto.PairRequest{Code: code, Name: "late", Host: "h"}, nil); status != http.StatusBadRequest {
		t.Errorf("expired code = %d", status)
	}
	if status, _ := h.post("/v1/pair", alertproto.PairRequest{Code: "x", Name: "bad name!", Host: "h"}, nil); status != http.StatusBadRequest {
		t.Errorf("bad name = %d", status)
	}
}

func TestAuth(t *testing.T) {
	h := newHarness(t)
	creds := h.pair(1, "a")
	if status, _ := h.signed(creds, "/v1/heartbeat", alertproto.HeartbeatRequest{At: h.now}); status != http.StatusNoContent {
		t.Errorf("valid = %d", status)
	}
	bad := creds
	bad.Secret = base64.StdEncoding.EncodeToString([]byte("wrong-secret-wrong-secret-wrong!"))
	if status, _ := h.signed(bad, "/v1/heartbeat", alertproto.HeartbeatRequest{}); status != http.StatusUnauthorized {
		t.Errorf("bad secret = %d", status)
	}
	unknown := creds
	unknown.NodeID = "n_nope"
	if status, _ := h.signed(unknown, "/v1/heartbeat", alertproto.HeartbeatRequest{}); status != http.StatusUnauthorized {
		t.Errorf("unknown node = %d", status)
	}
	if status, _ := h.post("/v1/alert", alert("disk_low", alertproto.Firing), nil); status != http.StatusUnauthorized {
		t.Errorf("unsigned = %d", status)
	}
}

func TestDeliveryMatrix(t *testing.T) {
	h := newHarness(t)
	creds := h.pair(1, "a")
	h.sender.reset()
	send := func(a alertproto.AlertRequest) {
		if status, body := h.signed(creds, "/v1/alert", a); status != http.StatusNoContent {
			t.Fatalf("alert: %d %s", status, body)
		}
	}
	count := func() int { return len(h.sender.forChat(1)) }

	send(alert("disk_low", alertproto.OK))
	if count() != 0 {
		t.Error("ok without prior firing must be dropped")
	}
	send(alert("disk_low", alertproto.Firing))
	if count() != 1 || !strings.Contains(h.sender.forChat(1)[0], "🟠") {
		t.Errorf("first firing delivered as warning: %v", h.sender.forChat(1))
	}
	send(alert("disk_low", alertproto.Firing))
	if count() != 1 {
		t.Error("repeat within 10 minutes dropped")
	}
	h.now = h.now.Add(11 * time.Minute)
	send(alert("disk_low", alertproto.Firing))
	if count() != 2 {
		t.Error("reminder after 10 minutes delivered")
	}
	send(alert("disk_low", alertproto.OK))
	if count() != 3 || !strings.Contains(h.sender.forChat(1)[2], "🟢") {
		t.Errorf("ok after firing delivered: %v", h.sender.forChat(1))
	}
	send(alert("disk_low", alertproto.OK))
	if count() != 3 {
		t.Error("second ok dropped")
	}
	send(alert("service_down", alertproto.Firing))
	if count() != 4 || !strings.Contains(h.sender.forChat(1)[3], "🔴") {
		t.Errorf("critical uses red: %v", h.sender.forChat(1))
	}
	send(alertproto.AlertRequest{Alert: "test", State: alertproto.Info, Severity: alertproto.InfoSev, Title: "test"})
	send(alertproto.AlertRequest{Alert: "test", State: alertproto.Info, Severity: alertproto.InfoSev, Title: "test"})
	if count() != 6 {
		t.Error("info always delivered")
	}

	// Mute: recorded but not sent; unmute; next transition goes through.
	if err := h.srv.HandleUpdate(context.Background(), Update{ChatID: 1, Text: "/mute a memory_high 1h"}); err != nil {
		t.Fatal(err)
	}
	h.sender.reset()
	send(alert("memory_high", alertproto.Firing))
	if count() != 0 {
		t.Error("muted alert must not be sent")
	}
	if err := h.srv.HandleUpdate(context.Background(), Update{ChatID: 1, Text: "/unmute a memory_high"}); err != nil {
		t.Fatal(err)
	}
	h.sender.reset()
	send(alert("memory_high", alertproto.OK))
	if count() != 1 {
		t.Errorf("state was recorded while muted, so ok is delivered after unmute: %v", h.sender.forChat(1))
	}
}

func TestChatIsolation(t *testing.T) {
	h := newHarness(t)
	a := h.pair(1, "a")
	h.pair(2, "b")
	h.sender.reset()
	h.signed(a, "/v1/alert", alert("service_down", alertproto.Firing))
	if len(h.sender.forChat(1)) != 1 || len(h.sender.forChat(2)) != 0 {
		t.Errorf("chat 1: %v, chat 2: %v", h.sender.forChat(1), h.sender.forChat(2))
	}
	if err := h.srv.HandleUpdate(context.Background(), Update{ChatID: 2, Text: "/unpair a"}); err != nil {
		t.Fatal(err)
	}
	if msgs := h.sender.forChat(2); !strings.Contains(msgs[len(msgs)-1], "No node named") {
		t.Errorf("chat 2 must not see chat 1's node: %v", msgs)
	}
}

func TestSilentDetection(t *testing.T) {
	h := newHarness(t)
	creds := h.pair(1, "a")
	h.sender.reset()
	h.now = h.now.Add(3 * time.Minute)
	if err := h.srv.CheckSilent(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(h.sender.forChat(1)) != 0 {
		t.Error("3 minutes is not silent")
	}
	h.now = h.now.Add(3 * time.Minute)
	_ = h.srv.CheckSilent(context.Background())
	_ = h.srv.CheckSilent(context.Background())
	msgs := h.sender.forChat(1)
	if len(msgs) != 1 || !strings.Contains(msgs[0], "node silent") {
		t.Errorf("silent once: %v", msgs)
	}
	if status, _ := h.signed(creds, "/v1/heartbeat", alertproto.HeartbeatRequest{At: h.now, Summary: alertproto.Summary{State: "synced", Height: 9}}); status != http.StatusNoContent {
		t.Fatal("heartbeat")
	}
	msgs = h.sender.forChat(1)
	if len(msgs) != 2 || !strings.Contains(msgs[1], "reporting again") {
		t.Errorf("recovery: %v", msgs)
	}
	if err := h.srv.HandleUpdate(context.Background(), Update{ChatID: 1, Text: "/nodes"}); err != nil {
		t.Fatal(err)
	}
	msgs = h.sender.forChat(1)
	if last := msgs[len(msgs)-1]; !strings.Contains(last, "synced 9") || strings.Contains(last, "silent") {
		t.Errorf("/nodes after recovery: %q", last)
	}
}

func TestUnpairFromNode(t *testing.T) {
	h := newHarness(t)
	creds := h.pair(1, "a")
	if status, _ := h.signed(creds, "/v1/unpair", struct{}{}); status != http.StatusNoContent {
		t.Fatal("unpair")
	}
	if status, _ := h.signed(creds, "/v1/heartbeat", alertproto.HeartbeatRequest{}); status != http.StatusUnauthorized {
		t.Errorf("after unpair = %d", status)
	}
}

func TestRateLimits(t *testing.T) {
	h := newHarness(t)
	var status int
	for range pairRatePerMin + 1 {
		status, _ = h.post("/v1/pair", alertproto.PairRequest{Code: "x", Name: "n", Host: "h"}, nil)
	}
	if status != http.StatusTooManyRequests {
		t.Errorf("pair limit = %d", status)
	}
	h.now = h.now.Add(2 * time.Minute)
	creds := h.pair(1, "a")
	for range nodeRatePerMin + 1 {
		status, _ = h.signed(creds, "/v1/heartbeat", alertproto.HeartbeatRequest{})
	}
	if status != http.StatusTooManyRequests {
		t.Errorf("node limit = %d", status)
	}
}

func TestCommands(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	h.pair(1, "pillar-1")
	h.sender.reset()
	cases := []struct{ text, want string }{
		{"/help", "pairing code"},
		{"/bogus", "pairing code"},
		{"/nodes", "pillar\\-1"},
		{"/mute", "Usage"},
		{"/mute nope disk_low", "No node named"},
		{"/mute pillar-1 bogus", "Unknown alert"},
		{"/mute pillar-1 disk_low 2d", "48h0m0s"},
		{"/mute pillar-1 all", "24h0m0s"},
		{"/mute pillar-1 all xyz", "Duration must"},
		{"/unmute pillar-1 all", "Unmuted all"},
		{"/start@nomctl_bot", "pairing code"},
		{"/unpair pillar-1", "Unpaired pillar"},
		{"/nodes", "No nodes paired"},
	}
	for _, c := range cases {
		h.sender.reset()
		if err := h.srv.HandleUpdate(ctx, Update{ChatID: 1, Text: c.text}); err != nil {
			t.Fatalf("%s: %v", c.text, err)
		}
		msgs := h.sender.forChat(1)
		if len(msgs) != 1 || !strings.Contains(msgs[0], c.want) {
			t.Errorf("%s: got %v, want substring %q", c.text, msgs, c.want)
		}
	}
	if err := h.srv.HandleUpdate(ctx, Update{ChatID: 1, Text: "not a command"}); err != nil || len(h.sender.forChat(1)) != 1 {
		t.Error("plain text is ignored")
	}
}

func TestFormatMessage(t *testing.T) {
	n := Node{Name: "pillar-1", Host: "vps.example"}
	at := time.Date(2026, 9, 6, 12, 0, 0, 0, time.UTC)
	got := FormatMessage(n, alertproto.AlertRequest{Alert: "service_down", State: alertproto.Firing, Severity: alertproto.Critical, Title: "service down", Detail: "go-zenon inactive (dead), 3 restarts", At: at})
	want := "🔴 *pillar\\-1* · service down\ngo\\-zenon inactive \\(dead\\), 3 restarts\nhost vps\\.example · 2026\\-09\\-06 12:00 UTC"
	if got != want {
		t.Errorf("got  %q\nwant %q", got, want)
	}
	if !strings.HasPrefix(FormatMessage(n, alertproto.AlertRequest{State: alertproto.OK, Title: "ok", At: at}), "🟢") {
		t.Error("ok icon")
	}
	if code := NewCode(); len(code) != 8 || strings.ContainsAny(code, "01OI") {
		t.Errorf("code = %q", code)
	}
}
