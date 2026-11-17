package migrate

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"testing"
)

// Re-exec the test binary as the installed remote nomctl. SSH's transport is
// replaced with a local pipe; production quoting, rsync protocol, Receive,
// inherited receiver lock, and the JSON control session are exercised intact.
func TestRemoteHelper(t *testing.T) {
	dir := os.Getenv("NOMCTL_TEST_REMOTE")
	if dir == "" {
		return
	}
	var args []string
	for i, a := range os.Args {
		if a == "--" {
			args = os.Args[i+1:]
			break
		}
	}
	e := &Endpoint{Dir: dir, LockPath: filepath.Join(filepath.Dir(dir), "operation.lock")}
	s, err := e.Status()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	checkDestinationSpace = func(Plan, int64) error { return nil }
	e.Runtime = &fakeRuntime{id: s.Plan.DestinationHost, guard: s.Plan.ID}
	switch {
	case len(args) >= 2 && args[0] == "migrate" && args[1] == "_serve":
		err = e.Serve(context.Background(), os.Stdin, os.Stdout)
	case len(args) >= 4 && args[0] == "migrate" && args[1] == "_receive":
		err = e.Receive(context.Background(), args[2], args[3:], os.Stdin, os.Stdout, os.Stderr)
	default:
		err = fmt.Errorf("unexpected helper command")
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	os.Exit(0)
}

func TestProductionRsyncReceiverAndControlProtocol(t *testing.T) {
	for _, host := range []string{"replacement", "root@192.0.2.1", "2001:db8::1", "root@2001:db8::1"} {
		t.Run(host, func(t *testing.T) { exerciseProductionTransport(t, host) })
	}
}

func exerciseProductionTransport(t *testing.T, host string) {
	t.Helper()
	r := fixture(t)
	root := t.TempDir()
	bin, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	helper := filepath.Join(root, "helper with spaces")
	putFile(t, helper, "#!/bin/sh\nexec "+quote(bin)+" -test.run=^TestRemoteHelper$ -- \"$@\"\n")
	if err := os.Chmod(helper, 0755); err != nil {
		t.Fatal(err)
	}
	ssh := filepath.Join(root, "ssh")
	putFile(t, ssh, `#!/bin/sh
set -eu
login=
while [ "$#" -gt 0 ]; do
 case "$1" in
 -p) shift 2;;
 -l) login="$2@"; shift 2;;
 -o*) shift;;
 --) shift; break;;
 *) break;;
 esac
done
test "$login$1" = "$NOMCTL_TEST_HOST"
shift
exec sh -c "$*" 2>"$NOMCTL_TEST_TRACE"
`)
	if err := os.Chmod(ssh, 0755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", root+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("NOMCTL_TEST_REMOTE", r.dest.Dir)
	t.Setenv("NOMCTL_TEST_HOST", host)
	trace := filepath.Join(root, "trace")
	t.Setenv("NOMCTL_TEST_TRACE", trace)
	s, err := r.c.Local.Status()
	if err != nil {
		t.Fatal(err)
	}
	s.Plan.Connection.Nomctl = helper
	s.Plan.Connection.Host = host
	if err := saveState(r.c.Local.Dir, s); err != nil {
		t.Fatal(err)
	}
	r.c.Transfer = Rsync
	s, err = r.c.Copy(context.Background(), "pass", io.Discard)
	if err != nil {
		data, _ := os.ReadFile(trace)
		t.Fatalf("%v; remote: %s", err, data)
	}
	remote, err := Connect(context.Background(), s.Plan.Connection)
	if err != nil {
		t.Fatal(err)
	}
	response, err := remote.Call(context.Background(), Request{Action: "status", Plan: s.Plan})
	if err != nil {
		t.Fatal(err)
	}
	if response.State == nil || response.State.Phase != "ready" {
		t.Fatalf("bad protocol reply: %+v", response)
	}
	if err := remote.Close(); err != nil {
		t.Fatal(err)
	}
	// EOF must release the control lock without clearing a durable start block.
	held, err := r.dest.locked("after SSH EOF")
	if err != nil {
		t.Fatal(err)
	}
	held.Release()
	if r.srcRun.running || r.dstRun.running {
		t.Fatal("transfer started a producer")
	}
}
