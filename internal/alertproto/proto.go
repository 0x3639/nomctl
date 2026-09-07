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

// Summary is the node status shown by the /nodes command. The first four
// fields are what nodes before v0.8.0 send; everything else is optional and
// rendered only when present, so old and new nodes share one relay.
type Summary struct {
	State    string `json:"state"`
	Height   uint64 `json:"height"`
	Peers    int    `json:"peers"`
	Restarts int    `json:"restarts"`

	TargetHeight uint64  `json:"target_height,omitempty"`
	MomentumRate float64 `json:"momentum_rate,omitempty"` // momentums per second
	ETASeconds   int64   `json:"eta_seconds,omitempty"`
	// Frontier is the newest momentum; FrontierAgeSeconds how old it is.
	// LedgerBusy is set instead when the ledger call did not answer.
	Frontier           uint64 `json:"frontier,omitempty"`
	FrontierAgeSeconds int64  `json:"frontier_age_seconds,omitempty"`
	LedgerBusy         bool   `json:"ledger_busy,omitempty"`
	UptimeSeconds      int64  `json:"uptime_seconds,omitempty"`

	NodeVersion   string `json:"node_version,omitempty"`
	NodeCommit    string `json:"node_commit,omitempty"`
	NomctlVersion string `json:"nomctl_version,omitempty"`

	PillarName     string `json:"pillar_name,omitempty"`
	PillarRank     int    `json:"pillar_rank,omitempty"`
	PillarProduced uint64 `json:"pillar_produced,omitempty"`
	PillarExpected uint64 `json:"pillar_expected,omitempty"`
	PillarError    string `json:"pillar_error,omitempty"`

	Load1        float64 `json:"load1,omitempty"`
	MemFree      uint64  `json:"mem_free,omitempty"` // bytes available
	MemTotal     uint64  `json:"mem_total,omitempty"`
	DiskFree     uint64  `json:"disk_free,omitempty"` // bytes, data directory
	DiskTotal    uint64  `json:"disk_total,omitempty"`
	CPUPercent   float64 `json:"cpu_percent,omitempty"`
	RSS          uint64  `json:"rss,omitempty"`
	NomctlUpdate string  `json:"nomctl_update,omitempty"` // newer release tag
	NodeUpdate   bool    `json:"node_update,omitempty"`   // branch has new commits
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

// PrivacyNotice is shown by the bot on /start and by nomctl alerts setup
// before pairing. relayHost is the relay's public host name.
func PrivacyNotice(relayHost string) string {
	return "Privacy notice: once paired, your node sends a heartbeat to " + relayHost +
		" every 30 seconds and forwards alerts through it. The relay operator can see your node's public IP address, " +
		"host name, node name and status summary. The IP address is used only to rate-limit pairing and is not stored in " +
		"the relay database, but it is visible to the relay and may appear in the logs of the proxy in front of it. " +
		"If your pillar's IP address must stay private, run your own relay (see the docs) or do not pair."
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
	{Name: "update_available", Severity: InfoSev, Title: "update available", OKTitle: "up to date"},
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
