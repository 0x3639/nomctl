// Package alertproto defines the wire types and request signing shared by
// the node-side alert agent (nomctl alerts) and the relay (nomctl-relay).
package alertproto

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strconv"
	"time"
)

// Request headers carrying node identity and the signature.
const (
	HeaderNode      = "X-Nomctl-Node"
	HeaderTimestamp = "X-Nomctl-Timestamp"
	HeaderSignature = "X-Nomctl-Signature"
)

// ReplayWindow is how far a request timestamp may be from the relay's clock.
const ReplayWindow = 5 * time.Minute

// State of an alert as sent by a node.
type State string

// Alert states.
const (
	Firing State = "firing"
	OK     State = "ok"
	Info   State = "info"
)

// Severity of an alert.
type Severity string

// Severities.
const (
	Critical Severity = "critical"
	Warning  Severity = "warning"
	InfoSev  Severity = "info"
)

// PairRequest is sent once, unauthenticated, to redeem a pairing code.
type PairRequest struct {
	Code    string `json:"code"`
	Name    string `json:"name"`
	Host    string `json:"host"`
	Version string `json:"version"`
}

// PairResponse carries the credentials the node stores.
type PairResponse struct {
	NodeID       string `json:"node_id"`
	Secret       string `json:"secret"`
	RelayVersion string `json:"relay_version"`
}

// AlertRequest reports a state change (or an info message).
type AlertRequest struct {
	Alert    string    `json:"alert"`
	State    State     `json:"state"`
	Severity Severity  `json:"severity"`
	Title    string    `json:"title"`
	Detail   string    `json:"detail"`
	At       time.Time `json:"at"`
}

// Summary is the small node status shown by the /nodes command.
type Summary struct {
	State    string `json:"state"`
	Height   uint64 `json:"height"`
	Peers    int    `json:"peers"`
	Restarts int    `json:"restarts"`
}

// HeartbeatRequest is sent on every sampling iteration.
type HeartbeatRequest struct {
	At      time.Time `json:"at"`
	Summary Summary   `json:"summary"`
}

// ErrorResponse is the JSON body of a failed request.
type ErrorResponse struct {
	Error string `json:"error"`
}

// UnknownNodeMessage is the error body of a 401 that means "this node is
// not paired" (as opposed to a stale timestamp or a bad signature).
const UnknownNodeMessage = "unknown node"

// Signing errors.
var (
	ErrStale        = errors.New("request timestamp outside the replay window")
	ErrBadSignature = errors.New("bad signature")
)

// Sign returns the hex HMAC-SHA256 of timestamp + "\n" + body.
func Sign(secret []byte, timestamp int64, body []byte) string {
	m := hmac.New(sha256.New, secret)
	m.Write([]byte(strconv.FormatInt(timestamp, 10)))
	m.Write([]byte{'\n'})
	m.Write(body)
	return hex.EncodeToString(m.Sum(nil))
}

// Verify checks the timestamp against now and the signature against body.
func Verify(secret []byte, timestamp int64, body []byte, signature string, now time.Time) error {
	ts := time.Unix(timestamp, 0)
	if d := now.Sub(ts); d > ReplayWindow || d < -ReplayWindow {
		return ErrStale
	}
	want, err := hex.DecodeString(Sign(secret, timestamp, body))
	if err != nil {
		return ErrBadSignature
	}
	got, err := hex.DecodeString(signature)
	if err != nil || !hmac.Equal(want, got) {
		return ErrBadSignature
	}
	return nil
}

// AlertInfo describes one alert kind.
type AlertInfo struct {
	Name     string
	Severity Severity
	Title    string
	OKTitle  string
	// Relay is true for alerts raised by the relay rather than the node.
	Relay bool
}

// Alerts lists every alert kind, node-raised first.
var Alerts = []AlertInfo{
	{Name: "service_down", Severity: Critical, Title: "service down", OKTitle: "service back up"},
	{Name: "crash_loop", Severity: Critical, Title: "crash loop", OKTitle: "crash loop ended"},
	{Name: "sync_stalled", Severity: Critical, Title: "sync stalled", OKTitle: "sync resumed"},
	{Name: "sync_behind", Severity: Warning, Title: "sync falling behind", OKTitle: "sync catching up"},
	{Name: "not_enough_peers", Severity: Warning, Title: "not enough peers", OKTitle: "peers ok"},
	{Name: "disk_low", Severity: Warning, Title: "disk space low", OKTitle: "disk space ok"},
	{Name: "memory_high", Severity: Warning, Title: "memory high", OKTitle: "memory ok"},
	{Name: "fds_high", Severity: Warning, Title: "open files high", OKTitle: "open files ok"},
	{Name: "backup_stale", Severity: Warning, Title: "backup overdue", OKTitle: "backup ok"},
	{Name: "rpc_unreachable", Severity: Warning, Title: "node rpc unreachable", OKTitle: "node rpc ok"},
	{Name: "momentums_stalled", Severity: Critical, Title: "momentums stalled", OKTitle: "momentums advancing again"},
	{Name: "pillar_missed", Severity: Critical, Title: "pillar missing momentums", OKTitle: "pillar producing again"},
	{Name: "node_silent", Severity: Critical, Title: "node silent", OKTitle: "node reporting again", Relay: true},
}

// Lookup finds an alert kind by name.
func Lookup(name string) (AlertInfo, bool) {
	for _, a := range Alerts {
		if a.Name == name {
			return a, true
		}
	}
	return AlertInfo{}, false
}
