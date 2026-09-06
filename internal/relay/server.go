package relay

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/0x3639/nomctl/internal/alertproto"
)

// Version of the relay, set via ldflags.
var Version = "dev"

// Tunables.
const (
	CodeTTL          = 10 * time.Minute
	ReminderEvery    = 10 * time.Minute
	MaxBody          = 64 << 10
	nodeRatePerMin   = 60
	pairRatePerMin   = 10
	chatRatePerMin   = 20
	defaultSilentGap = 5 * time.Minute
)

// Options tunes a Server.
type Options struct {
	SilentAfter time.Duration
	PublicURL   string
	Now         func() time.Time
}

// Server holds the relay state.
type Server struct {
	store Store
	tg    Sender
	opts  Options

	nodeLimit *rateLimiter
	pairLimit *rateLimiter
	chatLimit *rateLimiter

	suppressedMu sync.Mutex
	suppressed   map[int64]int // per chat, messages dropped in the current minute
}

// NewServer wires a relay.
func NewServer(store Store, tg Sender, opts Options) *Server {
	if opts.Now == nil {
		opts.Now = time.Now
	}
	if opts.SilentAfter == 0 {
		opts.SilentAfter = defaultSilentGap
	}
	return &Server{
		store:      store,
		tg:         tg,
		opts:       opts,
		nodeLimit:  newRateLimiter(nodeRatePerMin, opts.Now),
		pairLimit:  newRateLimiter(pairRatePerMin, opts.Now),
		chatLimit:  newRateLimiter(chatRatePerMin, opts.Now),
		suppressed: map[int64]int{},
	}
}

// Handler returns the HTTP routes.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("ok\n")) })
	mux.HandleFunc("POST /v1/pair", s.handlePair)
	mux.HandleFunc("POST /v1/alert", s.authed(s.handleAlert))
	mux.HandleFunc("POST /v1/heartbeat", s.authed(s.handleHeartbeat))
	mux.HandleFunc("POST /v1/unpair", s.authed(s.handleUnpair))
	return mux
}

func writeError(w http.ResponseWriter, code int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(alertproto.ErrorResponse{Error: msg})
}

func readBody(w http.ResponseWriter, r *http.Request) ([]byte, bool) {
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, MaxBody))
	if err != nil {
		writeError(w, http.StatusRequestEntityTooLarge, "body too large")
		return nil, false
	}
	return body, true
}

func clientIP(r *http.Request) string {
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		return strings.TrimSpace(strings.Split(xff, ",")[0])
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

// --- pairing ---------------------------------------------------------------

const codeAlphabet = "ABCDEFGHJKLMNPQRSTUVWXYZ23456789"

// NewCode returns an 8 character pairing code from an unambiguous alphabet.
func NewCode() string {
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	out := make([]byte, 8)
	for i := range b {
		out[i] = codeAlphabet[int(b[i])%len(codeAlphabet)]
	}
	return string(out)
}

func newSecret() []byte {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return b
}

func newNodeID() string {
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return "n_" + hex.EncodeToString(b)
}

func validName(name string) bool {
	if name == "" || len(name) > 32 {
		return false
	}
	for _, r := range name {
		ok := r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_' || r == '.'
		if !ok {
			return false
		}
	}
	return true
}

func (s *Server) handlePair(w http.ResponseWriter, r *http.Request) {
	if !s.pairLimit.allow(clientIP(r)) {
		writeError(w, http.StatusTooManyRequests, "too many pairing attempts; try again in a minute")
		return
	}
	body, ok := readBody(w, r)
	if !ok {
		return
	}
	var req alertproto.PairRequest
	if err := json.Unmarshal(body, &req); err != nil {
		writeError(w, http.StatusBadRequest, "bad json")
		return
	}
	req.Code = strings.ToUpper(strings.TrimSpace(req.Code))
	if !validName(req.Name) {
		writeError(w, http.StatusBadRequest, "name must be 1-32 characters: letters, digits, '-', '_' or '.'")
		return
	}
	ctx := r.Context()
	now := s.opts.Now()
	chatID, err := s.store.ConsumeCode(ctx, req.Code, now)
	if err != nil {
		if errors.Is(err, ErrCodeInvalid) {
			writeError(w, http.StatusBadRequest, "pairing code is invalid or expired; send /start to the bot for a new one")
			return
		}
		writeError(w, http.StatusInternalServerError, "store error")
		return
	}
	node := Node{ID: newNodeID(), ChatID: chatID, Name: req.Name, Host: req.Host, Version: req.Version, Secret: newSecret(), Created: now, LastSeen: now}
	if err := s.store.CreateNode(ctx, node); err != nil {
		if errors.Is(err, ErrNameTaken) {
			writeError(w, http.StatusConflict, fmt.Sprintf("a node named %q is already paired to this chat; choose another name (the code was consumed, send /start again)", req.Name))
			return
		}
		writeError(w, http.StatusInternalServerError, "store error")
		return
	}
	slog.Info("node paired", "node", node.ID, "name", node.Name, "chat", chatID)
	_ = s.notify(ctx, node, fmt.Sprintf("ℹ️ %s paired and reporting from host %s\\.", Bold(node.Name), Code(node.Host)))
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(alertproto.PairResponse{NodeID: node.ID, Secret: base64.StdEncoding.EncodeToString(node.Secret), RelayVersion: Version})
}

// --- authenticated node requests ------------------------------------------

type nodeHandler func(w http.ResponseWriter, r *http.Request, node Node, body []byte)

func (s *Server) authed(h nodeHandler) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := r.Header.Get(alertproto.HeaderNode)
		if id == "" {
			writeError(w, http.StatusUnauthorized, "missing node id")
			return
		}
		if !s.nodeLimit.allow(id) {
			writeError(w, http.StatusTooManyRequests, "rate limited")
			return
		}
		body, ok := readBody(w, r)
		if !ok {
			return
		}
		ctx := r.Context()
		node, err := s.store.GetNode(ctx, id)
		if err != nil {
			writeError(w, http.StatusUnauthorized, "unknown node")
			return
		}
		ts, err := strconv.ParseInt(r.Header.Get(alertproto.HeaderTimestamp), 10, 64)
		if err != nil {
			writeError(w, http.StatusUnauthorized, "bad timestamp")
			return
		}
		if err := alertproto.Verify(node.Secret, ts, body, r.Header.Get(alertproto.HeaderSignature), s.opts.Now()); err != nil {
			writeError(w, http.StatusUnauthorized, err.Error())
			return
		}
		now := s.opts.Now()
		wasSilent := node.Silent
		node.LastSeen = now
		node.Silent = false
		if err := s.store.UpdateNode(ctx, node); err != nil {
			writeError(w, http.StatusInternalServerError, "store error")
			return
		}
		if wasSilent {
			info, _ := alertproto.Lookup("node_silent")
			s.deliver(ctx, node, alertproto.AlertRequest{Alert: "node_silent", State: alertproto.OK, Severity: info.Severity, Title: info.OKTitle, At: now})
		}
		h(w, r, node, body)
	}
}

func (s *Server) handleAlert(w http.ResponseWriter, _ *http.Request, node Node, body []byte) {
	var req alertproto.AlertRequest
	if err := json.Unmarshal(body, &req); err != nil || req.Alert == "" {
		writeError(w, http.StatusBadRequest, "bad alert")
		return
	}
	if req.At.IsZero() {
		req.At = s.opts.Now()
	}
	s.deliver(context.WithoutCancel(context.Background()), node, req)
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleHeartbeat(w http.ResponseWriter, r *http.Request, node Node, body []byte) {
	var req alertproto.HeartbeatRequest
	if err := json.Unmarshal(body, &req); err != nil {
		writeError(w, http.StatusBadRequest, "bad heartbeat")
		return
	}
	node.Summary = req.Summary
	if err := s.store.UpdateNode(r.Context(), node); err != nil {
		writeError(w, http.StatusInternalServerError, "store error")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleUnpair(w http.ResponseWriter, r *http.Request, node Node, _ []byte) {
	if err := s.store.DeleteNode(r.Context(), node.ID); err != nil && !errors.Is(err, ErrNotFound) {
		writeError(w, http.StatusInternalServerError, "store error")
		return
	}
	slog.Info("node unpaired", "node", node.ID, "name", node.Name)
	_ = s.notify(r.Context(), node, fmt.Sprintf("ℹ️ %s unpaired\\.", Bold(node.Name)))
	w.WriteHeader(http.StatusNoContent)
}

// --- delivery --------------------------------------------------------------

// deliver applies the delivery rules and sends the alert to the node's chat.
func (s *Server) deliver(ctx context.Context, node Node, a alertproto.AlertRequest) {
	now := s.opts.Now()
	if a.State != alertproto.Info {
		lastState, lastAt, err := s.store.LastSent(ctx, node.ID, a.Alert)
		if err != nil {
			slog.Error("last sent lookup failed", "err", err)
			return
		}
		unchanged := a.State == lastState
		switch {
		case unchanged && a.State == alertproto.OK:
			return // ok after ok: nothing new
		case unchanged && now.Sub(lastAt) < ReminderEvery:
			return // still firing, too soon for a reminder
		case a.State == alertproto.OK && lastState == "":
			return // never fired as far as the relay knows
		}
		if err := s.store.SetLastSent(ctx, node.ID, a.Alert, a.State, now); err != nil {
			slog.Error("set last sent failed", "err", err)
		}
		muted, err := s.store.Muted(ctx, node.ID, a.Alert, now)
		if err == nil && muted {
			return
		}
	}
	_ = s.notify(ctx, node, FormatMessage(node, a))
}

// notify sends text to the node's chat, applying the per-chat rate limit.
func (s *Server) notify(ctx context.Context, node Node, text string) error {
	if node.Blocked {
		return ErrBlocked
	}
	chatKey := strconv.FormatInt(node.ChatID, 10)
	if !s.chatLimit.allow(chatKey) {
		s.suppressedMu.Lock()
		s.suppressed[node.ChatID]++
		n := s.suppressed[node.ChatID]
		s.suppressedMu.Unlock()
		if n == 1 {
			// One notice per burst, sent outside the limiter.
			go func() {
				time.Sleep(time.Minute)
				s.suppressedMu.Lock()
				count := s.suppressed[node.ChatID]
				delete(s.suppressed, node.ChatID)
				s.suppressedMu.Unlock()
				_ = s.tg.Send(context.WithoutCancel(ctx), node.ChatID, Escape(fmt.Sprintf("⚠️ %d alerts suppressed in the last minute (rate limit).", count)))
			}()
		}
		return errors.New("chat rate limited")
	}
	err := s.tg.Send(ctx, node.ChatID, text)
	if errors.Is(err, ErrBlocked) {
		node.Blocked = true
		_ = s.store.UpdateNode(ctx, node)
		slog.Warn("chat blocked the bot", "chat", node.ChatID, "node", node.Name)
	} else if err != nil {
		slog.Error("telegram send failed", "err", err, "node", node.Name)
	}
	return err
}

// FormatMessage renders an alert for Telegram (MarkdownV2).
func FormatMessage(node Node, a alertproto.AlertRequest) string {
	icon := "ℹ️"
	switch {
	case a.State == alertproto.OK:
		icon = "🟢"
	case a.State == alertproto.Firing && a.Severity == alertproto.Critical:
		icon = "🔴"
	case a.State == alertproto.Firing:
		icon = "🟠"
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%s %s · %s", icon, Bold(node.Name), Escape(a.Title))
	if a.Detail != "" {
		b.WriteString("\n" + Escape(a.Detail))
	}
	fmt.Fprintf(&b, "\n%s", Escape(fmt.Sprintf("host %s · %s", node.Host, a.At.UTC().Format("2006-01-02 15:04 UTC"))))
	return b.String()
}

// --- node silence ----------------------------------------------------------

// CheckSilent raises node_silent for nodes without a recent heartbeat.
func (s *Server) CheckSilent(ctx context.Context) error {
	now := s.opts.Now()
	nodes, err := s.store.SilentCandidates(ctx, now.Add(-s.opts.SilentAfter))
	if err != nil {
		return err
	}
	info, _ := alertproto.Lookup("node_silent")
	for _, n := range nodes {
		n.Silent = true
		if err := s.store.UpdateNode(ctx, n); err != nil {
			continue
		}
		s.deliver(ctx, n, alertproto.AlertRequest{Alert: "node_silent", State: alertproto.Firing, Severity: info.Severity, Title: info.Title,
			Detail: fmt.Sprintf("no heartbeat for %s (last seen %s)", now.Sub(n.LastSeen).Round(time.Second), n.LastSeen.UTC().Format("15:04:05 UTC")), At: now})
	}
	return nil
}

// RunSilentTicker calls CheckSilent every interval until ctx is done.
func (s *Server) RunSilentTicker(ctx context.Context, every time.Duration) {
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if err := s.CheckSilent(ctx); err != nil {
				slog.Error("silent check failed", "err", err)
			}
		}
	}
}

// --- rate limiting -----------------------------------------------------------

type rateLimiter struct {
	mu     sync.Mutex
	limit  int
	now    func() time.Time
	counts map[string]struct {
		window time.Time
		n      int
	}
}

func newRateLimiter(perMinute int, now func() time.Time) *rateLimiter {
	return &rateLimiter{limit: perMinute, now: now, counts: map[string]struct {
		window time.Time
		n      int
	}{}}
}

// allow implements a fixed one-minute window per key.
func (r *rateLimiter) allow(key string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	now := r.now()
	c := r.counts[key]
	if now.Sub(c.window) >= time.Minute {
		c.window, c.n = now, 0
	}
	if c.n >= r.limit {
		r.counts[key] = c
		return false
	}
	c.n++
	r.counts[key] = c
	if len(r.counts) > 10000 {
		for k, v := range r.counts {
			if now.Sub(v.window) >= time.Minute {
				delete(r.counts, k)
			}
		}
	}
	return true
}
