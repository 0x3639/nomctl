package migrate

import (
	"context"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/0x3639/nomctl/internal/lock"
)

func TestFreePortsAcceptsPortableRPCBinds(t *testing.T) {
	for _, host := range []string{"127.0.0.1", "0.0.0.0", "::1", "::"} {
		t.Run(host, func(t *testing.T) {
			addr := net.JoinHostPort(host, "0")
			listener, err := net.Listen("tcp", addr)
			if err != nil {
				t.Skipf("bind unavailable on this test host: %v", err)
			}
			_ = listener.Close()
			if err := freePorts("http://"+addr, 0); err != nil {
				t.Fatal(err)
			}
		})
	}
	for _, host := range []string{"192.0.2.1", "2001:db8::1", "invalid"} {
		if err := freePorts("http://"+net.JoinHostPort(host, "0"), 0); err == nil || !strings.Contains(err.Error(), "loopback or wildcard") {
			t.Fatalf("nonportable bind %s: %v", host, err)
		}
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = listener.Close() }()
	if err := freePorts("http://"+listener.Addr().String(), 0); err == nil {
		t.Fatal("accepted occupied RPC port")
	}
}

func TestSystemRuntimeDurableBlocks(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	r := systemRuntime{stateDir: filepath.Join(root, "control"), unitDir: filepath.Join(root, "units")}
	i := Installation{Data: filepath.Join(root, ".znn"), Binary: filepath.Join(root, "znnd"), Service: "test-node"}
	id := strings.Repeat("a", 32)
	state := filepath.Join(root, "service-state")
	events := filepath.Join(root, "events")
	putFile(t, state, "active")
	t.Setenv("MIGRATION_SERVICE_STATE", state)
	t.Setenv("MIGRATION_EVENTS", events)
	t.Setenv("MIGRATION_GUARD", r.guardFile())
	commands := filepath.Join(root, "commands")
	ctl := filepath.Join(commands, "systemctl")
	putFile(t, ctl, `#!/bin/sh
set -eu
echo "$*" >> "$MIGRATION_EVENTS"
case "$1" in
status) exit 0;;
show) printf 'LoadState=loaded\nActiveState=%s\nMainPID=0\n' "$(cat "$MIGRATION_SERVICE_STATE")";;
daemon-reload) test -f "$MIGRATION_GUARD";;
disable) test "$2" = --now; test -f "$MIGRATION_GUARD"; echo inactive > "$MIGRATION_SERVICE_STATE";;
enable) test ! -f "$MIGRATION_GUARD";;
start) test ! -f "$MIGRATION_GUARD"; echo active > "$MIGRATION_SERVICE_STATE";;
*) exit 1;;
esac
`)
	if err := os.Chmod(ctl, 0755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", commands+string(os.PathListSeparator)+os.Getenv("PATH"))
	if err := r.Block(ctx, i, id); err != nil {
		t.Fatal(err)
	}
	if err := r.Blocked(ctx, i, id); err != nil {
		t.Fatal(err)
	}
	if err := r.Stopped(ctx, i); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(r.guardFile())
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatal("guard permissions", err)
	}
	dropin, err := os.ReadFile(r.guardDropin(i))
	if err != nil || !strings.Contains(string(dropin), "ConditionPathExists=!"+r.guardFile()) {
		t.Fatal("missing persistent systemd condition", err)
	}
	// A new process/runtime reconstructs the same persistent block from files.
	reopened := systemRuntime{stateDir: r.stateDir, unitDir: r.unitDir}
	if err := reopened.Blocked(ctx, i, id); err != nil {
		t.Fatal(err)
	}
	if err := reopened.Unblock(ctx, i, strings.Repeat("b", 32)); err == nil {
		t.Fatal("wrong migration removed guard")
	}
	if err := reopened.Unblock(ctx, i, id); err != nil {
		t.Fatal(err)
	}
	if err := reopened.Unblock(ctx, i, id); err != nil {
		t.Fatal("interrupted unblock not resumable", err)
	}
	if err := reopened.Start(ctx, i); err != nil {
		t.Fatal(err)
	}
	if err := reopened.Block(ctx, i, id); err != nil {
		t.Fatal("return failed to reinstall block", err)
	}
	if err := reopened.Blocked(ctx, i, id); err != nil {
		t.Fatal(err)
	}
	calls, _ := os.ReadFile(events)
	if strings.Count(string(calls), "disable --now test-node") != 2 {
		t.Fatalf("services not disabled before acknowledgement: %s", calls)
	}
	if (systemRuntime{}).guardFile() != lock.MigrationGuard {
		t.Fatal("systemd and operation-lock guards disagree")
	}
}

func TestResumeSpaceCreditsCompletedFiles(t *testing.T) {
	r := fixture(t)
	s, err := r.c.Local.Status()
	if err != nil {
		t.Fatal(err)
	}
	p := s.Plan
	empty, err := remainingSpace(p)
	if err != nil {
		t.Fatal(err)
	}
	putFile(t, filepath.Join(p.Stage(), "data", "completed"), strings.Repeat("x", int(p.Bytes)))
	remaining, err := remainingSpace(p)
	if err != nil {
		t.Fatal(err)
	}
	if remaining != empty-p.Bytes || remaining < p.LargestFile+p.BinaryBytes {
		t.Fatalf("resume reserve incorrect: %d -> %d", empty, remaining)
	}
}
