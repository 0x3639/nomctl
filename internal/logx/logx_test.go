package logx

import (
	"bytes"
	"log/slog"
	"strings"
	"testing"
)

func TestHandlerWritesConsoleAndFile(t *testing.T) {
	var console, file bytes.Buffer
	l := slog.New(New(&console, &file, slog.LevelInfo))
	l.Info("hello", "k", "v")
	l.Debug("hidden")
	l.Log(t.Context(), LevelSuccess, "done")

	c := console.String()
	if !strings.Contains(c, "hello") || !strings.Contains(c, "k") || strings.Contains(c, "hidden") {
		t.Errorf("console output unexpected: %q", c)
	}
	if !strings.Contains(c, "✓ done") {
		t.Errorf("success marker missing: %q", c)
	}
	f := file.String()
	if !strings.Contains(f, "INFO nomctl hello k=v") || !strings.Contains(f, "✓ done") {
		t.Errorf("file output unexpected: %q", f)
	}
}

func TestLevelLabels(t *testing.T) {
	for l, want := range map[slog.Level]string{slog.LevelDebug: "DEBU", slog.LevelInfo: "INFO", LevelSuccess: "INFO", slog.LevelWarn: "WARN", slog.LevelError: "ERRO"} {
		if got, _ := levelLabel(l); got != want {
			t.Errorf("%v: got %s want %s", l, got, want)
		}
	}
}

func TestSanitizeControlCharacters(t *testing.T) {
	var console bytes.Buffer
	l := slog.New(New(&console, nil, slog.LevelInfo))
	l.Error("relay said: line one\nERRO forged line", "err", "x\r\ny\u009bz\x1b[31m")
	out := console.String()
	if strings.Count(out, "\n") != 1 || !strings.Contains(out, `line one\nERRO forged line`) || !strings.Contains(out, `x\r\ny`) ||
		strings.Contains(out, "\u009b") || strings.Contains(out, "\x1b[31m") || !strings.Contains(out, `\u009bz\u001b[31m`) {
		t.Errorf("control characters must be escaped: %q", out)
	}
}
