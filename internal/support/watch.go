package support

import (
	"context"
	"errors"
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
	if err := os.Mkdir(opts.Out, 0o700); err != nil {
		return err
	}
	return watchInto(ctx, cfg, opts)
}

func watchInto(ctx context.Context, cfg config.Config, opts WatchOptions) error {
	if opts.Poll < time.Second {
		return errors.New("watch poll interval must be at least one second")
	}
	sampler := metrics.NewSampler(cfg)
	first := sampler.Take(ctx)
	det := &restartDetector{initialRestarts: first.Service.NRestarts, initialPID: first.Service.MainPID}

	journalCtx, cancelJournal := context.WithCancel(ctx)
	journalDone := make(chan struct{})
	go func() {
		defer close(journalDone)
		out, err := execx.New("journalctl", "-fu", cfg.ServiceUnit(), "-o", "short-iso-precise", "--no-pager", "--lines=1000").
			Context(journalCtx).OutputLimited(LogTailBytes)
		if err != nil && journalCtx.Err() == nil {
			out += "\n[error] " + err.Error()
		}
		// Raw journal content stays in bounded memory until it can be redacted
		// as a whole, including structured records spanning multiple lines.
		if err := writePrivateFile(filepath.Join(opts.Out, "00-live-journal.log"), []byte(Redact(out))); err != nil {
			slog.Warn("cannot save live journal: " + err.Error())
		}
	}()
	defer func() { cancelJournal(); <-journalDone }()

	watchLog, err := os.OpenFile(filepath.Join(opts.Out, "01-runtime-watch.log"), os.O_CREATE|os.O_WRONLY|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	defer func() { _ = watchLog.Close() }()
	written := 0
	record := func(s metrics.Sample) {
		line := Redact(fmt.Sprintf("===== %s =====\n%s\n", s.Taken.UTC().Format(time.RFC3339), metrics.Format(s)))
		if written+len(line) <= LogTailBytes {
			n, _ := watchLog.WriteString(line)
			written += n
		} else if written <= LogTailBytes {
			_, _ = watchLog.WriteString("[watch output truncated]\n")
			written = LogTailBytes + 1
		}
	}
	record(first)

	slog.Info(fmt.Sprintf("Watching %s (pid %d, restart count %d); sampling every %s. Press Ctrl+C to stop and collect.",
		cfg.ServiceUnit(), first.Service.MainPID, first.Service.NRestarts, opts.Poll))
	ticker := time.NewTicker(opts.Poll)
	defer ticker.Stop()
	var deadline <-chan time.Time
	if opts.Timeout > 0 {
		timer := time.NewTimer(opts.Timeout)
		defer timer.Stop()
		deadline = timer.C
	}
	for {
		select {
		case <-ctx.Done():
			slog.Info("Watch interrupted; collecting the bundle now.")
			return nil
		case <-deadline:
			slog.Info("Watch timeout reached without observing a restart.")
			record(sampler.Take(ctx))
			return nil
		case <-ticker.C:
		}
		s := sampler.Take(ctx)
		record(s)
		if ok, why := det.restarted(s.Service); ok {
			slog.Info("Detected " + why)
			select {
			case <-ctx.Done():
			case <-time.After(2 * time.Second):
			}
			record(sampler.Take(ctx))
			return nil
		}
	}
}
