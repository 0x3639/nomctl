// Package logx configures log/slog with a human-friendly console handler that
// mimics `gum log` (the logger used by the original bash toolkit) and tees a
// plain copy of every line to a log file.
package logx

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/charmbracelet/lipgloss"
)

// LevelSuccess is an INFO-class level rendered with a check mark, mirroring
// success_log in the bash version.
const LevelSuccess slog.Level = slog.LevelInfo + 1

// Prefix is printed on every line, like gum's --prefix.
const Prefix = "nomctl"

var (
	styleTime    = lipgloss.NewStyle().Foreground(lipgloss.Color("239"))
	stylePrefix  = lipgloss.NewStyle().Foreground(lipgloss.Color("250"))
	styleDebug   = lipgloss.NewStyle().Foreground(lipgloss.Color("245")).Bold(true)
	styleInfo    = lipgloss.NewStyle().Foreground(lipgloss.Color("#0061EB")).Bold(true)
	styleSuccess = lipgloss.NewStyle().Foreground(lipgloss.Color("46")).Bold(true)
	styleWarn    = lipgloss.NewStyle().Foreground(lipgloss.Color("214")).Bold(true)
	styleError   = lipgloss.NewStyle().Foreground(lipgloss.Color("196")).Bold(true)
	styleKey     = lipgloss.NewStyle().Foreground(lipgloss.Color("245"))
)

// Handler is a slog.Handler writing styled lines to Console and plain lines to File.
type Handler struct {
	mu      *sync.Mutex
	console io.Writer
	file    io.Writer
	level   slog.Level
	attrs   []slog.Attr
}

// New creates a Handler. file may be nil.
func New(console, file io.Writer, level slog.Level) *Handler {
	return &Handler{mu: &sync.Mutex{}, console: console, file: file, level: level}
}

// Enabled implements slog.Handler.
func (h *Handler) Enabled(_ context.Context, l slog.Level) bool { return l >= h.level }

// WithAttrs implements slog.Handler.
func (h *Handler) WithAttrs(attrs []slog.Attr) slog.Handler {
	c := *h
	c.attrs = append(append([]slog.Attr{}, h.attrs...), attrs...)
	return &c
}

// WithGroup implements slog.Handler (groups are flattened).
func (h *Handler) WithGroup(string) slog.Handler { return h }

// Handle implements slog.Handler.
func (h *Handler) Handle(_ context.Context, r slog.Record) error {
	var kv strings.Builder
	add := func(a slog.Attr) {
		if a.Key == "" {
			return
		}
		fmt.Fprintf(&kv, " %s=%s", styleKey.Render(a.Key), sanitize(fmt.Sprint(a.Value)))
	}
	for _, a := range h.attrs {
		add(a)
	}
	r.Attrs(func(a slog.Attr) bool { add(a); return true })

	label, style := levelLabel(r.Level)
	msg := sanitize(r.Message)
	if r.Level == LevelSuccess {
		msg = "✓ " + msg
	}
	ts := r.Time
	if ts.IsZero() {
		ts = time.Now()
	}

	h.mu.Lock()
	defer h.mu.Unlock()
	if h.console != nil {
		fmt.Fprintf(h.console, "%s %s %s %s%s\n",
			styleTime.Render(ts.Format(time.Kitchen)),
			style.Render(label),
			stylePrefix.Render(Prefix),
			msg, kv.String())
	}
	if h.file != nil {
		plain := strings.Builder{}
		for _, a := range h.attrs {
			fmt.Fprintf(&plain, " %s=%s", a.Key, sanitize(fmt.Sprint(a.Value)))
		}
		r.Attrs(func(a slog.Attr) bool {
			fmt.Fprintf(&plain, " %s=%s", a.Key, sanitize(fmt.Sprint(a.Value)))
			return true
		})
		fmt.Fprintf(h.file, "%s %s %s %s%s\n", ts.Format(time.RFC3339), label, Prefix, msg, plain.String())
	}
	return nil
}

// sanitize escapes control characters so untrusted text (relay errors,
// command output) cannot forge extra log lines or move the cursor.
func sanitize(s string) string {
	bad := func(r rune) bool { return r != '\t' && unicode.IsControl(r) }
	if !strings.ContainsFunc(s, bad) {
		return s
	}
	var b strings.Builder
	for _, r := range s {
		switch {
		case r == '\n':
			b.WriteString(`\n`)
		case r == '\r':
			b.WriteString(`\r`)
		case bad(r):
			fmt.Fprintf(&b, `\u%04x`, r)
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}

func levelLabel(l slog.Level) (string, lipgloss.Style) {
	switch {
	case l == LevelSuccess:
		return "INFO", styleSuccess
	case l >= slog.LevelError:
		return "ERRO", styleError
	case l >= slog.LevelWarn:
		return "WARN", styleWarn
	case l >= slog.LevelInfo:
		return "INFO", styleInfo
	default:
		return "DEBU", styleDebug
	}
}

// Setup installs the console handler as slog's default logger and opens the
// log file (creating parent directories). It returns the open file (may be
// nil when the file cannot be opened; a warning is logged in that case) so
// callers can route external command output to it.
func Setup(debug bool, logFile string) (*os.File, func()) {
	level := slog.LevelInfo
	if debug {
		level = slog.LevelDebug
	}
	var f *os.File
	var openErr error
	if logFile != "" {
		if err := os.MkdirAll(filepath.Dir(logFile), 0o755); err == nil {
			f, openErr = os.OpenFile(logFile, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
		} else {
			openErr = err
		}
	}
	var file io.Writer
	if f != nil {
		file = f
	}
	h := New(os.Stderr, file, level)
	currentMu.Lock()
	current = h
	currentMu.Unlock()
	slog.SetDefault(slog.New(h))
	if openErr != nil {
		slog.Warn("log file unavailable; logging to console only", "path", logFile, "err", openErr)
	}
	return f, func() {
		if f != nil {
			_ = f.Close()
		}
	}
}

// Success logs at LevelSuccess using the default logger.
func Success(msg string, args ...any) {
	slog.Default().Log(context.Background(), LevelSuccess, msg, args...)
}

var (
	currentMu sync.Mutex
	current   *Handler
)

// Quiet runs fn with console logging suppressed (log lines still reach the
// file). It mirrors `gum spin` swallowing the output of the spun command.
func Quiet(fn func()) {
	currentMu.Lock()
	h := current
	currentMu.Unlock()
	if h == nil {
		fn()
		return
	}
	prev := slog.Default()
	quiet := *h
	quiet.console = nil
	slog.SetDefault(slog.New(&quiet))
	defer slog.SetDefault(prev)
	fn()
}
