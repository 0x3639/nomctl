// Package execx wraps os/exec so that every external command nomctl runs goes
// through one place: commands are logged at debug level, their output is sent
// to the log file (or to the terminal in debug mode), and failures carry the
// tail of the command output.
package execx

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"
)

// Runner holds the process-wide execution settings.
type Runner struct {
	// Debug streams command output to Stdout/Stderr instead of Sink.
	Debug bool
	// Sink receives command output when not in debug mode (the log file).
	Sink io.Writer
	// Stdout / Stderr are the terminal streams.
	Stdout io.Writer
	Stderr io.Writer
	// Env is appended to the environment of every command.
	Env []string
}

var (
	mu       sync.RWMutex
	defaults = &Runner{Stdout: os.Stdout, Stderr: os.Stderr, Sink: io.Discard,
		Env: []string{"DEBIAN_FRONTEND=noninteractive"}}
)

// Configure sets the global runner used by the package-level helpers.
func Configure(debug bool, sink io.Writer) {
	mu.Lock()
	defer mu.Unlock()
	defaults.Debug = debug
	if sink != nil {
		defaults.Sink = sink
	} else {
		defaults.Sink = io.Discard
	}
}

func runner() *Runner {
	mu.RLock()
	defer mu.RUnlock()
	return defaults
}

// Cmd is a command under construction.
type Cmd struct {
	r     *Runner
	name  string
	args  []string
	dir   string
	env   []string
	stdin io.Reader
	ctx   context.Context
}

// New starts building a command with the global runner.
func New(name string, args ...string) *Cmd {
	return &Cmd{r: runner(), name: name, args: args}
}

// Context bounds the command: it is killed when ctx is done.
func (c *Cmd) Context(ctx context.Context) *Cmd { c.ctx = ctx; return c }

// Dir sets the working directory.
func (c *Cmd) Dir(dir string) *Cmd { c.dir = dir; return c }

// Env appends KEY=VALUE pairs to the command environment.
func (c *Cmd) Env(kv ...string) *Cmd { c.env = append(c.env, kv...); return c }

// Stdin sets the command's standard input.
func (c *Cmd) Stdin(r io.Reader) *Cmd { c.stdin = r; return c }

func (c *Cmd) String() string {
	return strings.TrimSpace(c.name + " " + strings.Join(c.args, " "))
}

func (c *Cmd) build() *exec.Cmd {
	var cmd *exec.Cmd
	if c.ctx != nil {
		cmd = exec.CommandContext(c.ctx, c.name, c.args...)
	} else {
		cmd = exec.Command(c.name, c.args...)
	}
	cmd.Dir = c.dir
	cmd.Env = append(os.Environ(), c.r.Env...)
	cmd.Env = append(cmd.Env, c.env...)
	cmd.Stdin = c.stdin
	slog.Debug("exec", "cmd", c.String(), "dir", c.dir)
	return cmd
}

// Run executes the command. Output goes to the sink (or the terminal in debug
// mode). On failure the error includes the last lines of output.
func (c *Cmd) Run() error {
	cmd := c.build()
	tail := &tailBuffer{max: 4096}
	if c.r.Debug {
		cmd.Stdout = io.MultiWriter(c.r.Stdout, tail)
		cmd.Stderr = io.MultiWriter(c.r.Stderr, tail)
	} else {
		cmd.Stdout = io.MultiWriter(c.r.Sink, tail)
		cmd.Stderr = io.MultiWriter(c.r.Sink, tail)
	}
	if err := cmd.Run(); err != nil {
		return &Error{Cmd: c.String(), Err: err, Output: tail.String()}
	}
	return nil
}

// Output runs the command and returns its trimmed standard output. Standard
// error goes to the sink (terminal in debug mode).
func (c *Cmd) Output() (string, error) {
	cmd := c.build()
	var out bytes.Buffer
	tail := &tailBuffer{max: 4096}
	cmd.Stdout = &out
	if c.r.Debug {
		cmd.Stderr = io.MultiWriter(c.r.Stderr, tail)
	} else {
		cmd.Stderr = io.MultiWriter(c.r.Sink, tail)
	}
	if err := cmd.Run(); err != nil {
		return strings.TrimSpace(out.String()), &Error{Cmd: c.String(), Err: err, Output: tail.String()}
	}
	return strings.TrimSpace(out.String()), nil
}

// Interactive runs the command attached to the terminal's stdin/stdout/stderr.
func (c *Cmd) Interactive() error {
	cmd := c.build()
	cmd.Stdin = os.Stdin
	cmd.Stdout = c.r.Stdout
	cmd.Stderr = c.r.Stderr
	if err := cmd.Run(); err != nil {
		return &Error{Cmd: c.String(), Err: err}
	}
	return nil
}

// InteractiveUntilInterrupt is Interactive for long-running foreground
// commands such as `journalctl -f`: Ctrl+C is caught here (so nomctl itself
// survives it) and forwarded to the child, whose termination ends the call.
//
// signal.Ignore must not be used for this: an ignored disposition is
// inherited across exec, so the child would ignore Ctrl+C as well.
func (c *Cmd) InteractiveUntilInterrupt() error {
	cmd := c.build()
	cmd.Stdin = os.Stdin
	cmd.Stdout = c.r.Stdout
	cmd.Stderr = c.r.Stderr

	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, os.Interrupt, syscall.SIGTERM)
	defer signal.Stop(sigs)

	if err := cmd.Start(); err != nil {
		return &Error{Cmd: c.String(), Err: err}
	}
	done := make(chan struct{})
	go func() {
		for {
			select {
			case sig := <-sigs:
				_ = cmd.Process.Signal(sig)
			case <-done:
				return
			}
		}
	}()
	err := cmd.Wait()
	close(done)
	if err != nil {
		return &Error{Cmd: c.String(), Err: err}
	}
	return nil
}

// StartToFile starts the command with stdout and stderr appended to path and
// returns a function that stops it (SIGTERM, then SIGKILL after 2 s) and
// waits for it to exit.
func (c *Cmd) StartToFile(path string) (stop func(), err error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return nil, err
	}
	cmd := c.build()
	cmd.Stdout = f
	cmd.Stderr = f
	if err := cmd.Start(); err != nil {
		_ = f.Close()
		return nil, &Error{Cmd: c.String(), Err: err}
	}
	done := make(chan struct{})
	go func() {
		_ = cmd.Wait()
		_ = f.Close()
		close(done)
	}()
	return func() {
		_ = cmd.Process.Signal(syscall.SIGTERM)
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			_ = cmd.Process.Kill()
			<-done
		}
	}, nil
}

// Quiet runs the command discarding all output; only the exit status matters.
func (c *Cmd) Quiet() error {
	cmd := c.build()
	cmd.Stdout = io.Discard
	cmd.Stderr = io.Discard
	if err := cmd.Run(); err != nil {
		return &Error{Cmd: c.String(), Err: err}
	}
	return nil
}

// Run is shorthand for New(name, args...).Run().
func Run(name string, args ...string) error { return New(name, args...).Run() }

// Output is shorthand for New(name, args...).Output().
func Output(name string, args ...string) (string, error) { return New(name, args...).Output() }

// Exists reports whether an executable is on PATH.
func Exists(name string) bool {
	_, err := exec.LookPath(name)
	return err == nil
}

// Error is returned when a command exits unsuccessfully.
type Error struct {
	Cmd    string
	Err    error
	Output string
}

func (e *Error) Error() string {
	msg := fmt.Sprintf("%s: %v", e.Cmd, e.Err)
	if tail := lastLines(e.Output, 5); tail != "" {
		msg += "\n" + tail
	}
	return msg
}

func (e *Error) Unwrap() error { return e.Err }

// ExitCode returns the process exit code carried by err, or -1.
func ExitCode(err error) int {
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		return ee.ExitCode()
	}
	return -1
}

func lastLines(s string, n int) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	lines := strings.Split(s, "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	for i, l := range lines {
		lines[i] = "  " + l
	}
	return strings.Join(lines, "\n")
}

// tailBuffer keeps only the last max bytes written to it.
type tailBuffer struct {
	mu  sync.Mutex
	buf []byte
	max int
}

func (t *tailBuffer) Write(p []byte) (int, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.buf = append(t.buf, p...)
	if len(t.buf) > t.max {
		t.buf = t.buf[len(t.buf)-t.max:]
	}
	return len(p), nil
}

func (t *tailBuffer) String() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return string(t.buf)
}
