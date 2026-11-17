// Package doctor collects read-only evidence and explains node health problems.
package doctor

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"time"
	"unicode"

	"github.com/0x3639/nomctl/internal/backup"
	"github.com/0x3639/nomctl/internal/config"
	"github.com/0x3639/nomctl/internal/execx"
	"github.com/0x3639/nomctl/internal/fsx"
	"github.com/0x3639/nomctl/internal/metrics"
	"github.com/0x3639/nomctl/internal/node"
	"github.com/0x3639/nomctl/internal/support"
)

const DefaultWait = 10 * time.Second

type Options struct {
	Wait     time.Duration
	ProbeURL string
	PublicIP string
}

func (o Options) Validate() error {
	if o.Wait < time.Second || o.Wait > 2*time.Minute {
		return fmt.Errorf("--wait must be between 1s and 2m")
	}
	return validateProbe(o.ProbeURL, o.PublicIP)
}

type Check struct {
	ID       string `json:"id"`
	Severity string `json:"severity"`
	Summary  string `json:"summary"`
	Evidence string `json:"evidence"`
	NextStep string `json:"next_step"`
}

type Report struct {
	SchemaVersion int       `json:"schema_version"`
	StartedAt     time.Time `json:"started_at"`
	FinishedAt    time.Time `json:"finished_at"`
	Service       string    `json:"service"`
	Identity      Identity  `json:"identity"`
	Checks        []Check   `json:"checks"`
}

// Run never starts/stops services or takes the mutation lock. A completed
// report is successful even when some checks could not collect evidence.
func Run(ctx context.Context, cfg config.Config, opts Options) (Report, error) {
	if err := opts.Validate(); err != nil {
		return Report{}, err
	}
	if err := ctx.Err(); err != nil {
		return Report{}, err
	}
	ctx, cancel := context.WithTimeout(ctx, opts.Wait+time.Minute)
	defer cancel()
	r := Report{SchemaVersion: 1, StartedAt: time.Now().UTC(), Service: clean(cfg.ServiceUnit())}
	settings, settingsErr := readSettings(cfg.ZnnDir)
	sampler := metrics.NewSampler(cfg)
	// Do not query an assumed endpoint when the configuration is invalid or
	// HTTP RPC is disabled. An empty URL fails locally without network access.
	sampler.Node = node.NewWithTimeout(settings.rpcURL, cfg.RPCTimeout)
	sampler.SetPillarName("")
	take := func() observation {
		sampleCtx, stop := context.WithTimeout(ctx, 10*time.Second)
		defer stop()
		return observe(sampleCtx, sampler.Take, metrics.ReadServicePropsContext)
	}
	before := take()
	timer := time.NewTimer(opts.Wait)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return Report{}, ctx.Err()
	case <-timer.C:
	}
	after := take()
	r.Checks = observationChecks(before, after, settings, settingsErr)
	r.Identity = collectIdentity(ctx, cfg.BinaryPath(), "/proc", after.Service, after.Node)
	// ProcessInfo can succeed even when another stats method fails. Retain
	// version/commit evidence without declaring the entire RPC healthy.
	if settings.rpcURL != "" && after.continuity == "" && r.Identity.Version == "" {
		infoCtx, stop := context.WithTimeout(ctx, 5*time.Second)
		p, err := sampler.Node.ProcessInfo(infoCtx)
		stop()
		if err == nil {
			r.Identity.Version, r.Identity.Commit = clean(p.Version), clean(p.Commit)
		}
	}
	r.Checks = append(r.Checks, identityCheck(r.Identity))
	r.Checks = append(r.Checks, diskCheck("data_disk", cfg.ZnnDir, cfg.MinFreeSpaceKB, fsx.DiskFree))
	if schedule, err := backup.ReadSchedule(); err != nil {
		r.Checks = append(r.Checks, check("backup_disk", "unknown", "Backup destination could not be determined", "Saved backup schedule could not be read.", "Run nomctl backup status and review the saved schedule."))
	} else {
		dir := cfg.BackupDir
		if schedule != nil {
			dir = schedule.Directory
		}
		r.Checks = append(r.Checks, diskCheck("backup_disk", dir, cfg.MinFreeSpaceKB, fsx.DiskFree))
	}
	r.Checks = append(r.Checks, readClock())
	if settingsErr != nil {
		r.Checks = append(r.Checks, check("p2p_local", "unknown", "P2P listener settings unavailable", "config.json could not be read or its network settings are invalid.", "Review Net.ListenHost and Net.ListenPort with nomctl config show."))
	} else {
		r.Checks = append(r.Checks, localNetworkCheck(ctx, "/proc", after.Service.MainPID, settings.port))
	}
	r.Checks = append(r.Checks, externalCheck(ctx, opts, settings.port))
	journalCtx, stop := context.WithTimeout(ctx, 5*time.Second)
	journal, journalErr := execx.New("journalctl", "-u", cfg.ServiceUnit(), "--since", "1 hour ago", "--lines=1000", "--no-pager", "--quiet", "-o", "cat").Context(journalCtx).OutputLimited(1 << 20)
	stop()
	r.Checks = append(r.Checks, logChecks(journal, journalErr)...)
	if err := ctx.Err(); err != nil {
		return Report{}, err
	}
	r.FinishedAt = time.Now().UTC()
	return r, nil
}

func check(id, severity, summary, evidence, next string) Check {
	return Check{id, severity, clean(summary), clean(evidence), clean(next)}
}

// Bound and sanitize text from configuration, RPC and system utilities. Raw
// journal messages are never included in reports, including JSON reports.
func clean(s string) string {
	s = support.Redact(s)
	s = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) || unicode.In(r, unicode.Cf) {
			return ' '
		}
		return r
	}, s)
	r := []rune(s)
	if len(r) > 1024 {
		s = string(r[:1024]) + "…"
	}
	return strings.TrimSpace(s)
}

func Write(w io.Writer, r Report, asJSON bool) error {
	if asJSON {
		enc := json.NewEncoder(w)
		enc.SetIndent("", "  ")
		return enc.Encode(r)
	}
	var out strings.Builder
	fmt.Fprintf(&out, "Check my node — %s\n", clean(r.Service))
	fmt.Fprintf(&out, "znnd version: %s\nznnd commit:  %s\n", known(r.Identity.Version), known(r.Identity.Commit))
	fmt.Fprintf(&out, "Installed: %s\n  SHA-256: %s\n", clean(r.Identity.Installed.Path), digestText(r.Identity.Installed))
	fmt.Fprintf(&out, "Running:   %s\n  SHA-256: %s\n\n", known(clean(r.Identity.Running.Path)), digestText(r.Identity.Running))
	counts := map[string]int{}
	for _, c := range r.Checks {
		counts[c.Severity]++
		fmt.Fprintf(&out, "[%s] %s\n  %s\n", strings.ToUpper(c.Severity), c.Summary, c.Evidence)
		if c.NextStep != "" {
			fmt.Fprintf(&out, "  Next: %s\n", c.NextStep)
		}
	}
	fmt.Fprintf(&out, "\n%d errors, %d warnings, %d unknown checks. No changes made.\n", counts["error"], counts["warning"], counts["unknown"])
	_, err := io.WriteString(w, out.String())
	return err
}

func known(s string) string {
	if s == "" {
		return "unknown"
	}
	return s
}

func digestText(d Digest) string {
	if d.SHA256 != "" {
		return d.SHA256
	}
	return "unknown (" + known(d.Error) + ")"
}
