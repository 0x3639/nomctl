package alerts

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/0x3639/nomctl/internal/alertproto"
)

// ErrUnpaired is returned when the relay rejects the node's credentials.
var ErrUnpaired = errors.New("relay rejected the node credentials (unpaired?)")

// Client talks to the relay on behalf of a paired node.
type Client struct {
	URL     string
	NodeID  string
	Secret  []byte
	Version string
	HTTP    *http.Client
	Now     func() time.Time
}

// NewClient builds a client from a paired config.
func NewClient(cfg Config, version string) (*Client, error) {
	secret, err := base64.StdEncoding.DecodeString(cfg.Secret)
	if err != nil {
		return nil, fmt.Errorf("decode node secret: %w", err)
	}
	return &Client{
		URL:     strings.TrimRight(cfg.RelayURL, "/"),
		NodeID:  cfg.NodeID,
		Secret:  secret,
		Version: version,
		HTTP:    &http.Client{Timeout: 15 * time.Second},
		Now:     time.Now,
	}, nil
}

// Pair redeems a pairing code. It is the only unauthenticated request.
func Pair(ctx context.Context, relayURL string, req alertproto.PairRequest) (alertproto.PairResponse, error) {
	var resp alertproto.PairResponse
	body, err := json.Marshal(req)
	if err != nil {
		return resp, err
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(relayURL, "/")+"/v1/pair", bytes.NewReader(body))
	if err != nil {
		return resp, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	res, err := (&http.Client{Timeout: 15 * time.Second}).Do(httpReq)
	if err != nil {
		return resp, fmt.Errorf("relay %s: %w", relayURL, err)
	}
	defer func() { _ = res.Body.Close() }()
	data, _ := io.ReadAll(io.LimitReader(res.Body, 64<<10))
	if res.StatusCode != http.StatusOK {
		return resp, fmt.Errorf("pairing failed: %s", relayError(res.StatusCode, data))
	}
	if err := json.Unmarshal(data, &resp); err != nil {
		return resp, fmt.Errorf("pairing failed: bad response: %w", err)
	}
	if resp.NodeID == "" || resp.Secret == "" {
		return resp, errors.New("pairing failed: relay returned no credentials")
	}
	return resp, nil
}

// relayError extracts the error message from a JSON error body.
func relayError(code int, data []byte) string {
	var e alertproto.ErrorResponse
	if json.Unmarshal(data, &e) == nil && e.Error != "" {
		return e.Error
	}
	return fmt.Sprintf("HTTP %d", code)
}

func (c *Client) post(ctx context.Context, path string, v any) error {
	body, err := json.Marshal(v)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.URL+path, bytes.NewReader(body))
	if err != nil {
		return err
	}
	ts := c.Now().Unix()
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(alertproto.HeaderNode, c.NodeID)
	req.Header.Set(alertproto.HeaderTimestamp, strconv.FormatInt(ts, 10))
	req.Header.Set(alertproto.HeaderSignature, alertproto.Sign(c.Secret, ts, body))
	res, err := c.HTTP.Do(req)
	if err != nil {
		return fmt.Errorf("relay: %w", err)
	}
	defer func() { _ = res.Body.Close() }()
	data, _ := io.ReadAll(io.LimitReader(res.Body, 64<<10))
	switch {
	case res.StatusCode == http.StatusUnauthorized && relayError(res.StatusCode, data) == alertproto.UnknownNodeMessage:
		return ErrUnpaired
	case res.StatusCode >= 200 && res.StatusCode < 300:
		return nil
	default:
		// Includes 401s for clock skew or a bad signature: transient from the
		// node's point of view, retried on the next step.
		return fmt.Errorf("relay %s: %s", path, relayError(res.StatusCode, data))
	}
}

// Alert reports a state change.
func (c *Client) Alert(ctx context.Context, a alertproto.AlertRequest) error {
	return c.post(ctx, "/v1/alert", a)
}

// Heartbeat reports liveness and a summary.
func (c *Client) Heartbeat(ctx context.Context, h alertproto.HeartbeatRequest) error {
	return c.post(ctx, "/v1/heartbeat", h)
}

// Unpair asks the relay to forget this node.
func (c *Client) Unpair(ctx context.Context) error {
	return c.post(ctx, "/v1/unpair", struct{}{})
}
