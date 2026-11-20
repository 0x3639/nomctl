package scheduleproof

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sync"
	"time"

	"github.com/gorilla/websocket"
	"github.com/zenon-network/go-zenon/chain/nom"
)

const maxRPCBytes = 16 << 20

// Source is deliberately restricted to three existing read-only ledger methods.
type Source interface {
	Name() string
	Frontier(context.Context) (*nom.Momentum, error)
	Header(context.Context, uint64) (*nom.Momentum, error)
	Details(context.Context, uint64, uint64) ([]json.RawMessage, error)
}
type RPCSource struct {
	endpoint *url.URL
	mu       sync.Mutex
	conn     *websocket.Conn
	seq      uint64
	Bytes    uint64
	Calls    uint64
}

func NewRPCSource(endpoint string) (*RPCSource, error) {
	u, err := url.Parse(endpoint)
	if err != nil {
		return nil, err
	}
	if u.Host == "" || (u.Scheme != "http" && u.Scheme != "https" && u.Scheme != "ws" && u.Scheme != "wss") {
		return nil, fmt.Errorf("expected an http(s) or ws(s) RPC URL")
	}
	return &RPCSource{endpoint: u}, nil
}
func (s *RPCSource) Name() string {
	u := *s.endpoint
	u.User = nil
	u.RawQuery = ""
	u.Fragment = ""
	return u.String()
}
func (s *RPCSource) Close() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.conn != nil {
		s.conn.Close()
		s.conn = nil
	}
}
func (s *RPCSource) call(ctx context.Context, method string, params []interface{}, result interface{}) (err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	s.seq++
	id := s.seq
	request, _ := json.Marshal(struct {
		JSONRPC string        `json:"jsonrpc"`
		ID      uint64        `json:"id"`
		Method  string        `json:"method"`
		Params  []interface{} `json:"params"`
	}{"2.0", id, method, params})
	var raw []byte
	if s.endpoint.Scheme == "http" || s.endpoint.Scheme == "https" {
		req, e := http.NewRequestWithContext(ctx, "POST", s.endpoint.String(), bytes.NewReader(request))
		if e != nil {
			return e
		}
		req.Header.Set("Content-Type", "application/json")
		res, e := http.DefaultClient.Do(req)
		if e != nil {
			return fmt.Errorf("RPC transport failed")
		}
		defer res.Body.Close()
		if res.StatusCode != http.StatusOK {
			return fmt.Errorf("RPC HTTP status %d", res.StatusCode)
		}
		raw, err = io.ReadAll(io.LimitReader(res.Body, maxRPCBytes+1))
		if err != nil {
			return err
		}
		if len(raw) > maxRPCBytes {
			return fmt.Errorf("RPC response too large")
		}
	} else {
		defer func() {
			if err != nil && s.conn != nil {
				s.conn.Close()
				s.conn = nil
			}
		}()
		if s.conn == nil {
			dialer := websocket.Dialer{HandshakeTimeout: 15 * time.Second}
			c, _, e := dialer.DialContext(ctx, s.endpoint.String(), nil)
			if e != nil {
				return fmt.Errorf("RPC websocket connection failed")
			}
			c.SetReadLimit(maxRPCBytes)
			s.conn = c
		}
		c := s.conn
		stop := context.AfterFunc(ctx, func() { c.Close() })
		defer stop()
		deadline, _ := ctx.Deadline()
		c.SetReadDeadline(deadline)
		c.SetWriteDeadline(deadline)
		if err = c.WriteMessage(websocket.TextMessage, request); err != nil {
			return fmt.Errorf("RPC websocket write failed")
		}
		_, raw, err = c.ReadMessage()
		if err != nil {
			return fmt.Errorf("RPC websocket read failed")
		}
	}
	s.Bytes += uint64(len(raw) + len(request))
	s.Calls++
	var envelope struct {
		ID     uint64          `json:"id"`
		Result json.RawMessage `json:"result"`
		Error  *struct {
			Code int `json:"code"`
		} `json:"error"`
	}
	if err = json.Unmarshal(raw, &envelope); err != nil {
		return fmt.Errorf("invalid RPC response")
	}
	if envelope.ID != id {
		return fmt.Errorf("RPC response ID mismatch")
	}
	if envelope.Error != nil {
		return fmt.Errorf("RPC error %d", envelope.Error.Code)
	}
	if len(envelope.Result) == 0 || string(envelope.Result) == "null" {
		return fmt.Errorf("missing RPC result")
	}
	return json.Unmarshal(envelope.Result, result)
}
func (s *RPCSource) Frontier(ctx context.Context) (*nom.Momentum, error) {
	var m nom.Momentum
	err := s.call(ctx, "ledger.getFrontierMomentum", []interface{}{}, &m)
	m.EnsureCache()
	return &m, err
}
func (s *RPCSource) Header(ctx context.Context, h uint64) (*nom.Momentum, error) {
	var list struct {
		List []*nom.Momentum `json:"list"`
	}
	if err := s.call(ctx, "ledger.getMomentumsByHeight", []interface{}{h, 1}, &list); err != nil {
		return nil, err
	}
	if len(list.List) != 1 || list.List[0] == nil || list.List[0].Height != h {
		return nil, fmt.Errorf("missing requested momentum")
	}
	list.List[0].EnsureCache()
	return list.List[0], nil
}
func (s *RPCSource) Details(ctx context.Context, h, n uint64) ([]json.RawMessage, error) {
	if n == 0 || n > 100 {
		return nil, fmt.Errorf("invalid batch size")
	}
	var list struct {
		List []json.RawMessage `json:"list"`
	}
	err := s.call(ctx, "ledger.getDetailedMomentumsByHeight", []interface{}{h, n}, &list)
	return list.List, err
}
