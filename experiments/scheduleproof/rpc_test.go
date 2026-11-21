package scheduleproof

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

func TestWebSocketCancellationDuringUpgrade(t *testing.T) {
	arrived, release := make(chan struct{}), make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(arrived)
		select {
		case <-release:
		case <-r.Context().Done():
		}
	}))
	defer server.Close()
	src, err := NewRPCSource("ws" + strings.TrimPrefix(server.URL, "http"))
	if err != nil {
		t.Fatal(err)
	}
	defer src.Close()
	defer close(release)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { _, err := src.Frontier(ctx); done <- err }()
	<-arrived
	cancel()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("stalled upgrade succeeded")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("caller cancellation did not interrupt upgrade")
	}
}

type decodingResult struct{ decode func() error }

func (r *decodingResult) UnmarshalJSON([]byte) error { return r.decode() }

func TestWebSocketCanceledDuringDecodeIsNotReused(t *testing.T) {
	peerClosed := make(chan struct{})
	var connections atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := (&websocket.Upgrader{}).Upgrade(w, r, nil)
		if err != nil {
			t.Error(err)
			return
		}
		defer c.Close()
		first := connections.Add(1) == 1
		for {
			_, raw, err := c.ReadMessage()
			if err != nil {
				if first {
					close(peerClosed)
				}
				return
			}
			var q struct{ ID uint64 }
			if err = json.Unmarshal(raw, &q); err != nil {
				t.Error(err)
				return
			}
			if err = c.WriteJSON(map[string]interface{}{"id": q.ID, "result": map[string]int{"height": 1}}); err != nil {
				t.Error(err)
				return
			}
		}
	}))
	defer server.Close()
	src, err := NewRPCSource("ws" + strings.TrimPrefix(server.URL, "http"))
	if err != nil {
		t.Fatal(err)
	}
	defer src.Close()
	// A normal request must leave a reusable connection.
	if _, err = src.Frontier(context.Background()); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	result := &decodingResult{decode: func() error {
		cancel()
		select {
		case <-peerClosed:
			return nil // callback ran, but decoding still succeeded
		case <-time.After(3 * time.Second):
			return context.DeadlineExceeded
		}
	}}
	if err = src.call(ctx, "ledger.getFrontierMomentum", nil, result); err != nil {
		t.Fatal(err)
	}
	if src.conn != nil || connections.Load() != 1 {
		t.Fatal("canceled socket retained, or normal connection not reused")
	}
	if _, err = src.Frontier(context.Background()); err != nil {
		t.Fatalf("next call must reconnect without a failed retry: %v", err)
	}
	if connections.Load() != 2 {
		t.Fatal("expected one replacement connection")
	}
}
