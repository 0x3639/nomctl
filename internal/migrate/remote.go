package migrate

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"time"

	"github.com/0x3639/nomctl/internal/health"
	"github.com/0x3639/nomctl/internal/lock"
)

type Request struct {
	Action string `json:"action"`
	Plan   Plan   `json:"plan"`
	Digest string `json:"digest,omitempty"`
}
type Response struct {
	Error        string         `json:"error,omitempty"`
	Protocol     int            `json:"protocol"`
	Host         string         `json:"host,omitempty"`
	Architecture string         `json:"architecture,omitempty"`
	State        *State         `json:"state,omitempty"`
	Evidence     *Evidence      `json:"evidence,omitempty"`
	Health       *health.Report `json:"health,omitempty"`
}

type Remote interface {
	Call(context.Context, Request) (Response, error)
	Close() error
}

// Serve holds the remote operation lock for the lifetime of the SSH control
// channel. EOF releases only the flock; durable start blocks always remain.
func (e *Endpoint) Serve(ctx context.Context, in io.Reader, out io.Writer) error {
	var held *lock.Lock
	defer func() {
		if held != nil {
			held.Release()
		}
	}()
	scanner := bufio.NewScanner(in)
	scanner.Buffer(make([]byte, 4096), 1<<20)
	enc := json.NewEncoder(out)
	for scanner.Scan() {
		var req Request
		if err := json.Unmarshal(scanner.Bytes(), &req); err != nil {
			return errors.New("invalid migration request")
		}
		if held == nil {
			var err error
			held, err = e.locked("remote migration session")
			if err != nil {
				_ = enc.Encode(Response{Protocol: Protocol, Error: err.Error()})
				return err
			}
		}
		response, err := e.handle(ctx, req)
		if err != nil {
			response.Error = err.Error()
		}
		response.Protocol = Protocol
		if err := enc.Encode(response); err != nil {
			return err
		}
	}
	return scanner.Err()
}

func (e *Endpoint) handle(ctx context.Context, req Request) (Response, error) {
	host, err := e.Runtime.HostID(ctx)
	if err != nil {
		return Response{}, err
	}
	response := Response{Protocol: Protocol, Host: host, Architecture: runtime.GOARCH}
	if req.Action == "hello" {
		return response, nil
	}
	if req.Action != "status" && req.Action != "preview" {
		if err := privateDir(e.Dir); err != nil {
			return response, err
		}
		transfer, err := lock.AcquireMigration(filepath.Join(e.Dir, "transfer.lock"), "migration receiver or verification")
		if err != nil {
			return response, err
		}
		defer transfer.Release()
	}
	p := req.Plan
	if err := p.Validate(); err != nil {
		return response, err
	}
	if p.DestinationHost != host {
		return response, errors.New("SSH destination host identity does not match the migration plan")
	}
	if req.Action == "preview" {
		if _, err := e.Status(); !errors.Is(err, os.ErrNotExist) {
			return response, errors.New("destination has a saved migration; inspect its status before a new transfer")
		}
		if err := e.Runtime.Preflight(ctx, p.Destination, false); err != nil {
			return response, err
		}
		if _, err := os.Lstat(p.Stage()); !errors.Is(err, os.ErrNotExist) {
			return response, errors.New("migration staging path already exists")
		}
		if err := checkDestinationSpace(p, p.Bytes+p.LargestFile+p.BinaryBytes+p.Entries*(64<<10)); err != nil {
			return response, err
		}
		return response, checkDestinationPorts(p.Settings.RPCURL, p.Settings.P2PPort)
	}
	if req.Action == "prepare" {
		s, err := e.Status()
		switch {
		case errors.Is(err, os.ErrNotExist):
			if err := e.Runtime.Preflight(ctx, p.Destination, false); err != nil {
				return response, err
			}
			if _, err := os.Lstat(p.Stage()); !errors.Is(err, os.ErrNotExist) {
				return response, errors.New("migration staging path already exists")
			}
			s = State{Plan: p, Role: "destination", Phase: "preparing"}
			if err := saveState(e.Dir, s); err != nil {
				return response, err
			}
		case err != nil:
			return response, err
		case s.Role != "destination" || !samePlan(s.Plan, p):
			return response, errors.New("another migration owns the destination")
		}
		if s.Phase != "preparing" && s.Phase != "copying" && s.Phase != "verified" {
			return response, fmt.Errorf("destination phase %s cannot receive files", s.Phase)
		}
		if _, err := os.Lstat(p.Destination.Data); !errors.Is(err, os.ErrNotExist) {
			return response, errors.New("destination data directory already exists")
		}
		if err := e.Runtime.Block(ctx, p.Destination, p.ID); err != nil {
			return response, err
		}
		additional, err := remainingSpace(p)
		if err != nil {
			return response, err
		}
		if err := checkDestinationSpace(p, additional); err != nil {
			return response, err
		}
		if err := privateDir(p.Stage()); err != nil {
			return response, err
		}
		if err := privateDir(filepath.Join(p.Stage(), "data")); err != nil {
			return response, err
		}
		s.Phase = "copying"
		if err := saveState(e.Dir, s); err != nil {
			return response, err
		}
		response.State = &s
		return response, nil
	}
	if req.Action == "block-return" {
		if _, err := e.Status(); errors.Is(err, os.ErrNotExist) {
			if err := e.Runtime.Preflight(ctx, p.Destination, false); err != nil {
				return response, err
			}
			if err := saveState(e.Dir, State{Plan: p, Role: "destination", Phase: "returning"}); err != nil {
				return response, err
			}
		}
	}
	s, err := e.bound(ctx, p, "destination")
	if err != nil {
		return response, err
	}
	response.State = &s
	switch req.Action {
	case "status":
		v := e.evidence(ctx, s)
		response.Evidence = &v
		return response, nil
	case "verify":
		if s.Phase != "copying" && s.Phase != "verified" {
			return response, errors.New("destination is not awaiting verification")
		}
		if err := e.Runtime.Blocked(ctx, p.Destination, p.ID); err != nil {
			return response, err
		}
		if err := e.Runtime.Stopped(ctx, p.Destination); err != nil {
			return response, err
		}
		if err := e.Runtime.Prepare(ctx, p); err != nil {
			return response, err
		}
		tree, err := Scan(ctx, filepath.Join(p.Stage(), "data"), true)
		if err != nil {
			return response, err
		}
		if req.Digest == "" || tree.Digest != req.Digest {
			return response, errors.New("destination contents or metadata differ from the stopped source")
		}
		address, err := validateProducer(filepath.Join(p.Stage(), "data"))
		if err != nil || address != p.Producer {
			return response, errors.New("destination producer configuration does not match")
		}
		s.Phase, s.Digest = "verified", req.Digest
	case "promote":
		if s.Phase == "ready" {
			return response, nil
		}
		if s.Phase != "verified" && s.Phase != "promoting" {
			return response, errors.New("only verified data can be promoted")
		}
		if err := e.Runtime.Blocked(ctx, p.Destination, p.ID); err != nil {
			return response, err
		}
		if err := e.Runtime.Stopped(ctx, p.Destination); err != nil {
			return response, err
		}
		staged := filepath.Join(p.Stage(), "data")
		if s.Phase == "promoting" {
			if _, err := os.Lstat(staged); errors.Is(err, os.ErrNotExist) {
				staged = p.Destination.Data
			}
		}
		tree, err := Scan(ctx, staged, true)
		if err != nil || tree.Digest != s.Digest {
			return response, errors.New("staged data changed before promotion")
		}
		if staged != p.Destination.Data {
			if _, err := os.Lstat(p.Destination.Data); !errors.Is(err, os.ErrNotExist) {
				return response, errors.New("refusing to overwrite an existing destination")
			}
			s.Phase = "promoting"
			if err := saveState(e.Dir, s); err != nil {
				return response, err
			}
			if err := os.Chmod(staged, 0o700); err != nil {
				return response, err
			}
			if err := os.Rename(staged, p.Destination.Data); err != nil {
				return response, err
			}
			if err := syncDir(filepath.Dir(p.Destination.Data)); err != nil {
				return response, err
			}
		}
		s.Phase = "ready"
	case "start":
		if s.Phase != "ready" && s.Phase != "starting" && s.Phase != "running" && s.Phase != "health-failed" {
			return response, errors.New("destination is not ready for confirmed handoff")
		}
		if s.Phase == "ready" {
			if err := e.Runtime.Blocked(ctx, p.Destination, p.ID); err != nil {
				return response, err
			}
			if err := e.Runtime.Stopped(ctx, p.Destination); err != nil {
				return response, err
			}
			tree, err := Scan(ctx, p.Destination.Data, true)
			if err != nil || tree.Digest != s.Digest {
				return response, errors.New("destination changed before handoff")
			}
			address, err := validateProducer(p.Destination.Data)
			if err != nil || address != p.Producer {
				return response, errors.New("destination producer validation failed")
			}
			s.Phase = "starting"
			if err := saveState(e.Dir, s); err != nil {
				return response, err
			}
		}
		if sum, err := hashFile(ctx, p.Destination.Binary); err != nil || sum != p.BinarySHA256 {
			return response, errors.New("destination znnd changed before handoff")
		}
		if err := e.Runtime.Unblock(ctx, p.Destination, p.ID); err != nil {
			return response, err
		}
		if err := e.Runtime.Start(ctx, p.Destination); err != nil {
			return response, err
		}
		r, healthErr := e.Runtime.Health(ctx, p.Destination, p.BinarySHA256)
		s.Health = &r
		response.Health = &r
		s.Phase = "running"
		if healthErr != nil {
			s.Phase = "health-failed"
		}
		if err := saveState(e.Dir, s); err != nil {
			return response, err
		}
		response.State = &s
		return response, healthErr
	case "block-return":
		// Publish intent first. Even an interrupted block must prevent a later
		// 'start' request from reopening the replacement producer.
		if s.Phase == "returned" {
			if err := e.Runtime.Blocked(ctx, p.Destination, p.ID); err != nil {
				return response, err
			}
			return response, e.Runtime.Stopped(ctx, p.Destination)
		}
		s.Phase = "returning"
		if err := saveState(e.Dir, s); err != nil {
			return response, err
		}
		if err := e.Runtime.Block(ctx, p.Destination, p.ID); err != nil {
			return response, err
		}
		if err := e.Runtime.Stopped(ctx, p.Destination); err != nil {
			return response, err
		}
		s.Phase = "returned"
	default:
		return response, errors.New("unknown remote migration action")
	}
	if err := saveState(e.Dir, s); err != nil {
		return response, err
	}
	response.State = &s
	return response, nil
}

// Receive supervises the rsync server under a second flock. Verification and
// promotion cannot race an old receiver left alive after the control SSH drops.
func (e *Endpoint) Receive(ctx context.Context, id string, args []string, in io.Reader, out, stderr io.Writer) error {
	if !idPattern.MatchString(id) || len(args) == 0 || args[0] != "--server" {
		return errors.New("invalid migration rsync request")
	}
	for _, arg := range args {
		if arg == "--sender" {
			return errors.New("migration receiver cannot send files")
		}
	}
	l, err := lock.AcquireMigration(filepath.Join(e.Dir, "transfer.lock"), "migration rsync receiver")
	if err != nil {
		return err
	}
	defer l.Release()
	s, err := e.Status()
	if err != nil {
		return err
	}
	if s.Plan.ID != id || s.Role != "destination" || s.Phase != "copying" {
		return errors.New("migration is not accepting data")
	}
	if err := e.Runtime.Blocked(ctx, s.Plan.Destination, id); err != nil {
		return err
	}
	if err := e.Runtime.Stopped(ctx, s.Plan.Destination); err != nil {
		return err
	}
	if _, err := e.bound(ctx, s.Plan, "destination"); err != nil {
		return err
	}
	if err := checkDestinationSpace(s.Plan, 0); err != nil {
		return err
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	spaceResult := make(chan error, 1)
	go func() {
		ticker := time.NewTicker(time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				spaceResult <- nil
				return
			case <-ticker.C:
				if err := checkDestinationSpace(s.Plan, 0); err != nil {
					cancel()
					spaceResult <- err
					return
				}
			}
		}
	}()
	cmd := exec.CommandContext(ctx, "rsync", args...)
	cmd.Stdin = in
	cmd.Stdout = out
	cmd.Stderr = stderr
	cmd.ExtraFiles = []*os.File{l.File()}
	runErr := cmd.Run()
	cancel()
	return errors.Join(runErr, <-spaceResult)
}

func syncDir(path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	return f.Sync()
}
