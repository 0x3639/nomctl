package relay

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeBotAPI records sendMessage calls and serves scripted responses.
type fakeBotAPI struct {
	mu       sync.Mutex
	sent     []sentMessage
	failNext []int // status codes to return before succeeding
	updates  string
	srv      *httptest.Server
}

type sentMessage struct {
	ChatID int64
	Text   string
}

func newFakeBotAPI(t *testing.T) *fakeBotAPI {
	t.Helper()
	f := &fakeBotAPI{updates: `[]`}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		if !strings.HasPrefix(r.URL.Path, "/bottoken/") {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		if len(f.failNext) > 0 {
			code := f.failNext[0]
			f.failNext = f.failNext[1:]
			w.WriteHeader(code)
			_, _ = w.Write([]byte(`{"ok":false,"description":"scripted","parameters":{"retry_after":1}}`))
			return
		}
		switch strings.TrimPrefix(r.URL.Path, "/bottoken/") {
		case "sendMessage":
			var p struct {
				ChatID int64  `json:"chat_id"`
				Text   string `json:"text"`
				Mode   string `json:"parse_mode"`
			}
			_ = json.NewDecoder(r.Body).Decode(&p)
			if p.Mode != "MarkdownV2" {
				w.WriteHeader(http.StatusBadRequest)
				_, _ = w.Write([]byte(`{"ok":false,"description":"bad parse mode"}`))
				return
			}
			f.sent = append(f.sent, sentMessage{ChatID: p.ChatID, Text: p.Text})
			_, _ = w.Write([]byte(`{"ok":true,"result":{}}`))
		case "getUpdates":
			_, _ = w.Write([]byte(`{"ok":true,"result":` + f.updates + `}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeBotAPI) client() *Telegram {
	tg := NewTelegram("token")
	tg.BaseURL = f.srv.URL
	tg.Sleep = func(time.Duration) {}
	return tg
}

func (f *fakeBotAPI) messages() []sentMessage {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]sentMessage{}, f.sent...)
}

func TestTelegramSend(t *testing.T) {
	api := newFakeBotAPI(t)
	tg := api.client()
	if err := tg.Send(context.Background(), 42, Escape("hello.")); err != nil {
		t.Fatal(err)
	}
	if got := api.messages(); len(got) != 1 || got[0].ChatID != 42 || got[0].Text != `hello\.` {
		t.Errorf("sent = %+v", got)
	}
	api.failNext = []int{403}
	if err := tg.Send(context.Background(), 42, "x"); !errors.Is(err, ErrBlocked) {
		t.Errorf("403 = %v", err)
	}
	api.failNext = []int{429, 500}
	if err := tg.Send(context.Background(), 42, "retry"); err != nil {
		t.Errorf("retries after 429 and 500: %v", err)
	}
	if got := api.messages(); len(got) != 2 || got[1].Text != "retry" {
		t.Errorf("after retries: %+v", got)
	}
	api.failNext = []int{500, 500, 500, 500}
	if err := tg.Send(context.Background(), 42, "never"); err == nil {
		t.Error("persistent 5xx must fail")
	}
	api.failNext = []int{400}
	if err := tg.Send(context.Background(), 42, "bad"); err == nil || !strings.Contains(err.Error(), "scripted") {
		t.Errorf("4xx is not retried and carries the description: %v", err)
	}
}

func TestTelegramUpdates(t *testing.T) {
	api := newFakeBotAPI(t)
	api.updates = `[{"update_id":10,"message":{"text":"/start","chat":{"id":1},"from":{"username":"alice"}}},{"update_id":11,"message":{"text":"/nodes","chat":{"id":2}}},{"update_id":12}]`
	ups, err := api.client().Updates(context.Background(), 0, time.Second)
	if err != nil || len(ups) != 3 {
		t.Fatalf("updates = %+v %v", ups, err)
	}
	if ups[0].ChatID != 1 || ups[0].Text != "/start" || ups[0].Username != "alice" || ups[1].ChatID != 2 || ups[2].ChatID != 0 || ups[2].UpdateID != 12 {
		t.Errorf("parsed = %+v", ups)
	}
}

func TestEscape(t *testing.T) {
	in := "a_b*c[d]e(f)g~h`i>j#k+l-m=n|o{p}q.r!s\\t"
	out := Escape(in)
	for _, r := range markdownV2Special {
		if !strings.Contains(out, "\\"+string(r)) {
			t.Errorf("%q not escaped in %q", r, out)
		}
	}
	if Escape("plain") != "plain" {
		t.Error("plain text must be unchanged")
	}
	if Code("a`b") != "`a\\`b`" || Bold("x.y") != `*x\.y*` {
		t.Error("Code/Bold")
	}
}
