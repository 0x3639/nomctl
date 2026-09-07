package execx

import (
	"bytes"
	"context"
	"os"
	"strings"
	"syscall"
	"testing"
	"time"
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

func TestInteractiveUntilInterruptForwardsSIGINT(t *testing.T) {
	Configure(false, nil)
	done := make(chan error, 1)
	go func() { done <- New("sleep", "30").InteractiveUntilInterrupt() }()
	time.Sleep(300 * time.Millisecond) // let the child start
	if err := syscall.Kill(os.Getpid(), syscall.SIGINT); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if ExitCode(err) != -1 {
			t.Errorf("child should die from the forwarded signal, got %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("child did not receive SIGINT; nomctl would hang on Ctrl+C")
	}
}

func TestStartToFile(t *testing.T) {
	Configure(false, nil)
	path := t.TempDir() + "/out.log"
	stop, err := New("sh", "-c", "echo started; sleep 30").StartToFile(path)
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(200 * time.Millisecond)
	stop()
	data, err := os.ReadFile(path)
	if err != nil || !strings.Contains(string(data), "started") {
		t.Errorf("output not captured: %q %v", data, err)
	}
	if _, err := New("definitely-missing-binary-xyz").StartToFile(path); err == nil {
		t.Error("missing binary must fail to start")
	}
}

func TestContextBoundsCommand(t *testing.T) {
	Configure(false, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, err := New("sleep", "30").Context(ctx).Output()
	if err == nil || time.Since(start) > 5*time.Second {
		t.Fatalf("command must be killed when the context ends: err=%v elapsed=%s", err, time.Since(start))
	}
}

func TestOutputLimitedKeepsPrivateBoundedCapture(t *testing.T) {
	var sink bytes.Buffer
	Configure(false, &sink)
	defer Configure(false, nil)
	out, err := New("sh", "-c", "printf 'first line\\nsecond line\\n'; printf 'stderr record\\n' >&2").OutputLimited(15)
	if err != nil {
		t.Fatal(err)
	}
	if out != "first line\n[output truncated]\n" {
		t.Fatalf("unexpected bounded output: %q", out)
	}
	if sink.Len() != 0 {
		t.Fatal("captured command output was copied to the log sink")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	started := time.Now()
	if _, err := New("sleep", "5").Context(ctx).OutputLimited(100); err == nil {
		t.Fatal("command ignored its context")
	}
	if time.Since(started) > 2*time.Second {
		t.Fatal("cancelled command took too long")
	}
}
