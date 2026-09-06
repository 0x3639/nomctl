package metrics

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/0x3639/nomctl/internal/config"
	"github.com/0x3639/nomctl/internal/fsx"
	"github.com/0x3639/nomctl/internal/node"
)

// StalledAfter is how old the frontier momentum may be on a node that
// reports itself synced before it is flagged as stalled.
const StalledAfter = 2 * time.Minute

// rateWindow is how many (time, height) points feed the sync rate.
const rateWindow = 30

// ServiceSample is the systemd view of the node.
type ServiceSample struct {
	Unit        string
	Found       bool
	ActiveState string
	SubState    string
	Result      string
	MainPID     int
	NRestarts   int
	Since       time.Time
	Error       string `json:",omitempty"`

	controlGroup string
}

// ControlGroup is the cgroup path of the unit, used for cgroup files.
func (s ServiceSample) ControlGroup() string { return s.controlGroup }

// ProcessSample is the /proc and cgroup view of the node process.
type ProcessSample struct {
	Present    bool
	CPUPercent float64
	RSS        uint64
	VmSwap     uint64
	VmPeak     uint64
	Threads    int
	OpenFDs    int
	FDLimit    uint64
	ReadBytes  uint64
	WriteBytes uint64
	Cgroup     CgroupStats
}

// HostSample is the machine-wide view.
type HostSample struct {
	Load1        float64
	Load5        float64
	Load15       float64
	MemTotal     uint64
	MemAvailable uint64
	DataDir      string
	DataDirFree  uint64
	DataDirTotal uint64
	Pressure     Pressure
}

// NodeSample is the RPC view plus derived values.
type NodeSample struct {
	URL             string
	Reachable       bool
	Error           string `json:",omitempty"`
	State           node.SyncState
	StateText       string
	CurrentHeight   uint64
	TargetHeight    uint64
	NumPeers        int
	Version         string
	Commit          string
	Goroutines      int
	FrontierHeight  uint64
	FrontierTime    time.Time
	FrontierAge     time.Duration
	MomentumsPerSec float64
	ETA             time.Duration
	ETAKnown        bool
	Stalled         bool
}

// Sample is one observation of everything.
type Sample struct {
	Taken   time.Time
	Service ServiceSample
	Process ProcessSample
	Host    HostSample
	Node    NodeSample
}

type heightPoint struct {
	t time.Time
	h uint64
}

// Sampler takes Samples and keeps the little state needed for rates.
type Sampler struct {
	ProcRoot   string
	CgroupRoot string
	Node       *node.Client
	Now        func() time.Time
	ClockTicks float64

	unit      string
	dataDir   string
	readProps func(unit string) (ServiceProps, error)
	diskFree  func(path string) (free, total uint64, err error)

	prevTicks uint64
	prevTime  time.Time
	prevPID   int
	heights   []heightPoint
}

// NewSampler configures a Sampler for the node described by cfg.
func NewSampler(cfg config.Config) *Sampler {
	return &Sampler{
		ProcRoot:   DefaultProcRoot,
		CgroupRoot: DefaultCgroupRoot,
		Node:       node.New(node.DefaultURL),
		Now:        time.Now,
		ClockTicks: 100,
		unit:       cfg.ServiceName,
		dataDir:    cfg.ZnnDir,
		readProps:  ReadServiceProps,
		diskFree:   diskFree,
	}
}

func diskFree(path string) (uint64, uint64, error) {
	availKB, usedPct, err := fsx.DiskFree(path)
	if err != nil {
		return 0, 0, err
	}
	free := uint64(availKB) * 1024 //nolint:gosec // non-negative by construction
	if usedPct >= 100 {
		return free, free, nil
	}
	total := uint64(float64(free) / (1 - float64(usedPct)/100))
	return free, total, nil
}

// Take samples everything now. Failures are recorded inside the Sample.
func (s *Sampler) Take(ctx context.Context) Sample {
	now := s.Now()
	smp := Sample{Taken: now}
	smp.Service = s.takeService()
	if smp.Service.MainPID > 0 {
		smp.Process = s.takeProcess(smp.Service.MainPID, smp.Service.controlGroup, now)
	} else {
		s.prevPID = 0
	}
	smp.Host = s.takeHost()
	smp.Node = s.takeNode(ctx, now)
	return smp
}

func (s *Sampler) takeService() ServiceSample {
	out := ServiceSample{Unit: s.unit}
	props, err := s.readProps(s.unit)
	if err != nil {
		out.Error = err.Error()
		return out
	}
	// systemctl show answers for unknown units too, with LoadState=not-found
	// and ActiveState=inactive; treat "inactive with no cgroup and no pid"
	// as not found, which also covers a stopped unit well enough for display.
	out.Found = props.ActiveState != "" && (props.ActiveState != "inactive" || props.MainPID > 0 || props.ControlGroup != "")
	if !out.Found {
		out.Error = "unit not found"
	}
	out.ActiveState = props.ActiveState
	out.SubState = props.SubState
	out.Result = props.Result
	out.MainPID = props.MainPID
	out.NRestarts = props.NRestarts
	out.Since = props.ExecMainStart
	out.controlGroup = props.ControlGroup
	return out
}

func (s *Sampler) takeProcess(pid int, controlGroup string, now time.Time) ProcessSample {
	var p ProcessSample
	st, err := ReadProcStatus(s.ProcRoot, pid)
	if err != nil {
		return p
	}
	p.Present = true
	p.RSS, p.VmSwap, p.VmPeak, p.Threads = st.VmRSS, st.VmSwap, st.VmPeak, st.Threads
	if stat, err := ReadProcStat(s.ProcRoot, pid); err == nil {
		ticks := stat.UTime + stat.STime
		if s.prevPID == pid && !s.prevTime.IsZero() && now.After(s.prevTime) && ticks >= s.prevTicks {
			secs := now.Sub(s.prevTime).Seconds()
			p.CPUPercent = float64(ticks-s.prevTicks) / s.ClockTicks / secs * 100
		}
		s.prevTicks, s.prevTime, s.prevPID = ticks, now, pid
	}
	if pio, err := ReadProcIO(s.ProcRoot, pid); err == nil {
		p.ReadBytes, p.WriteBytes = pio.ReadBytes, pio.WriteBytes
	}
	p.FDLimit, _ = ReadFDLimit(s.ProcRoot, pid)
	p.OpenFDs, _ = CountFDs(s.ProcRoot, pid)
	p.Cgroup = ReadCgroup(s.CgroupRoot, controlGroup)
	return p
}

func (s *Sampler) takeHost() HostSample {
	var h HostSample
	h.Load1, h.Load5, h.Load15, _ = ReadLoadAvg(s.ProcRoot)
	if m, err := ReadMemInfo(s.ProcRoot); err == nil {
		h.MemTotal, h.MemAvailable = m.Total, m.Available
	}
	h.DataDir = s.dataDir
	h.DataDirFree, h.DataDirTotal, _ = s.diskFree(s.dataDir)
	h.Pressure = ReadPressure(s.ProcRoot)
	return h
}

func (s *Sampler) takeNode(ctx context.Context, now time.Time) NodeSample {
	n := NodeSample{URL: s.Node.URL}
	snap := s.Node.Snapshot(ctx)
	if snap.Err != nil {
		n.Error = snap.Err.Error()
		s.heights = nil
		return n
	}
	n.Reachable = true
	n.State = snap.Sync.State
	n.StateText = snap.Sync.State.String()
	n.CurrentHeight, n.TargetHeight = snap.Sync.CurrentHeight, snap.Sync.TargetHeight
	n.NumPeers = snap.Network.NumPeers
	n.Version, n.Commit = snap.Process.Version, snap.Process.Commit
	n.Goroutines = snap.Os.NumGoroutine
	n.FrontierHeight = snap.Frontier.Height
	n.FrontierTime = snap.Frontier.Time()
	n.FrontierAge = now.Sub(n.FrontierTime)
	n.Stalled = n.State == node.Done && n.FrontierAge > StalledAfter

	s.heights = append(s.heights, heightPoint{now, n.CurrentHeight})
	if len(s.heights) > rateWindow {
		s.heights = s.heights[len(s.heights)-rateWindow:]
	}
	n.MomentumsPerSec = rate(s.heights)
	if n.State == node.Syncing && n.MomentumsPerSec > 0 && n.TargetHeight > n.CurrentHeight {
		n.ETA = time.Duration(float64(n.TargetHeight-n.CurrentHeight)/n.MomentumsPerSec) * time.Second
		n.ETAKnown = true
	}
	return n
}

// rate is the height change per second between the oldest and newest point.
func rate(points []heightPoint) float64 {
	if len(points) < 2 {
		return 0
	}
	first, last := points[0], points[len(points)-1]
	secs := last.t.Sub(first.t).Seconds()
	if secs <= 0 || last.h < first.h {
		return 0
	}
	return float64(last.h-first.h) / secs
}

// Format renders the sample as the aligned text block of nomctl status.
func Format(s Sample) string {
	var b strings.Builder
	line := func(label, text string) { fmt.Fprintf(&b, "%-9s %s\n", label, text) }

	if !s.Service.Found {
		line("Service", fmt.Sprintf("%s: %s", s.Service.Unit, s.Service.Error))
	} else {
		up := ""
		if !s.Service.Since.IsZero() && s.Service.ActiveState == "active" {
			up = ", up " + HumanDuration(s.Taken.Sub(s.Service.Since))
		}
		line("Service", fmt.Sprintf("%s %s (%s), pid %d, %d restarts%s", s.Service.Unit, s.Service.ActiveState, s.Service.SubState, s.Service.MainPID, s.Service.NRestarts, up))
	}

	if !s.Node.Reachable {
		line("Node", fmt.Sprintf("node rpc unreachable at %s: %s", strings.TrimPrefix(s.Node.URL, "http://"), s.Node.Error))
	} else {
		n := s.Node
		sync := n.StateText
		if n.TargetHeight > 0 {
			sync += fmt.Sprintf(" %s / %s (%.1f%%)", Commas(n.CurrentHeight), Commas(n.TargetHeight), float64(n.CurrentHeight)/float64(n.TargetHeight)*100)
		}
		if n.MomentumsPerSec > 0 {
			sync += fmt.Sprintf(", %.1f mom/s", n.MomentumsPerSec)
		}
		if n.ETAKnown {
			sync += ", ETA " + HumanDuration(n.ETA)
		}
		if n.Stalled {
			sync += " [STALLED]"
		}
		line("Node", fmt.Sprintf("znnd %s (%s), %s", n.Version, n.Commit, sync))
		line("Peers", fmt.Sprintf("%d connected", n.NumPeers))
		line("Frontier", fmt.Sprintf("height %s, %s ago", Commas(n.FrontierHeight), HumanDuration(n.FrontierAge)))
	}

	if s.Process.Present {
		p := s.Process
		fds := fmt.Sprintf("%d", p.OpenFDs)
		if p.FDLimit > 0 {
			fds += fmt.Sprintf(" / %d", p.FDLimit)
		}
		line("Process", fmt.Sprintf("cpu %.1f%%, rss %s, threads %d, open files %s", p.CPUPercent, HumanBytes(p.RSS), p.Threads, fds))
	} else {
		line("Process", "not running")
	}
	h := s.Host
	line("Host", fmt.Sprintf("load %.2f %.2f %.2f, mem %s / %s available, %s %s free", h.Load1, h.Load5, h.Load15, HumanBytes(h.MemAvailable), HumanBytes(h.MemTotal), h.DataDir, HumanBytes(h.DataDirFree)))
	line("Pressure", fmt.Sprintf("cpu %.1f%%, io %.1f%%, mem %.1f%%", h.Pressure.CPU, h.Pressure.IO, h.Pressure.Memory))
	return b.String()
}

// HumanBytes renders bytes as KiB/MiB/GiB with one decimal.
func HumanBytes(n uint64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := uint64(unit), 0
	for m := n / unit; m >= unit; m /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(n)/float64(div), "KMGTPE"[exp])
}

// HumanDuration renders a duration as "3d 4h", "4h 5m", "5m 6s" or "6s".
func HumanDuration(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	d = d.Round(time.Second)
	days := int(d.Hours()) / 24
	hours := int(d.Hours()) % 24
	mins := int(d.Minutes()) % 60
	secs := int(d.Seconds()) % 60
	switch {
	case days > 0:
		return fmt.Sprintf("%dd %dh", days, hours)
	case hours > 0:
		return fmt.Sprintf("%dh %dm", hours, mins)
	case mins > 0:
		return fmt.Sprintf("%dm %ds", mins, secs)
	default:
		return fmt.Sprintf("%ds", secs)
	}
}

// Commas renders 1234567 as "1,234,567".
func Commas(n uint64) string {
	s := fmt.Sprint(n)
	if len(s) <= 3 {
		return s
	}
	var b strings.Builder
	pre := len(s) % 3
	if pre > 0 {
		b.WriteString(s[:pre])
	}
	for i := pre; i < len(s); i += 3 {
		if b.Len() > 0 {
			b.WriteByte(',')
		}
		b.WriteString(s[i : i+3])
	}
	return b.String()
}
