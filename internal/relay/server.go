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
	CodeTTL        = 10 * time.Minute
	ReminderEvery  = 10 * time.Minute
	MaxBody        = 64 << 10
	nodeRatePerMin = 60
	// admitRatePerMin bounds unauthenticated node requests per client IP.
	// A host running several nodes sends 2/min each.
	admitRatePerMin  = 120
	pairRatePerMin   = 10
	chatRatePerMin   = 20
	defaultSilentGap = 5 * time.Minute
)

// Options tunes a Server.
type Options struct {
	SilentAfter time.Duration
	PublicURL   string
	Now         func() time.Time
	// TrustedProxies lists CIDRs of reverse proxies whose X-Forwarded-For
	// header is honoured. Empty means the header is ignored.
	TrustedProxies []*net.IPNet
}

// Server holds the relay state.
type Server struct {
	store Store
	tg    Sender
	opts  Options

	nodeLimit  *rateLimiter
	admitLimit *rateLimiter
	pairLimit  *rateLimiter
	chatLimit  *rateLimiter

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
		admitLimit: newRateLimiter(admitRatePerMin, opts.Now),
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

// clientIP returns the peer address, or, when the peer is a trusted proxy,
// the right-most X-Forwarded-For entry that is not itself a trusted proxy.
// Anything a client could have forged on the left is never used.
func (s *Server) clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	if len(s.opts.TrustedProxies) == 0 || !s.trusted(host) {
		return host
	}
	parts := strings.Split(r.Header.Get("X-Forwarded-For"), ",")
	for i := len(parts) - 1; i >= 0; i-- {
		ip := strings.TrimSpace(parts[i])
		if ip == "" {
			continue
		}
		if !s.trusted(ip) {
			return ip
		}
	}
	return host
}

func (s *Server) trusted(ip string) bool {
	parsed := net.ParseIP(ip)
	if parsed == nil {
		return false
	}
	for _, n := range s.opts.TrustedProxies {
		if n.Contains(parsed) {
			return true
		}
	}
	return false
}

// ParseCIDRs parses a comma-separated list of CIDRs or single addresses.
func ParseCIDRs(list string) ([]*net.IPNet, error) {
	var out []*net.IPNet
	for _, item := range strings.Split(list, ",") {
		item = strings.TrimSpace(item)
		if item == "" {
			continue
		}
		if !strings.Contains(item, "/") {
			if strings.Contains(item, ":") {
				item += "/128"
			} else {
				item += "/32"
			}
		}
		_, n, err := net.ParseCIDR(item)
		if err != nil {
			return nil, fmt.Errorf("bad trusted proxy %q: %w", item, err)
		}
		out = append(out, n)
	}
	return out, nil
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
	if !s.pairLimit.allow(s.clientIP(r)) {
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
		// Before authentication the only key the caller cannot choose is
		// its address, so admission is limited per IP; the per-node limit
		// applies once the node is known.
		if !s.admitLimit.allow(s.clientIP(r)) {
			writeError(w, http.StatusTooManyRequests, "rate limited")
			return
		}
		id := r.Header.Get(alertproto.HeaderNode)
		if !validNodeID(id) {
			writeError(w, http.StatusUnauthorized, "missing node id")
			return
		}
		body, ok := readBody(w, r)
		if !ok {
			return
		}
		ctx := r.Context()
		node, err := s.store.GetNode(ctx, id)
		if errors.Is(err, ErrNotFound) {
			writeError(w, http.StatusUnauthorized, alertproto.UnknownNodeMessage)
			return
		}
		if err != nil {
			writeError(w, http.StatusInternalServerError, "store error")
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
		// Only a request proven to come from the node counts against it.
		if !s.nodeLimit.allow(node.ID) {
			writeError(w, http.StatusTooManyRequests, "rate limited")
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
			if err := s.deliver(ctx, node, alertproto.AlertRequest{Alert: "node_silent", State: alertproto.OK, Severity: info.Severity, Title: info.OKTitle, At: now}); err != nil {
				// Leave it flagged so the recovery is retried on the next request.
				node.Silent = true
				_ = s.store.UpdateNode(ctx, node)
			}
		}
		h(w, r, node, body)
	}
}

func (s *Server) handleAlert(w http.ResponseWriter, r *http.Request, node Node, body []byte) {
	var req alertproto.AlertRequest
	if err := json.Unmarshal(body, &req); err != nil || !alertproto.ValidAlert(req) {
		writeError(w, http.StatusBadRequest, "bad alert")
		return
	}
	if req.At.IsZero() {
		req.At = s.opts.Now()
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), 30*time.Second)
	defer cancel()
	if err := s.deliver(ctx, node, req); err != nil {
		// Tell the node so it retries on its next sample. The cause stays in
		// the relay log: transport errors are internal detail.
		slog.Warn("alert delivery failed", "node", node.ID, "alert", req.Alert, "err", err)
		writeError(w, http.StatusBadGateway, "delivery failed")
		return
	}
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

// deliver applies the delivery rules and sends the alert to the node's
// chat. The (alert, state) is recorded as sent only after Telegram accepted
// it, or when it was intentionally muted, so a failed send is retried by
// the next transition or reminder. It returns an error only for a failed
// send; suppressed or muted deliveries return nil.
func (s *Server) deliver(ctx context.Context, node Node, a alertproto.AlertRequest) error {
	now := s.opts.Now()
	if a.State == alertproto.Info {
		return s.notify(ctx, node, FormatMessage(node, a))
	}
	lastState, lastAt, err := s.store.LastSent(ctx, node.ID, a.Alert)
	if err != nil {
		return fmt.Errorf("last sent lookup: %w", err)
	}
	unchanged := a.State == lastState
	switch {
	case unchanged && a.State == alertproto.OK:
		return nil // ok after ok: nothing new
	case unchanged && now.Sub(lastAt) < ReminderEvery:
		return nil // still firing, too soon for a reminder
	case a.State == alertproto.OK && lastState == "":
		return nil // never fired as far as the relay knows
	}
	muted, err := s.store.Muted(ctx, node.ID, a.Alert, now)
	if err != nil {
		return fmt.Errorf("mute lookup: %w", err)
	}
	if !muted {
		if err := s.notify(ctx, node, FormatMessage(node, a)); err != nil {
			return err
		}
	}
	if err := s.store.SetLastSent(ctx, node.ID, a.Alert, a.State, now); err != nil {
		// Without the record a later recovery would be suppressed; report the
		// failure so the node retries this transition.
		return fmt.Errorf("record last sent: %w", err)
	}
	return nil
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

// clearBlocked resets the blocked flag on every node of a chat; called
// when the chat proves it can talk to the bot again.
func (s *Server) clearBlocked(ctx context.Context, chatID int64) {
	nodes, err := s.store.ListNodes(ctx, chatID)
	if err != nil {
		return
	}
	for _, n := range nodes {
		if n.Blocked {
			n.Blocked = false
			_ = s.store.UpdateNode(ctx, n)
		}
	}
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
		err := s.deliver(ctx, n, alertproto.AlertRequest{Alert: "node_silent", State: alertproto.Firing, Severity: info.Severity, Title: info.Title,
			Detail: fmt.Sprintf("no heartbeat for %s (last seen %s)", now.Sub(n.LastSeen).Round(time.Second), n.LastSeen.UTC().Format("15:04:05 UTC")), At: now})
		if err != nil {
			slog.Warn("node_silent delivery failed; will retry", "node", n.Name, "err", err)
			continue // stays a candidate for the next tick
		}
		n.Silent = true
		if err := s.store.UpdateNode(ctx, n); err != nil {
			slog.Error("mark silent failed", "node", n.Name, "err", err)
		}
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
	mu        sync.Mutex
	limit     int
	now       func() time.Time
	lastSweep time.Time
	counts    map[string]struct {
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

// limiterMaxKeys bounds a limiter's memory. Expired keys are swept at most
// once a minute; a new key arriving while the map is full is refused, so an
// attacker cycling keys cannot grow the map or force a scan per request.
const limiterMaxKeys = 10000

// allow implements a fixed one-minute window per key.
func (r *rateLimiter) allow(key string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	now := r.now()
	c, known := r.counts[key]
	if !known && len(r.counts) >= limiterMaxKeys {
		if now.Sub(r.lastSweep) >= time.Minute {
			r.lastSweep = now
			for k, v := range r.counts {
				if now.Sub(v.window) >= time.Minute {
					delete(r.counts, k)
				}
			}
		}
		if len(r.counts) >= limiterMaxKeys {
			return false
		}
	}
	if now.Sub(c.window) >= time.Minute {
		c.window, c.n = now, 0
	}
	if c.n >= r.limit {
		r.counts[key] = c
		return false
	}
	c.n++
	r.counts[key] = c
	return true
}

// validNodeID accepts the ids newNodeID produces: "n_" and 16 hex digits.
func validNodeID(id string) bool {
	if len(id) != 18 || id[:2] != "n_" {
		return false
	}
	for _, c := range id[2:] {
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}
