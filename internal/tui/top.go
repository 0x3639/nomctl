package tui

import (
	"context"
	"fmt"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/0x3639/nomctl/internal/config"
	"github.com/0x3639/nomctl/internal/metrics"
	"github.com/0x3639/nomctl/internal/ui"
)

// historyLen is how many rate points the sparkline keeps.
const historyLen = 60

// Top runs the live dashboard until q, Esc or Ctrl+C.
func Top(cfg config.Config, interval time.Duration) error {
	m := newTopModel(metrics.NewSampler(cfg), interval)
	_, err := tea.NewProgram(m, tea.WithAltScreen()).Run()
	return err
}

type sampleMsg metrics.Sample
type tickMsg time.Time

type topModel struct {
	sampler  *metrics.Sampler
	interval time.Duration
	sample   metrics.Sample
	history  []float64
	width    int
	sampling bool
}

func newTopModel(s *metrics.Sampler, interval time.Duration) topModel {
	return topModel{sampler: s, interval: interval, width: 80}
}

func (m topModel) Init() tea.Cmd { return tea.Batch(m.takeSample(), m.tick()) }

func (m topModel) tick() tea.Cmd {
	return tea.Tick(m.interval, func(t time.Time) tea.Msg { return tickMsg(t) })
}

func (m topModel) takeSample() tea.Cmd {
	s := m.sampler
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		return sampleMsg(s.Take(ctx))
	}
}

func (m topModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		switch msg.String() {
		case "q", "esc", "ctrl+c":
			return m, tea.Quit
		}
	case tea.WindowSizeMsg:
		m.width = msg.Width
	case tickMsg:
		if m.sampling {
			return m, m.tick()
		}
		m.sampling = true
		return m, tea.Batch(m.takeSample(), m.tick())
	case sampleMsg:
		m.sampling = false
		m.sample = metrics.Sample(msg)
		if m.sample.Node.Reachable {
			m.history = append(m.history, m.sample.Node.MomentumsPerSec)
			if len(m.history) > historyLen {
				m.history = m.history[len(m.history)-historyLen:]
			}
		}
	}
	return m, nil
}

func (m topModel) View() string {
	if m.sample.Taken.IsZero() {
		return "Sampling…\n"
	}
	return renderTop(m.sample, m.history, m.width, time.Now())
}

var (
	stylePanelTitle = lipgloss.NewStyle().Bold(true).Foreground(ui.ColorAccent)
	stylePanel      = lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(ui.ColorDim).Padding(0, 1)
	styleWarn       = lipgloss.NewStyle().Foreground(lipgloss.Color("214")).Bold(true)
	styleBad        = lipgloss.NewStyle().Foreground(lipgloss.Color("196")).Bold(true)
	styleFooter     = lipgloss.NewStyle().Foreground(ui.ColorMuted)
	styleDim        = lipgloss.NewStyle().Foreground(ui.ColorDim)
)

// renderTop draws the three panels for a sample. width is the terminal
// width; now is used for the "sampled Ns ago" footer.
func renderTop(s metrics.Sample, history []float64, width int, now time.Time) string {
	inner := max(40, width-4)
	panel := func(title string, lines ...string) string {
		body := stylePanelTitle.Render(title) + "\n" + strings.Join(lines, "\n")
		return stylePanel.Width(inner).Render(body)
	}

	return strings.Join([]string{
		panel("NODE", nodeLines(s.Node, history, inner)...),
		panel("PROCESS", processLines(s)...),
		panel("HOST", hostLines(s.Host)...),
		styleFooter.Render(fmt.Sprintf("sampled %s ago   q quit", metrics.HumanDuration(now.Sub(s.Taken)))),
	}, "\n") + "\n"
}

func nodeLines(n metrics.NodeSample, history []float64, inner int) []string {
	if !n.Reachable {
		return []string{styleBad.Render("node rpc unreachable") + " at " + strings.TrimPrefix(n.URL, "http://") + ": " + n.Error}
	}
	state := n.StateText
	if n.Stalled {
		state = styleBad.Render(state + " but stalled")
	}
	lines := []string{fmt.Sprintf("znnd %s (%s)   %s   %d peers", n.Version, n.Commit, state, n.NumPeers)}
	if n.TargetHeight > 0 {
		pct := float64(n.CurrentHeight) / float64(n.TargetHeight)
		lines = append(lines,
			fmt.Sprintf("height %s / %s (%.1f%%)", metrics.Commas(n.CurrentHeight), metrics.Commas(n.TargetHeight), pct*100),
			progressBar(pct, min(60, inner-4)))
	}
	rateLine := fmt.Sprintf("%.1f mom/s", n.MomentumsPerSec)
	if n.ETAKnown {
		rateLine += "   ETA " + metrics.HumanDuration(n.ETA)
	}
	if sl := sparkline(history, min(30, inner-len(rateLine)-6)); sl != "" {
		rateLine += "   " + ui.StyleAccent.Render(sl)
	}
	lines = append(lines, rateLine,
		fmt.Sprintf("frontier %s, %s ago", metrics.Commas(n.FrontierHeight), metrics.HumanDuration(n.FrontierAge)))
	return lines
}

func processLines(s metrics.Sample) []string {
	svc := s.Service
	svcText := fmt.Sprintf("%s %s (%s)", svc.Unit, svc.ActiveState, svc.SubState)
	if !svc.Found {
		svcText = fmt.Sprintf("%s: %s", svc.Unit, svc.Error)
	}
	if svc.ActiveState != "active" {
		svcText = styleBad.Render(svcText)
	}
	if !svc.Since.IsZero() && svc.ActiveState == "active" {
		svcText += "   up " + metrics.HumanDuration(s.Taken.Sub(svc.Since))
	}
	restarts := fmt.Sprintf("%d restarts", svc.NRestarts)
	if svc.NRestarts > 0 {
		restarts = styleWarn.Render(restarts)
	}
	lines := []string{svcText + "   " + restarts}
	p := s.Process
	if !p.Present {
		return append(lines, styleBad.Render("process not running"))
	}
	fds := fmt.Sprintf("%d", p.OpenFDs)
	if p.FDLimit > 0 {
		fds += fmt.Sprintf(" / %d", p.FDLimit)
		if float64(p.OpenFDs) > float64(p.FDLimit)*0.8 {
			fds = styleWarn.Render(fds)
		}
	}
	lines = append(lines, fmt.Sprintf("cpu %.1f%%   rss %s   threads %d   open files %s", p.CPUPercent, metrics.HumanBytes(p.RSS), p.Threads, fds))
	if p.Cgroup.Present {
		lines = append(lines, fmt.Sprintf("cgroup mem %s (peak %s)   pids %d", metrics.HumanBytes(p.Cgroup.MemoryCurrent), metrics.HumanBytes(p.Cgroup.MemoryPeak), p.Cgroup.PidsCurrent))
	}
	return lines
}

func hostLines(h metrics.HostSample) []string {
	return []string{
		fmt.Sprintf("load %.2f %.2f %.2f   mem %s / %s available", h.Load1, h.Load5, h.Load15, metrics.HumanBytes(h.MemAvailable), metrics.HumanBytes(h.MemTotal)),
		fmt.Sprintf("%s %s free of %s", h.DataDir, metrics.HumanBytes(h.DataDirFree), metrics.HumanBytes(h.DataDirTotal)),
		fmt.Sprintf("pressure cpu %.1f%%   io %.1f%%   mem %.1f%%", h.Pressure.CPU, h.Pressure.IO, h.Pressure.Memory),
	}
}

// progressBar renders a filled bar of the given width for pct in [0,1].
func progressBar(pct float64, width int) string {
	if width < 4 {
		width = 4
	}
	pct = min(max(pct, 0), 1)
	filled := int(pct * float64(width))
	return ui.StyleAccent.Render(strings.Repeat("█", filled)) + styleDim.Render(strings.Repeat("░", width-filled))
}

var sparkRunes = []rune("▁▂▃▄▅▆▇█")

// sparkline renders the newest `width` values scaled to their maximum.
func sparkline(values []float64, width int) string {
	if len(values) == 0 || width <= 0 {
		return ""
	}
	if len(values) > width {
		values = values[len(values)-width:]
	}
	maxV := 0.0
	for _, v := range values {
		maxV = max(maxV, v)
	}
	var b strings.Builder
	for _, v := range values {
		idx := 0
		if maxV > 0 {
			idx = int(v / maxV * float64(len(sparkRunes)-1))
		}
		b.WriteRune(sparkRunes[idx])
	}
	return b.String()
}
