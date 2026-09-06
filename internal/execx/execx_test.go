package execx

import (
	"bytes"
	"strings"
	"testing"
)

func TestOutputAndSink(t *testing.T) {
	var sink bytes.Buffer
	Configure(false, &sink)
	t.Cleanup(func() { Configure(false, nil) })

	out, err := Output("sh", "-c", "echo hello; echo err >&2")
	if err != nil || out != "hello" {
		t.Fatalf("Output = %q, %v", out, err)
	}
	if !strings.Contains(sink.String(), "err") {
		t.Errorf("stderr should reach sink, got %q", sink.String())
	}
}

func TestRunFailureCarriesTail(t *testing.T) {
	Configure(false, nil)
	err := Run("sh", "-c", "echo line1; echo boom >&2; exit 3")
	if err == nil {
		t.Fatal("expected error")
	}
	if ExitCode(err) != 3 {
		t.Errorf("exit code = %d", ExitCode(err))
	}
	if !strings.Contains(err.Error(), "boom") {
		t.Errorf("error should include output tail: %v", err)
	}
}

func TestDirAndEnv(t *testing.T) {
	Configure(false, nil)
	out, err := New("sh", "-c", "echo $NOMCTL_TEST_VAR; pwd").Dir("/").Env("NOMCTL_TEST_VAR=42").Output()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(out, "42\n") || !strings.HasSuffix(out, "/") {
		t.Errorf("unexpected output %q", out)
	}
}

func TestExists(t *testing.T) {
	if !Exists("sh") || Exists("definitely-not-a-binary-xyz") {
		t.Error("Exists misbehaves")
	}
}

func TestLastLines(t *testing.T) {
	got := lastLines("a\nb\nc\nd", 2)
	if got != "  c\n  d" {
		t.Errorf("lastLines = %q", got)
	}
	if lastLines("", 3) != "" {
		t.Error("empty input should give empty output")
	}
}
