package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"runtime"
	"syscall"
	"time"

	proof "github.com/0x3639/nomctl/experiments/scheduleproof"
	log15 "github.com/inconshreveable/log15"
	"github.com/zenon-network/go-zenon/chain/genesis"
	"github.com/zenon-network/go-zenon/common"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
func run() error {
	endpoint := flag.String("rpc", "", "existing read-only ledger RPC URL")
	dir := flag.String("data-dir", "", "new empty directory or existing standalone replica")
	duration := flag.Duration("duration", 2*time.Minute, "maximum run duration; 0 for continuous service")
	maxBlocks := flag.Uint64("max-blocks", 5000, "maximum momentums applied this run; 0 for unlimited")
	batch := flag.Uint64("batch", 50, "momentums per request, 1–100")
	poll := flag.Duration("poll", 500*time.Millisecond, "minimum pause between sync batches")
	reorg := flag.Uint64("max-reorg", 120, "maximum private replica rewind depth")
	reportFile := flag.String("report", "", "optional JSON measurement report file")
	maxFailures := flag.Int("max-failures", 5, "consecutive failures before exiting")
	flag.Parse()
	if *endpoint == "" || *dir == "" || *duration < 0 || *poll < 100*time.Millisecond || *batch == 0 || *batch > 100 || *reorg > 10000 || *maxFailures < 1 {
		return fmt.Errorf("provide --rpc and --data-dir with valid bounded run settings")
	}
	started := time.Now()
	for _, l := range []common.Logger{common.ChainLogger, common.ConsensusLogger, common.RPCLogger, common.ZenonLogger, common.EmbeddedLogger, common.SupervisorLogger, common.VerifierLogger, common.VmLogger} {
		l.SetHandler(log15.LvlFilterHandler(log15.LvlError, log15.StderrHandler))
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if *duration > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, *duration)
		defer cancel()
	}
	source, err := proof.NewRPCSource(*endpoint)
	if err != nil {
		return err
	}
	defer source.Close()
	g, err := genesis.MakeEmbeddedGenesisConfig()
	if err != nil {
		return err
	}
	replica, err := proof.OpenReplica(*dir, g)
	if err != nil {
		return err
	}
	defer replica.Close()
	first := replica.Mirror.Frontier()
	syncer := proof.NewSyncer(replica, source)
	syncer.Batch = *batch
	syncer.MaxReorg = *reorg
	failures := 0
	reason := ""
	var runErr error
	for {
		if ctx.Err() != nil {
			reason = "time-limit-or-signal"
			break
		}
		if *maxBlocks > 0 && syncer.Status.Applied >= *maxBlocks {
			reason = "block-limit"
			break
		}
		syncer.Batch = *batch
		if *maxBlocks > 0 && *maxBlocks-syncer.Status.Applied < syncer.Batch {
			syncer.Batch = *maxBlocks - syncer.Status.Applied
		}
		err = syncer.Step(ctx)
		if ctx.Err() != nil {
			reason = "time-limit-or-signal"
			break
		}
		if err != nil {
			failures++
			fmt.Fprintln(os.Stderr, err)
			if failures >= *maxFailures {
				reason = "consecutive-errors"
				runErr = err
				break
			}
		} else {
			failures = 0
		}
		pause := *poll
		if failures > 0 {
			pause = time.Duration(failures) * time.Second
			if pause > 10*time.Second {
				pause = 10 * time.Second
			}
		}
		timer := time.NewTimer(pause)
		select {
		case <-ctx.Done():
			timer.Stop()
		case <-timer.C:
		}
	}
	last := syncer.Status
	if err = syncer.Stop(); err != nil {
		runErr = err
	}
	replica.Close()
	resources, err := proof.Measure(*dir)
	if err != nil {
		return err
	}
	report := struct {
		Engine          string          `json:"engine"`
		Platform        string          `json:"platform"`
		StartedAt       time.Time       `json:"startedAt"`
		FinishedAt      time.Time       `json:"finishedAt"`
		ElapsedSeconds  float64         `json:"elapsedSeconds"`
		Reason          string          `json:"reason"`
		InitialHeight   uint64          `json:"initialHeight"`
		Batch           uint64          `json:"batch"`
		PollSeconds     float64         `json:"pollSeconds"`
		RPCPayloadBytes uint64          `json:"rpcPayloadBytes"`
		RPCCalls        uint64          `json:"rpcCalls"`
		Resources       proof.Resources `json:"resources"`
		LastStatus      proof.Status    `json:"lastSyncStatus"`
	}{proof.EngineVersion, runtime.GOOS + "/" + runtime.GOARCH, started.UTC(), time.Now().UTC(), time.Since(started).Seconds(), reason, first.Height, *batch, poll.Seconds(), source.Bytes, source.Calls, resources, last}
	raw, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		return err
	}
	fmt.Println(string(raw))
	if *reportFile != "" {
		if err = os.WriteFile(*reportFile, append(raw, '\n'), 0600); err != nil {
			return err
		}
	}
	return runErr
}
