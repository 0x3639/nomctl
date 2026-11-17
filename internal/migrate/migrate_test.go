package migrate

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/0x3639/nomctl/internal/config"
	"github.com/0x3639/nomctl/internal/health"
	"github.com/0x3639/nomctl/internal/lock"
)

type fakeRuntime struct {
	id, guard                     string
	running, failStart, badHealth bool
	starts                        int
}

func (r *fakeRuntime) HostID(context.Context) (string, error) { return r.id, nil }
func (r *fakeRuntime) Preflight(_ context.Context, i Installation, source bool) error {
	if source {
		return nil
	}
	for _, p := range []string{i.Data, i.Binary} {
		if _, err := os.Lstat(p); !errors.Is(err, os.ErrNotExist) {
			return errors.New("existing destination")
		}
	}
	return nil
}
func (r *fakeRuntime) Block(_ context.Context, _ Installation, id string) error {
	r.guard = id
	r.running = false
	return nil
}
func (r *fakeRuntime) Blocked(_ context.Context, _ Installation, id string) error {
	if r.guard != id {
		return errors.New("missing guard")
	}
	return nil
}
func (r *fakeRuntime) Stop(context.Context, Installation) error { r.running = false; return nil }
func (r *fakeRuntime) Stopped(context.Context, Installation) error {
	if r.running {
		return errors.New("still running")
	}
	return nil
}
func (r *fakeRuntime) Prepare(ctx context.Context, p Plan) error {
	sum, err := hashFile(ctx, filepath.Join(p.Stage(), "node-binary"))
	if err != nil || sum != p.BinarySHA256 {
		return errors.New("wrong binary")
	}
	b, err := os.ReadFile(filepath.Join(p.Stage(), "node-binary"))
	if err != nil {
		return err
	}
	return os.WriteFile(p.Destination.Binary, b, 0755)
}
func (r *fakeRuntime) Unblock(_ context.Context, _ Installation, id string) error {
	if r.guard != "" && r.guard != id {
		return errors.New("wrong guard")
	}
	r.guard = ""
	return nil
}
func (r *fakeRuntime) Start(context.Context, Installation) error {
	if r.guard != "" {
		return errors.New("boot/start prevented by persistent guard")
	}
	if r.failStart {
		return errors.New("start failed")
	}
	r.running = true
	r.starts++
	return nil
}
func (r *fakeRuntime) Health(context.Context, Installation, string) (health.Report, error) {
	if r.badHealth {
		return health.Report{Outcome: "stalled"}, errors.New("no progress")
	}
	return health.Report{Outcome: "syncing"}, nil
}

type inprocessRemote struct {
	e    *Endpoint
	held *lock.Lock
	lose *string
}

func (r *inprocessRemote) Call(ctx context.Context, q Request) (Response, error) {
	v, err := r.e.handle(ctx, q)
	if r.lose != nil && *r.lose == q.Action {
		*r.lose = ""
		return Response{}, errors.New("lost SSH reply")
	}
	return v, err
}
func (r *inprocessRemote) Close() error { r.held.Release(); return nil }

type rehearsal struct {
	c              *Coordinator
	dest           *Endpoint
	srcRun, dstRun *fakeRuntime
	cfg            config.Config
	lose           string
}

func putFile(t *testing.T, p, data string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(p), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(data), 0600); err != nil {
		t.Fatal(err)
	}
}
func requireRsync(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("rsync"); err != nil {
		if os.Getenv("NOMCTL_REQUIRE_RSYNC") == "1" {
			t.Fatal(err)
		}
		t.Skip("rsync integration requires test/migration/Dockerfile")
	}
}

func fixture(t *testing.T) *rehearsal {
	t.Helper()
	requireRsync(t)
	oldSpace, oldPorts := checkDestinationSpace, checkDestinationPorts
	t.Cleanup(func() { checkDestinationSpace, checkDestinationPorts = oldSpace, oldPorts })
	checkDestinationSpace = func(Plan, int64) error { return nil }
	checkDestinationPorts = func(string, int) error { return nil }
	root := t.TempDir()
	cfg := config.Default()
	cfg.ZnnDir = filepath.Join(root, "original", ".znn")
	cfg.InstallDir = filepath.Join(root, "original")
	cfg.BackupDir = filepath.Join(root, "recovery")
	cfg.MinFreeSpaceKB = 0
	putFile(t, cfg.BinaryPath(), "known binary")
	for path, data := range map[string]string{"config.json": "{}", "nom/state": "chain", "wallet/secret": "wallet-secret", ".hidden": "hidden", "unlisted/custom": "other"} {
		putFile(t, filepath.Join(cfg.ZnnDir, path), data)
	}
	if err := os.Link(filepath.Join(cfg.ZnnDir, ".hidden"), filepath.Join(cfg.ZnnDir, "hardlink")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("wallet/secret", filepath.Join(cfg.ZnnDir, "internal-link")); err != nil {
		t.Fatal(err)
	}
	destRoot := filepath.Join(root, "replacement")
	if err := os.MkdirAll(destRoot, 0700); err != nil {
		t.Fatal(err)
	}
	r := &rehearsal{cfg: cfg, srcRun: &fakeRuntime{id: "original-host", running: true}, dstRun: &fakeRuntime{id: "replacement-host"}}
	r.dest = &Endpoint{Dir: filepath.Join(destRoot, "control"), LockPath: filepath.Join(destRoot, "operation.lock"), Runtime: r.dstRun}
	r.c = &Coordinator{Local: &Endpoint{Dir: filepath.Join(root, "source-control"), LockPath: filepath.Join(root, "source.lock"), Runtime: r.srcRun}, Recovery: func(context.Context, config.Config, string) (string, string, string, error) {
		return "protected-pair", "encrypted-wallet", "wallet-hash", nil
	}, Transfer: localRsync}
	r.c.Dial = func(context.Context, Connection) (Remote, error) {
		held, err := r.dest.locked("remote test session")
		if err != nil {
			return nil, err
		}
		return &inprocessRemote{r.dest, held, &r.lose}, nil
	}
	_, err := r.c.Plan(context.Background(), cfg, Connection{Host: "root@replacement", Port: 22, Nomctl: "/usr/local/bin/nomctl"}, Installation{Data: filepath.Join(destRoot, ".znn"), Binary: filepath.Join(destRoot, "znnd"), Service: "go-zenon"})
	if err != nil {
		t.Fatal(err)
	}
	return r
}

// Uses a real rsync process and the production preservation/verification flags,
// with isolated source/destination trees instead of operator servers.
func localRsync(ctx context.Context, p Plan, out io.Writer) error {
	for _, pair := range [][2]string{{p.Source.Binary, filepath.Join(p.Stage(), "node-binary")}, {p.Source.Data + "/", filepath.Join(p.Stage(), "data") + "/"}} {
		args := append(rsyncFileArgs(p), "--delete-delay", "--", pair[0], pair[1])
		cmd := exec.CommandContext(ctx, "rsync", args...)
		cmd.Stdout = out
		var stderr bytes.Buffer
		cmd.Stderr = &stderr
		if err := cmd.Run(); err != nil {
			return errors.New(err.Error() + ": " + stderr.String())
		}
	}
	return nil
}

func TestFullDirectoryHandoffAndReturn(t *testing.T) {
	r := fixture(t)
	ctx := context.Background()
	if !r.srcRun.running || r.dstRun.running {
		t.Fatal("preview changed services")
	}
	s, err := r.c.Copy(ctx, "encryption passphrase", io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	if s.Phase != "ready" || r.srcRun.running || r.dstRun.running || r.srcRun.guard == "" || r.dstRun.guard == "" {
		t.Fatalf("unsafe copied state: %+v", s)
	}
	a, err := Scan(ctx, s.Plan.Source.Data, true)
	if err != nil {
		t.Fatal(err)
	}
	b, err := Scan(ctx, s.Plan.Destination.Data, true)
	if err != nil || a.Digest != b.Digest {
		t.Fatal("tree differs", err)
	}
	if data, _ := os.ReadFile(filepath.Join(s.Plan.Destination.Data, "wallet/secret")); string(data) != "wallet-secret" {
		t.Fatal("wallet omitted")
	}
	if info, _ := os.Stat(s.Plan.Destination.Data); info.Mode().Perm() != 0700 {
		t.Fatal("data root not private")
	}
	s, err = r.c.Handoff(ctx)
	if err != nil || s.Phase != "complete" || r.srcRun.running || !r.dstRun.running {
		t.Fatalf("handoff: %+v %v", s, err)
	}
	if err := r.srcRun.Start(ctx, s.Plan.Source); err == nil {
		t.Fatal("original could reboot/start after handoff")
	}
	s, err = r.c.Return(ctx)
	if err != nil || s.Phase != "returned" || !r.srcRun.running || r.dstRun.running {
		t.Fatalf("return: %+v %v", s, err)
	}
	if err := r.dstRun.Start(ctx, s.Plan.Destination); err == nil {
		t.Fatal("replacement could reboot/start after return")
	}
	if _, err := r.c.Handoff(ctx); err == nil {
		t.Fatal("returned plan reopened replacement")
	}
	if _, err := Scan(ctx, s.Plan.Source.Data, true); err != nil {
		t.Fatal("original was removed", err)
	}
}

func TestInterruptedTransferResumesAndRepairsVerification(t *testing.T) {
	r := fixture(t)
	ctx := context.Background()
	first := true
	r.c.Transfer = func(ctx context.Context, p Plan, out io.Writer) error {
		if err := localRsync(ctx, p, out); err != nil {
			return err
		}
		if first {
			first = false
			return errors.New("disconnected")
		}
		return nil
	}
	s, err := r.c.Copy(ctx, "pass", io.Discard)
	if err == nil || s.Phase != "copying" || r.srcRun.running || r.dstRun.running {
		t.Fatalf("interruption unsafe: %+v %v", s, err)
	}
	putFile(t, filepath.Join(s.Plan.Stage(), "data", "stray"), "partial extra")
	r.c.Transfer = func(ctx context.Context, p Plan, out io.Writer) error {
		if err := localRsync(ctx, p, out); err != nil {
			return err
		}
		putFile(t, filepath.Join(p.Stage(), "data", "nom/state"), "corrupt")
		return nil
	}
	s, err = r.c.Copy(ctx, "", io.Discard)
	if err == nil || s.Phase != "copying" {
		t.Fatal("bad copy accepted", err)
	}
	r.c.Transfer = localRsync
	s, err = r.c.Copy(ctx, "", io.Discard)
	if err != nil || s.Phase != "ready" {
		t.Fatal("resume failed", err)
	}
	if _, err := os.Stat(filepath.Join(s.Plan.Destination.Data, "stray")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("extra staging file retained")
	}
}

func TestFailureKeepsSourceBlocked(t *testing.T) {
	for _, failure := range []string{"start", "health", "lost-start", "lost-promote", "lost-return"} {
		t.Run(failure, func(t *testing.T) {
			r := fixture(t)
			ctx := context.Background()
			if failure == "lost-promote" {
				r.lose = "promote"
			}
			_, err := r.c.Copy(ctx, "pass", io.Discard)
			if failure == "lost-promote" {
				if err == nil {
					t.Fatal("expected lost reply")
				}
				_, err = r.c.Copy(ctx, "", io.Discard)
			}
			if err != nil {
				t.Fatal(err)
			}
			r.dstRun.failStart = failure == "start"
			r.dstRun.badHealth = failure == "health"
			if failure == "lost-start" {
				r.lose = "start"
			}
			_, err = r.c.Handoff(ctx)
			if r.srcRun.running || r.srcRun.guard == "" {
				t.Fatal("source restarted after destination uncertainty")
			}
			if failure == "start" || failure == "health" || failure == "lost-start" {
				if err == nil {
					t.Fatal("missing failure")
				}
			}
			if failure == "lost-start" {
				if _, err := r.c.Handoff(ctx); err != nil {
					t.Fatal("could not reconcile lost handoff reply", err)
				}
			}
			if failure == "lost-return" {
				r.lose = "block-return"
				if _, err := r.c.Return(ctx); err == nil {
					t.Fatal("expected return disconnect")
				}
				if r.srcRun.running {
					t.Fatal("source restarted without acknowledgement")
				}
			}
			if _, err := r.c.Return(ctx); err != nil {
				t.Fatal("safe return failed", err)
			}
			if r.dstRun.running || r.dstRun.guard == "" || !r.srcRun.running {
				t.Fatal("unsafe return")
			}
		})
	}
}

func TestRefusalsAndEarlyReturn(t *testing.T) {
	t.Run("space before downtime", func(t *testing.T) {
		r := fixture(t)
		checkDestinationSpace = func(Plan, int64) error { return errors.New("disk reserve") }
		if _, err := r.c.Copy(context.Background(), "pass", io.Discard); err == nil {
			t.Fatal("accepted insufficient space")
		}
		if !r.srcRun.running {
			t.Fatal("stopped before failed preflight")
		}
	})
	t.Run("source changed on resume", func(t *testing.T) {
		r := fixture(t)
		r.c.Transfer = func(context.Context, Plan, io.Writer) error { return errors.New("interrupted") }
		s, _ := r.c.Copy(context.Background(), "pass", io.Discard)
		putFile(t, filepath.Join(s.Plan.Source.Data, "nom/state"), "different")
		if _, err := r.c.Copy(context.Background(), "", io.Discard); err == nil {
			t.Fatal("accepted changed source")
		}
	})
	t.Run("destination collision", func(t *testing.T) {
		r := fixture(t)
		s, _ := r.c.Local.Status()
		putFile(t, filepath.Join(s.Plan.Destination.Data, "existing"), "keep")
		if _, err := r.c.Copy(context.Background(), "pass", io.Discard); err == nil {
			t.Fatal("overwrote destination")
		}
		if !r.srcRun.running {
			t.Fatal("stopped source on conflict")
		}
	})
	t.Run("failure before remote prepare", func(t *testing.T) {
		r := fixture(t)
		s, _ := r.c.Local.Status()
		s.Phase = "protected"
		if err := saveState(r.c.Local.Dir, s); err != nil {
			t.Fatal(err)
		}
		if _, err := r.c.Return(context.Background()); err != nil {
			t.Fatal(err)
		}
		if !r.srcRun.running || r.dstRun.running || r.dstRun.guard == "" {
			t.Fatal("early return unsafe")
		}
	})
	t.Run("receiver locks promotion", func(t *testing.T) {
		r := fixture(t)
		s, err := r.c.Copy(context.Background(), "pass", io.Discard)
		if err != nil {
			t.Fatal(err)
		}
		l, err := lock.AcquireMigration(filepath.Join(r.dest.Dir, "transfer.lock"), "old receiver")
		if err != nil {
			t.Fatal(err)
		}
		defer l.Release()
		if _, err := r.dest.handle(context.Background(), Request{Action: "start", Plan: s.Plan}); !errors.Is(err, lock.ErrBusy) {
			t.Fatalf("start raced receiver: %v", err)
		}
	})
}

func TestRejectLinksAndSpecialFiles(t *testing.T) {
	for _, target := range []string{"/outside/absent", "../absent", "../outside"} {
		t.Run(target, func(t *testing.T) {
			root := t.TempDir()
			data := filepath.Join(root, "data")
			putFile(t, filepath.Join(data, "file"), "secret")
			putFile(t, filepath.Join(root, "outside"), "different target")
			if err := os.Symlink(target, filepath.Join(data, "bad")); err != nil {
				t.Fatal(err)
			}
			if _, err := Scan(context.Background(), data, true); err == nil {
				t.Fatal("external link accepted")
			}
		})
	}
}

func TestPlanBindingAndSSHOptions(t *testing.T) {
	p := Plan{Protocol: Protocol, ID: strings.Repeat("a", 32), SourceHost: "a", DestinationHost: "b", Architecture: runtime.GOARCH, Source: Installation{"/root/.znn", "/usr/local/bin/znnd", "go-zenon"}, Destination: Installation{"/root/.znn", "/usr/local/bin/znnd", "go-zenon"}, Connection: Connection{"root@host", 22, "/usr/local/bin/nomctl", true}, BinarySHA256: strings.Repeat("b", 64), BackupDirectory: "/backup"}
	if err := p.Validate(); err != nil {
		t.Fatal(err)
	}
	p.DestinationHost = "a"
	if err := p.Validate(); err == nil {
		t.Fatal("same host accepted")
	}
	for _, host := range []string{"-oProxyCommand=x", "root@host;touch /x", "host\nother"} {
		c := p.Connection
		c.Host = host
		if err := c.Validate(); err == nil {
			t.Fatal("unsafe host", host)
		}
	}
	args := strings.Join(p.Connection.sshArgs(), " ")
	for _, want := range []string{"StrictHostKeyChecking=yes", "BatchMode=yes", "ForwardAgent=no"} {
		if !strings.Contains(args, want) {
			t.Fatal("missing SSH constraint", want)
		}
	}
}
