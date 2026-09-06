package relay

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// DefaultTelegramURL is the Bot API base.
const DefaultTelegramURL = "https://api.telegram.org"

// ErrBlocked is returned when the chat has blocked the bot (HTTP 403).
var ErrBlocked = errors.New("chat has blocked the bot")

// Sender delivers a message to a chat; *Telegram implements it.
type Sender interface {
	Send(ctx context.Context, chatID int64, text string) error
}

// Telegram is a minimal Bot API client.
type Telegram struct {
	Token   string
	BaseURL string
	HTTP    *http.Client
	Sleep   func(time.Duration) // for tests
}

// NewTelegram builds a client for the given bot token.
func NewTelegram(token string) *Telegram {
	return &Telegram{Token: token, BaseURL: DefaultTelegramURL, HTTP: &http.Client{Timeout: 70 * time.Second}}
}

// Update is the part of a Telegram update the relay uses.
type Update struct {
	UpdateID int64
	ChatID   int64
	Text     string
	Username string
}

type apiResponse struct {
	OK          bool            `json:"ok"`
	Description string          `json:"description"`
	Result      json.RawMessage `json:"result"`
	Parameters  *struct {
		RetryAfter int `json:"retry_after"`
	} `json:"parameters"`
}

func (t *Telegram) call(ctx context.Context, method string, params any) (json.RawMessage, error) {
	body, err := json.Marshal(params)
	if err != nil {
		return nil, err
	}
	var lastWait time.Duration
	var lastErr error
	const attempts = 4
	for attempt := 0; attempt < attempts; attempt++ {
		if attempt > 0 {
			if err := t.wait(ctx, lastWait); err != nil {
				return nil, err
			}
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, fmt.Sprintf("%s/bot%s/%s", strings.TrimRight(t.BaseURL, "/"), t.Token, method), bytes.NewReader(body))
		if err != nil {
			return nil, err
		}
		req.Header.Set("Content-Type", "application/json")
		res, err := t.HTTP.Do(req)
		if err != nil {
			lastErr = err
			lastWait = backoff(attempt)
			continue
		}
		data, _ := io.ReadAll(io.LimitReader(res.Body, 4<<20))
		_ = res.Body.Close()
		var api apiResponse
		_ = json.Unmarshal(data, &api)
		switch {
		case res.StatusCode == http.StatusOK && api.OK:
			return api.Result, nil
		case res.StatusCode == http.StatusForbidden:
			return nil, ErrBlocked
		case res.StatusCode == http.StatusTooManyRequests:
			wait := time.Second
			if api.Parameters != nil && api.Parameters.RetryAfter > 0 {
				wait = time.Duration(api.Parameters.RetryAfter) * time.Second
			}
			lastErr = fmt.Errorf("telegram %s: rate limited", method)
			lastWait = wait
			continue
		case res.StatusCode >= 500:
			lastErr = fmt.Errorf("telegram %s: HTTP %d", method, res.StatusCode)
			lastWait = backoff(attempt)
			continue
		default:
			return nil, fmt.Errorf("telegram %s: %s (HTTP %d)", method, api.Description, res.StatusCode)
		}
	}
	return nil, lastErr
}

func backoff(attempt int) time.Duration { return time.Duration(1<<attempt) * time.Second }

// wait sleeps for d unless ctx ends first. Sleep (tests) may shortcut it.
func (t *Telegram) wait(ctx context.Context, d time.Duration) error {
	if t.Sleep != nil {
		t.Sleep(d)
		return ctx.Err()
	}
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

// Send delivers a MarkdownV2 message; callers escape user text with Escape.
func (t *Telegram) Send(ctx context.Context, chatID int64, text string) error {
	_, err := t.call(ctx, "sendMessage", map[string]any{
		"chat_id":                  chatID,
		"text":                     text,
		"parse_mode":               "MarkdownV2",
		"disable_web_page_preview": true,
	})
	return err
}

// Updates long-polls getUpdates for messages after offset.
func (t *Telegram) Updates(ctx context.Context, offset int64, timeout time.Duration) ([]Update, error) {
	raw, err := t.call(ctx, "getUpdates", map[string]any{
		"offset":          offset,
		"timeout":         int(timeout.Seconds()),
		"allowed_updates": []string{"message"},
	})
	if err != nil {
		return nil, err
	}
	var items []struct {
		UpdateID int64 `json:"update_id"`
		Message  *struct {
			Text string `json:"text"`
			Chat struct {
				ID int64 `json:"id"`
			} `json:"chat"`
			From *struct {
				Username string `json:"username"`
			} `json:"from"`
		} `json:"message"`
	}
	if err := json.Unmarshal(raw, &items); err != nil {
		return nil, fmt.Errorf("telegram getUpdates: bad result: %w", err)
	}
	var out []Update
	for _, it := range items {
		u := Update{UpdateID: it.UpdateID}
		if it.Message != nil {
			u.ChatID = it.Message.Chat.ID
			u.Text = it.Message.Text
			if it.Message.From != nil {
				u.Username = it.Message.From.Username
			}
		}
		out = append(out, u)
	}
	return out, nil
}

// markdownV2Special lists the characters MarkdownV2 requires escaping.
const markdownV2Special = "_*[]()~`>#+-=|{}.!\\"

// Escape makes arbitrary text safe inside a MarkdownV2 message.
func Escape(s string) string {
	var b strings.Builder
	for _, r := range s {
		if strings.ContainsRune(markdownV2Special, r) {
			b.WriteByte('\\')
		}
		b.WriteRune(r)
	}
	return b.String()
}

// Code renders s as inline code (backslash and backtick escaped).
func Code(s string) string {
	s = strings.ReplaceAll(s, "\\", "\\\\")
	s = strings.ReplaceAll(s, "`", "\\`")
	return "`" + s + "`"
}

// Bold renders s in bold with escaping.
func Bold(s string) string { return "*" + Escape(s) + "*" }
