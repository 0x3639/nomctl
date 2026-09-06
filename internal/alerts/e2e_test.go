package alerts

import (
	"context"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/0x3639/nomctl/internal/alertproto"
	"github.com/0x3639/nomctl/internal/metrics"
	"github.com/0x3639/nomctl/internal/relay"
)

// chatSink is a relay.Sender that records messages per chat.
type chatSink struct {
	mu   sync.Mutex
	msgs map[int64][]string
}

func (c *chatSink) Send(_ context.Context, chatID int64, text string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.msgs == nil {
		c.msgs = map[int64][]string{}
	}
	c.msgs[chatID] = append(c.msgs[chatID], text)
	return nil
}

func (c *chatSink) get(chat int64) []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]string{}, c.msgs[chat]...)
}

// TestEndToEnd runs a real relay in-process: two chats press /start, one
// node pairs with chat 1, goes down and recovers. Chat 1 gets the story,
// chat 2 gets nothing but its own pairing code.
func TestEndToEnd(t *testing.T) {
	now := base
	clock := func() time.Time { return now }
	sink := &chatSink{}
	srv := relay.NewServer(relay.NewMemoryStore(), sink, relay.Options{Now: clock})
	httpSrv := httptest.NewServer(srv.Handler())
	defer httpSrv.Close()
	ctx := context.Background()

	for _, chat := range []int64{1, 2} {
		if err := srv.HandleUpdate(ctx, relay.Update{ChatID: chat, Text: "/start"}); err != nil {
			t.Fatal(err)
		}
	}
	reply := sink.get(1)[0]
	i := strings.Index(reply, "`")
	code := reply[i+1 : i+9]

	resp, err := Pair(ctx, httpSrv.URL, alertproto.PairRequest{Code: code, Name: "pillar-1", Host: "vps1", Version: "test"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Pair(ctx, httpSrv.URL, alertproto.PairRequest{Code: code, Name: "again", Host: "vps1"}); err == nil {
		t.Error("code reuse must fail")
	}

	cfg := DefaultConfig()
	cfg.RelayURL, cfg.NodeID, cfg.Secret, cfg.Name = httpSrv.URL, resp.NodeID, resp.Secret, "pillar-1"
	cfgPath := filepath.Join(t.TempDir(), "alerts.json")
	if err := cfg.Save(cfgPath); err != nil {
		t.Fatal(err)
	}
	client, err := NewClient(cfg, "test")
	if err != nil {
		t.Fatal(err)
	}
	client.Now = clock
	script := &scriptedSampler{now: clock, samples: []func(time.Time) metrics.Sample{healthy, healthy, downSample, downSample, downSample, healthy, healthy}}
	d := NewDaemon(cfg, cfgPath, filepath.Join(t.TempDir(), "state.json"), script, client)
	d.now = clock

	for range 7 {
		d.Step(ctx)
		now = now.Add(30 * time.Second)
	}

	msgs := sink.get(1)
	// pairing code, paired notice, alerts started, service down, service back up
	if len(msgs) != 5 {
		t.Fatalf("chat 1 got %d messages:\n%s", len(msgs), strings.Join(msgs, "\n---\n"))
	}
	if !strings.Contains(msgs[1], "paired") || !strings.Contains(msgs[2], "alerts started") ||
		!strings.Contains(msgs[3], "🔴") || !strings.Contains(msgs[3], "service down") || !strings.Contains(msgs[3], "pillar\\-1") ||
		!strings.Contains(msgs[4], "🟢") || !strings.Contains(msgs[4], "service back up") {
		t.Errorf("unexpected story:\n%s", strings.Join(msgs, "\n---\n"))
	}
	if other := sink.get(2); len(other) != 1 {
		t.Errorf("chat 2 must only have its pairing code, got %v", other)
	}

	// The relay reports the node silent once heartbeats stop, and its
	// recovery when they resume.
	now = now.Add(6 * time.Minute)
	if err := srv.CheckSilent(ctx); err != nil {
		t.Fatal(err)
	}
	if msgs := sink.get(1); len(msgs) != 6 || !strings.Contains(msgs[5], "node silent") {
		t.Errorf("silent: %v", msgs)
	}
	d.Step(ctx)
	if msgs := sink.get(1); len(msgs) != 7 || !strings.Contains(msgs[6], "reporting again") {
		t.Errorf("recovery: %v", msgs)
	}

	// Unpairing from Telegram makes the daemon stop.
	if err := srv.HandleUpdate(ctx, relay.Update{ChatID: 1, Text: "/unpair pillar-1"}); err != nil {
		t.Fatal(err)
	}
	now = now.Add(30 * time.Second)
	d.Step(ctx)
	if !d.state.Unpaired {
		t.Error("daemon must notice it was unpaired")
	}
}
