package support

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	"github.com/0x3639/nomctl/internal/config"
	"github.com/0x3639/nomctl/internal/execx"
	"github.com/0x3639/nomctl/internal/metrics"
)

// WatchOptions tunes the pre-collection watch.
type WatchOptions struct {
	Poll    time.Duration
	Timeout time.Duration
	Out     string
}

type restartDetector struct {
	initialRestarts int
	initialPID      int
	seenDown        bool
}

// restarted implements the script's rule: NRestarts grew, or the PID was
// seen at 0 and is now a different non-zero value.
func (d *restartDetector) restarted(s metrics.ServiceSample) (bool, string) {
	if s.NRestarts > d.initialRestarts {
		return true, fmt.Sprintf("systemd restart count changed: %d -> %d", d.initialRestarts, s.NRestarts)
	}
	if s.MainPID == 0 {
		d.seenDown = true
		return false, ""
	}
	if d.seenDown && s.MainPID != d.initialPID {
		return true, fmt.Sprintf("process restarted: %d -> %d", d.initialPID, s.MainPID)
	}
	return false, ""
}

// Watch samples the service every Poll into 01-runtime-watch.log and
// follows its journal into 00-live-journal.log until systemd restarts it,
// ctx is cancelled, or Timeout elapses. It then takes one final sample.
func Watch(ctx context.Context, cfg config.Config, opts WatchOptions) error {
	if err := os.MkdirAll(opts.Out, 0o700); err != nil {
		return err
	}
	sampler := metrics.NewSampler(cfg)
	first := sampler.Take(ctx)
	det := &restartDetector{initialRestarts: first.Service.NRestarts, initialPID: first.Service.MainPID}

	stopJournal, err := execx.New("journalctl", "-fu", cfg.ServiceUnit(), "-o", "short-iso-precise", "--no-pager").
		StartToFile(filepath.Join(opts.Out, "00-live-journal.log"))
	if err != nil {
		slog.Warn("cannot follow journal: " + err.Error())
		stopJournal = func() {}
	}
	defer stopJournal()

	watchLog, err := os.OpenFile(filepath.Join(opts.Out, "01-runtime-watch.log"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return err
	}
	defer func() { _ = watchLog.Close() }()
	record := func(s metrics.Sample) {
		fmt.Fprintf(watchLog, "===== %s =====\n%s\n", s.Taken.UTC().Format(time.RFC3339), metrics.Format(s))
	}
	record(first)

	slog.Info(fmt.Sprintf("Watching %s (pid %d, restart count %d); sampling every %s. Press Ctrl+C to stop and collect.",
		cfg.ServiceUnit(), first.Service.MainPID, first.Service.NRestarts, opts.Poll))
	start := time.Now()
	ticker := time.NewTicker(opts.Poll)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			slog.Info("Watch interrupted; collecting the bundle now.")
			return nil
		case <-ticker.C:
		}
		s := sampler.Take(ctx)
		record(s)
		if ok, why := det.restarted(s.Service); ok {
			slog.Info("Detected " + why)
			time.Sleep(2 * time.Second)
			record(sampler.Take(ctx))
			return nil
		}
		if opts.Timeout > 0 && time.Since(start) >= opts.Timeout {
			slog.Info("Watch timeout reached without observing a restart.")
			return nil
		}
	}
}
