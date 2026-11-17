package migrate

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

type Evidence struct {
	Stopped      bool   `json:"stopped"`
	StopProblem  string `json:"stop_problem,omitempty"`
	Blocked      bool   `json:"blocked"`
	BlockProblem string `json:"block_problem,omitempty"`
}

func (e *Endpoint) evidence(ctx context.Context, s State) Evidence {
	i := s.Plan.Source
	if s.Role == "destination" {
		i = s.Plan.Destination
	}
	v := Evidence{}
	if err := e.Runtime.Stopped(ctx, i); err != nil {
		v.StopProblem = err.Error()
	} else {
		v.Stopped = true
	}
	if err := e.Runtime.Blocked(ctx, i, s.Plan.ID); err != nil {
		v.BlockProblem = err.Error()
	} else {
		v.Blocked = true
	}
	return v
}

type StatusReport struct {
	Local          State     `json:"local"`
	LocalEvidence  Evidence  `json:"local_evidence"`
	Remote         *State    `json:"remote,omitempty"`
	RemoteEvidence *Evidence `json:"remote_evidence,omitempty"`
	RemoteProblem  string    `json:"remote_problem,omitempty"`
}

func (c *Coordinator) Inspect(ctx context.Context) (StatusReport, error) {
	s, err := c.Local.Status()
	if err != nil {
		return StatusReport{}, err
	}
	r := StatusReport{Local: s, LocalEvidence: c.Local.evidence(ctx, s)}
	if _, err := c.Local.bound(ctx, s.Plan, s.Role); err != nil {
		return r, err
	}
	if s.Role != "source" {
		return r, nil
	}
	remote, err := c.Dial(ctx, s.Plan.Connection)
	if err != nil {
		r.RemoteProblem = err.Error()
		return r, nil
	}
	defer func() { _ = remote.Close() }()
	response, err := remote.Call(ctx, Request{Action: "status", Plan: s.Plan})
	if err != nil {
		r.RemoteProblem = err.Error()
		return r, nil
	}
	r.Remote, r.RemoteEvidence = response.State, response.Evidence
	return r, nil
}

func WriteStatus(out io.Writer, r StatusReport) {
	WriteState(out, r.Local)
	fmt.Fprintf(out, "Local observed: stopped=%t, persistent start block=%t\n", r.LocalEvidence.Stopped, r.LocalEvidence.Blocked)
	if r.LocalEvidence.StopProblem != "" {
		fmt.Fprintln(out, r.LocalEvidence.StopProblem)
	}
	if r.LocalEvidence.BlockProblem != "" {
		fmt.Fprintln(out, r.LocalEvidence.BlockProblem)
	}
	if r.Remote != nil && r.RemoteEvidence != nil {
		fmt.Fprintf(out, "Remote observed: phase=%s, stopped=%t, persistent start block=%t\n", r.Remote.Phase, r.RemoteEvidence.Stopped, r.RemoteEvidence.Blocked)
		if r.RemoteEvidence.StopProblem != "" {
			fmt.Fprintln(out, r.RemoteEvidence.StopProblem)
		}
		if r.RemoteEvidence.BlockProblem != "" {
			fmt.Fprintln(out, r.RemoteEvidence.BlockProblem)
		}
	}
	if r.RemoteProblem != "" {
		fmt.Fprintln(out, "Remote state is unknown:", r.RemoteProblem)
	}
	fmt.Fprintln(out, FollowUp)
}

// CancelPlan only discards a preview; any operation that touched a node must
// instead take the guarded return path.
func (c *Coordinator) CancelPlan(ctx context.Context) error {
	l, err := c.Local.locked("discard migration preview")
	if err != nil {
		return err
	}
	defer l.Release()
	s, err := c.Local.Status()
	if err != nil {
		return err
	}
	if _, err := c.Local.bound(ctx, s.Plan, "source"); err != nil {
		return err
	}
	if s.Phase != "planned" {
		return errors.New("only an untouched migration preview can be discarded; use migrate return")
	}
	if err := os.Remove(filepath.Join(c.Local.Dir, "state.json")); err != nil {
		return err
	}
	return syncDir(c.Local.Dir)
}
