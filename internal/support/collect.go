package support

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/0x3639/nomctl/internal/backup"
	"github.com/0x3639/nomctl/internal/config"
	"github.com/0x3639/nomctl/internal/execx"
	"github.com/0x3639/nomctl/internal/fsx"
	"github.com/0x3639/nomctl/internal/metrics"
	"github.com/0x3639/nomctl/internal/node"
)

// Options tunes a collection run.
type Options struct {
	OutputDir string
	Since     string
	Version   string
	NodeURL   string
}

// Result is where the bundle landed.
type Result struct {
	Dir     string
	Archive string
	Markers string
}

// DefaultSince is the default journal window.
const DefaultSince = "12 hours ago"

// DefaultOutputDir is /root/nomctl-support-<host>-<UTC stamp>.
func DefaultOutputDir(now time.Time) string {
	host, err := os.Hostname()
	if err != nil || host == "" {
		host = "unknown-host"
	}
	if i := strings.IndexByte(host, '.'); i > 0 {
		host = host[:i]
	}
	return filepath.Join("/root", fmt.Sprintf("nomctl-support-%s-%s", host, now.UTC().Format("20060102T150405Z")))
}

// bundle carries state through the collection steps.
type bundle struct {
	dir  string
	cfg  config.Config
	opts Options
	unit string
}

// write creates (or truncates) a file in the bundle with the given content.
func (b *bundle) write(name, content string) {
	if err := os.WriteFile(filepath.Join(b.dir, name), []byte(content), 0o600); err != nil {
		slog.Warn("bundle: cannot write " + name + ": " + err.Error())
	}
}

// capture runs a command and stores its output (or error) in a file. It
// mirrors the script's capture(): a header, then output, best effort.
func (b *bundle) capture(name string, redact bool, cmd string, args ...string) {
	out, err := execx.New(cmd, args...).Output()
	header := fmt.Sprintf("command: %s %s\ncollected: %s\n\n", cmd, strings.Join(args, " "), time.Now().UTC().Format(time.RFC3339))
	if err != nil {
		out += "\n[error] " + err.Error()
	}
	if redact {
		out = Redact(out)
	}
	b.write(name, header+out+"\n")
}

// section runs a command and returns a titled block for a combined file.
func section(title, cmd string, args ...string) string {
	out, err := execx.Output(cmd, args...)
	if err != nil {
		out += "\n[error] " + err.Error()
	}
	return fmt.Sprintf("== %s ==\n%s\n\n", title, out)
}

// Collect gathers everything into opts.OutputDir and archives it. It is
// read-only with respect to the node: no service control, no data access
// beyond <data dir>/log.
func Collect(ctx context.Context, cfg config.Config, opts Options) (Result, error) {
	if opts.OutputDir == "" {
		opts.OutputDir = DefaultOutputDir(time.Now())
	}
	if opts.Since == "" {
		opts.Since = DefaultSince
	}
	if opts.NodeURL == "" {
		opts.NodeURL = node.DefaultURL
	}
	if err := os.MkdirAll(opts.OutputDir, 0o700); err != nil {
		return Result{}, fmt.Errorf("create %s: %w", opts.OutputDir, err)
	}
	_ = os.Chmod(opts.OutputDir, 0o700)
	b := &bundle{dir: opts.OutputDir, cfg: cfg, opts: opts, unit: cfg.ServiceUnit()}

	sampler := metrics.NewSampler(cfg)
	sampler.Node = node.New(opts.NodeURL)
	sample := sampler.Take(ctx)

	b.summary(sample)
	b.service()
	b.journals()
	b.process(sample)
	b.hostResources()
	b.appLogs()
	b.crashMarkers()
	b.coredumps()
	b.binary(sample)
	b.oomAndBoots()
	b.nodeRPC(ctx, opts.NodeURL)
	b.nomctl()
	b.write("20-status.txt", metrics.Format(sample))

	archive, err := writeArchive(opts.OutputDir)
	if err != nil {
		return Result{}, err
	}
	return Result{Dir: opts.OutputDir, Archive: archive, Markers: filepath.Join(opts.OutputDir, "13-crash-markers.log")}, nil
}

func (b *bundle) summary(s metrics.Sample) {
	host, _ := os.Hostname()
	uname, _ := execx.Output("uname", "-a")
	uptime, _ := execx.Output("uptime")
	var sb strings.Builder
	fmt.Fprintf(&sb, "host=%s\ncollected=%s\nnomctl=%s\nservice=%s\ndata-dir=%s\njournal-since=%s\nuid=%d\n\n%s\n%s\n",
		host, time.Now().UTC().Format(time.RFC3339), b.opts.Version, b.unit, b.cfg.ZnnDir, b.opts.Since, os.Getuid(), uname, uptime)
	if os.Geteuid() != 0 {
		sb.WriteString("WARNING: not running as root; journal, kernel, /proc, or coredump evidence may be incomplete.\n")
	}
	if !s.Service.Found {
		sb.WriteString("WARNING: unit " + b.unit + " not found.\n")
	}
	b.write("02-summary.txt", sb.String())
}

var showProperties = []string{"Id", "Description", "LoadState", "ActiveState", "SubState", "Result", "MainPID", "ControlPID", "NRestarts", "Restart", "RestartUSec", "Type", "User", "Group", "ExecStart", "ExecStop", "ExecStopPost", "ControlGroup", "ExecMainStartTimestamp", "ExecMainExitTimestamp", "ExecMainCode", "ExecMainStatus", "WatchdogUSec", "WatchdogTimestamp", "OOMPolicy", "MemoryCurrent", "MemoryPeak", "MemoryMax", "TasksCurrent", "TasksMax", "LimitNOFILE", "CPUUsageNSec"}

func (b *bundle) service() {
	b.capture("03-service-status.txt", false, "systemctl", "status", b.unit, "--full", "--no-pager")
	args := []string{"show", b.unit}
	for _, p := range showProperties {
		args = append(args, "-p", p)
	}
	b.capture("04-service-properties.txt", true, "systemctl", args...)
	b.capture("05-service-unit.txt", true, "systemctl", "cat", b.unit)
}

func (b *bundle) journals() {
	since := b.opts.Since
	b.capture("06-service-journal.log", false, "journalctl", "-u", b.unit, "--since", since, "-o", "short-iso-precise", "--no-pager")
	b.capture("07-kernel-journal.log", false, "journalctl", "-k", "--since", since, "-o", "short-iso-precise", "--no-pager")
	b.capture("08-system-warnings.log", false, "journalctl", "--since", since, "-p", "warning..alert", "-o", "short-iso-precise", "--no-pager")
}

func (b *bundle) process(s metrics.Sample) {
	pid := s.Service.MainPID
	var sb strings.Builder
	fmt.Fprintf(&sb, "pid=%d\ncollected=%s\n\n", pid, time.Now().UTC().Format(time.RFC3339))
	procDir := filepath.Join(metrics.DefaultProcRoot, strconv.Itoa(pid))
	if pid <= 0 || !fsx.IsDir(procDir) {
		sb.WriteString("znnd main process is not currently available\n")
	} else {
		exe, _ := os.Readlink(filepath.Join(procDir, "exe"))
		fmt.Fprintf(&sb, "== executable ==\n%s\n\n", exe)
		for _, f := range []string{"status", "limits", "io", "cgroup"} {
			data, err := os.ReadFile(filepath.Join(procDir, f))
			if err != nil {
				data = []byte("[error] " + err.Error())
			}
			fmt.Fprintf(&sb, "== %s ==\n%s\n\n", f, data)
		}
		fmt.Fprintf(&sb, "== file descriptors ==\n%d\n", s.Process.OpenFDs)
	}
	b.write("09-live-process.txt", sb.String())

	cg := s.Process.Cgroup
	var cb strings.Builder
	fmt.Fprintf(&cb, "ControlGroup=%s\npresent=%v\n", s.Service.ControlGroup(), cg.Present)
	if cg.Present {
		fmt.Fprintf(&cb, "memory.current=%d\nmemory.peak=%d\nmemory.max=%d\npids.current=%d\n", cg.MemoryCurrent, cg.MemoryPeak, cg.MemoryMax, cg.PidsCurrent)
		dir := filepath.Join(metrics.DefaultCgroupRoot, s.Service.ControlGroup())
		for _, f := range []string{"memory.events", "memory.events.local", "pids.events", "cpu.stat", "io.stat"} {
			if data, err := os.ReadFile(filepath.Join(dir, f)); err == nil {
				fmt.Fprintf(&cb, "== %s ==\n%s\n", f, data)
			}
		}
	}
	b.write("09-cgroup.txt", cb.String())
}

func (b *bundle) hostResources() {
	var sb strings.Builder
	sb.WriteString(section("memory", "free", "-h"))
	if data, err := os.ReadFile("/proc/meminfo"); err == nil {
		fmt.Fprintf(&sb, "%s\n", data)
	}
	sb.WriteString("== pressure ==\n")
	for _, f := range []string{"/proc/pressure/cpu", "/proc/pressure/io", "/proc/pressure/memory"} {
		if data, err := os.ReadFile(f); err == nil {
			fmt.Fprintf(&sb, "-- %s\n%s", f, data)
		}
	}
	sb.WriteString("\n")
	sb.WriteString(section("filesystems", "df", "-hT"))
	sb.WriteString(section("inodes", "df", "-i"))
	sb.WriteString(section("top processes by RSS", "sh", "-c", "ps -eo pid,ppid,user,stat,%cpu,%mem,rss,vsz,nlwp,etimes,comm --sort=-rss | head -50"))
	sb.WriteString(section("shell limits", "sh", "-c", "ulimit -a"))
	b.write("10-host-resources.txt", sb.String())
}

func (b *bundle) appLogs() {
	logDir := filepath.Join(b.cfg.ZnnDir, "log")
	var inv strings.Builder
	fmt.Fprintf(&inv, "data-dir=%s\nlog-dir=%s\nConfig contents are intentionally not collected.\n\n", b.cfg.ZnnDir, logDir)
	logs, err := ListLogs(logDir)
	if err != nil {
		inv.WriteString("Log directory not found or unreadable: " + err.Error() + "\n")
		b.write("11-app-log-inventory.txt", inv.String())
		return
	}
	for _, l := range logs {
		fmt.Fprintf(&inv, "%s|%d|%s\n", l.ModTime.UTC().Format(time.RFC3339), l.Size, l.Path)
	}
	b.write("11-app-log-inventory.txt", inv.String())
	tails := filepath.Join(b.dir, "12-app-log-tails")
	if err := os.MkdirAll(tails, 0o700); err != nil {
		return
	}
	for i, l := range logs {
		if i >= MaxLogFiles {
			break
		}
		dst := filepath.Join(tails, fmt.Sprintf("%02d-%s.tail", i+1, filepath.Base(l.Path)))
		if err := TailLog(l.Path, dst, LogTailBytes); err != nil {
			slog.Warn("bundle: cannot tail " + l.Path + ": " + err.Error())
		}
	}
}

func (b *bundle) crashMarkers() {
	out, err := os.OpenFile(filepath.Join(b.dir, "13-crash-markers.log"), os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return
	}
	defer func() { _ = out.Close() }()
	sources := []string{filepath.Join(b.dir, "06-service-journal.log"), filepath.Join(b.dir, "07-kernel-journal.log")}
	tails, _ := filepath.Glob(filepath.Join(b.dir, "12-app-log-tails", "*"))
	sources = append(sources, tails...)
	for _, src := range sources {
		f, err := os.Open(src)
		if err != nil {
			continue
		}
		_ = FilterCrashMarkers(f, out)
		_ = f.Close()
	}
}

func (b *bundle) coredumps() {
	if !execx.Exists("coredumpctl") {
		b.write("14-coredumps-list.txt", "coredumpctl is not installed\n")
		b.write("15-coredumps-info.txt", "coredumpctl is not installed\n")
		return
	}
	b.capture("14-coredumps-list.txt", false, "coredumpctl", "--since", b.opts.Since, "list", b.cfg.BinaryName, "--no-pager")
	b.capture("15-coredumps-info.txt", false, "coredumpctl", "--since", b.opts.Since, "info", b.cfg.BinaryName, "--no-pager")
}

func (b *bundle) binary(s metrics.Sample) {
	var sb strings.Builder
	exe := ""
	if s.Service.MainPID > 0 {
		exe, _ = os.Readlink(filepath.Join(metrics.DefaultProcRoot, strconv.Itoa(s.Service.MainPID), "exe"))
	}
	if exe == "" {
		exe = b.cfg.BinaryPath()
	}
	fmt.Fprintf(&sb, "executable=%s\n", exe)
	st, err := os.Stat(exe)
	if err != nil {
		fmt.Fprintf(&sb, "[error] %s\n", err)
		b.write("16-binary.txt", sb.String())
		return
	}
	fmt.Fprintf(&sb, "size=%d\nmodified=%s\nmode=%s\n", st.Size(), st.ModTime().UTC().Format(time.RFC3339), st.Mode())
	if sum, err := backup.SHA256File(exe); err == nil {
		fmt.Fprintf(&sb, "sha256=%s\n", sum)
	}
	if goBin := b.cfg.GoBinary(); fsx.Exists(goBin) {
		if out, err := execx.Output(goBin, "version", "-m", exe); err == nil {
			fmt.Fprintf(&sb, "\n%s\n", out)
		}
	}
	b.write("16-binary.txt", sb.String())
}

func (b *bundle) oomAndBoots() {
	var sb strings.Builder
	sb.WriteString(section("boot history", "journalctl", "--list-boots", "--no-pager"))
	sb.WriteString(section("systemd-oomd", "systemctl", "status", "systemd-oomd", "--full", "--no-pager"))
	if execx.Exists("oomctl") {
		sb.WriteString(section("oomctl", "oomctl"))
	}
	b.write("17-oom-and-boots.txt", sb.String())
}

func (b *bundle) nodeRPC(ctx context.Context, url string) {
	c := node.New(url)
	doc := map[string]any{"url": url, "collected": time.Now().UTC().Format(time.RFC3339)}
	call := func(key string, fn func() (any, error)) {
		v, err := fn()
		if err != nil {
			doc[key] = map[string]string{"error": err.Error()}
			return
		}
		doc[key] = v
	}
	call("syncInfo", func() (any, error) { return c.SyncInfo(ctx) })
	call("processInfo", func() (any, error) { return c.ProcessInfo(ctx) })
	call("osInfo", func() (any, error) { return c.OsInfo(ctx) })
	call("frontierMomentum", func() (any, error) { return c.FrontierMomentum(ctx) })
	call("networkInfo", func() (any, error) {
		n, err := c.NetworkInfo(ctx)
		if err != nil {
			return nil, err
		}
		raw, err := json.Marshal(n)
		if err != nil {
			return nil, err
		}
		red, err := RedactPeers(raw)
		if err != nil {
			return nil, err
		}
		return json.RawMessage(red), nil
	})
	data, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		data = []byte(`{"error":` + strconv.Quote(err.Error()) + `}`)
	}
	b.write("18-node-rpc.json", string(data)+"\n")
}

func (b *bundle) nomctl() {
	var sb strings.Builder
	fmt.Fprintf(&sb, "version=%s\nconfig=%s\n\n", b.opts.Version, b.cfg.Redacted())
	sb.WriteString(section("backup timer", "systemctl", "list-timers", backup.TimerName+".timer", "--all", "--no-pager"))
	fmt.Fprintf(&sb, "== last 200 lines of %s ==\n", b.cfg.LogFile)
	data, err := os.ReadFile(b.cfg.LogFile)
	if err != nil {
		fmt.Fprintf(&sb, "[error] %s\n", err)
	} else {
		lines := strings.Split(strings.TrimRight(string(data), "\n"), "\n")
		if len(lines) > 200 {
			lines = lines[len(lines)-200:]
		}
		sb.WriteString(Redact(strings.Join(lines, "\n")) + "\n")
	}
	b.write("19-nomctl.txt", sb.String())
}

// writeArchive tars dir into dir.tar.gz (entries prefixed with the base name).
func writeArchive(dir string) (string, error) {
	archive := strings.TrimSuffix(dir, "/") + ".tar.gz"
	f, err := os.OpenFile(archive, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return "", err
	}
	gz := gzip.NewWriter(f)
	tw := tar.NewWriter(gz)
	base := filepath.Base(dir)
	err = filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(dir, path)
		if err != nil {
			return err
		}
		hdr, err := tar.FileInfoHeader(info, "")
		if err != nil {
			return err
		}
		hdr.Name = filepath.ToSlash(filepath.Join(base, rel))
		if info.IsDir() {
			hdr.Name += "/"
		}
		if err := tw.WriteHeader(hdr); err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return nil
		}
		src, err := os.Open(path)
		if err != nil {
			return err
		}
		defer func() { _ = src.Close() }()
		_, err = io.Copy(tw, src)
		return err
	})
	if err != nil {
		_ = tw.Close()
		_ = gz.Close()
		_ = f.Close()
		_ = os.Remove(archive)
		return "", err
	}
	for _, c := range []io.Closer{tw, gz, f} {
		if err := c.Close(); err != nil {
			return "", err
		}
	}
	return archive, nil
}
