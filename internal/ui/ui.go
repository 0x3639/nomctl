// Package ui holds the lipgloss styles and small interactive helpers shared by
// the CLI commands and the TUI. The palette follows the gum settings of the
// original bash toolkit (green 46 accents, grey 239/242 chrome).
package ui

import (
	"fmt"
	"io"
	"log/slog"
	"os"
	"strings"
	"sync"

	"github.com/charmbracelet/bubbles/spinner"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"golang.org/x/term"

	"github.com/hypercore-one/nomctl/internal/logx"
)

// Colours used throughout.
var (
	ColorAccent = lipgloss.Color("46")
	ColorMuted  = lipgloss.Color("242")
	ColorDim    = lipgloss.Color("239")
	ColorBanner = lipgloss.Color("245")
	ColorWhite  = lipgloss.Color("#FFFFFF")
	ColorBlack  = lipgloss.Color("#000000")
)

// Styles.
var (
	StyleBanner   = lipgloss.NewStyle().Foreground(ColorBanner).Padding(1, 1)
	StyleSubtitle = lipgloss.NewStyle().Foreground(ColorMuted).Align(lipgloss.Center).Width(61)
	StyleSection  = lipgloss.NewStyle().Foreground(ColorDim).Border(lipgloss.NormalBorder()).BorderForeground(ColorDim).Padding(0, 1)
	StyleHeader   = lipgloss.NewStyle().Foreground(ColorMuted).Padding(1, 1)
	StyleAccent   = lipgloss.NewStyle().Foreground(ColorAccent)
	StyleBox      = lipgloss.NewStyle().Border(lipgloss.NormalBorder()).BorderForeground(ColorAccent).Margin(1).Padding(1, 2)
	StyleItalic   = lipgloss.NewStyle().Foreground(lipgloss.Color("240")).Italic(true).Align(lipgloss.Center).Width(61)
)

// Banner is the ASCII art shown by the TUI and the help screen.
const Banner = `
 _ __    ___   _ __ ___     ___  | |_  | |
| '_ \  / _ \ | '_ ' _ \   / __| | __| | |
| | | || (_) || | | | | | | (__  | |_  | |
|_| |_| \___/ |_| |_| |_|  \___|  \__| |_|`

var (
	debugMu sync.RWMutex
	debug   bool
)

// SetDebug switches Step into pass-through mode (no spinner, output visible),
// the equivalent of `gum spin --show-output`.
func SetDebug(on bool) {
	debugMu.Lock()
	defer debugMu.Unlock()
	debug = on
}

// IsTerminal reports whether stderr (where UI is drawn) is a TTY.
func IsTerminal() bool {
	return term.IsTerminal(int(os.Stderr.Fd()))
}

// Interactive reports whether both stdin and stderr are TTYs, i.e. prompts
// can be shown.
func Interactive() bool {
	return term.IsTerminal(int(os.Stdin.Fd())) && IsTerminal()
}

// Section prints a bordered section title such as "==== BUILD ====".
func Section(w io.Writer, title string) {
	fmt.Fprintln(w, StyleSection.Render(title))
}

// Step runs fn while showing a spinner titled title. In debug mode, or when
// stderr is not a terminal, the title is logged and fn runs with its output
// visible. While the spinner is shown, console log lines are suppressed and
// only reach the log file, exactly like `gum spin` without --show-output.
func Step(title string, fn func() error) error {
	debugMu.RLock()
	dbg := debug
	debugMu.RUnlock()
	if dbg || !IsTerminal() {
		slog.Info(title)
		return fn()
	}
	var err error
	logx.Quiet(func() { err = spin(title, fn) })
	return err
}

func spin(title string, fn func() error) error {
	m := spinModel{title: title, sp: spinner.New(spinner.WithSpinner(spinner.Meter), spinner.WithStyle(StyleAccent))}
	m.done = make(chan error, 1)
	p := tea.NewProgram(m, tea.WithOutput(os.Stderr), tea.WithInput(nil))
	go func() {
		m.done <- fn()
		p.Send(finishedMsg{})
	}()
	if _, perr := p.Run(); perr != nil {
		// Fall back to running synchronously if the TUI cannot start.
		return <-m.done
	}
	return <-m.done
}

type finishedMsg struct{}

type spinModel struct {
	title string
	sp    spinner.Model
	done  chan error
}

func (m spinModel) Init() tea.Cmd { return m.sp.Tick }

func (m spinModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case finishedMsg:
		return m, tea.Quit
	case spinner.TickMsg:
		var cmd tea.Cmd
		m.sp, cmd = m.sp.Update(msg)
		return m, cmd
	}
	return m, nil
}

func (m spinModel) View() string {
	return m.sp.View() + " " + m.title
}

// Pad right-pads s to width for simple column output.
func Pad(s string, width int) string {
	if len(s) >= width {
		return s
	}
	return s + strings.Repeat(" ", width-len(s))
}
