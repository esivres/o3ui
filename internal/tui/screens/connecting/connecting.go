// Package connecting renders the screen shown while a tunnel comes up.
// Drives Service.Connect in the background and polls session status until
// the tunnel reaches the connected state (or fails).
package connecting

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/spinner"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/esivres/openvpn3ui/internal/app"
	"github.com/esivres/openvpn3ui/internal/ovpn"
	"github.com/esivres/openvpn3ui/internal/tui/components"
	"github.com/esivres/openvpn3ui/internal/tui/theme"
)

var _ = ovpn.StatusChangeEvent{} // keep import live for the case branch above

// DoneMsg signals the root that the tunnel reached the connected state.
type DoneMsg struct{ SessionPath string }

// FailedMsg signals the root that the connect attempt failed; user is
// expected to dismiss the screen with Esc and try again.
type FailedMsg struct{ Err error }

// CancelMsg is emitted when the user presses Esc — root drops back to list.
type CancelMsg struct{}

type connectResultMsg struct {
	sessionPath string
	err         error
}

type statusTickMsg struct{}

type Model struct {
	svc        *app.Service
	width      int
	height     int
	configPath string
	configName string

	sessionPath string
	status      ovpn.Status
	connectErr  error
	finished    bool

	// connect runs in its own goroutine; cancel is invoked from the
	// Esc handler so a stuck Auth.Provide (e.g. user closed the auth
	// modal) doesn't leak that goroutine for the rest of the session.
	connectCtx    context.Context
	connectCancel context.CancelFunc

	spinner spinner.Model
	started time.Time
}

func New(svc *app.Service, configPath, configName string) *Model {
	sp := spinner.New()
	// Line spinner — ASCII-only "|/-\". Renders in every terminal font;
	// the braille "Dot" preset shows as "::" on systems without the
	// Unicode glyphs.
	sp.Spinner = spinner.Line
	sp.Style = lipgloss.NewStyle().Foreground(theme.Pink).Bold(true)
	ctx, cancel := context.WithCancel(context.Background())
	return &Model{
		svc:           svc,
		configPath:    configPath,
		configName:    configName,
		connectCtx:    ctx,
		connectCancel: cancel,
		spinner:       sp,
		started:       time.Now(),
	}
}

// HelpKeys feeds the `?` overlay. The connecting screen owns one
// thing: cancel the attempt.
func (m *Model) HelpKeys() []components.KeyHelp {
	return []components.KeyHelp{
		{Key: "esc", Label: "cancel the connect attempt"},
	}
}

func (m *Model) Init() tea.Cmd {
	return tea.Batch(m.connectCmd(), m.spinner.Tick, statusTick())
}

func (m *Model) SetSize(w, h int) { m.width, m.height = w, h }

func (m *Model) connectCmd() tea.Cmd {
	cp := m.configPath
	svc := m.svc
	ctx := m.connectCtx
	return func() tea.Msg {
		path, err := svc.Connect(ctx, cp)
		return connectResultMsg{sessionPath: path, err: err}
	}
}

func statusTick() tea.Cmd {
	return tea.Tick(500*time.Millisecond, func(time.Time) tea.Msg {
		return statusTickMsg{}
	})
}

func (m *Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.SetSize(msg.Width, msg.Height)

	case tea.KeyMsg:
		if msg.String() == "esc" {
			// Cancel the in-flight Connect goroutine before yielding to
			// root — otherwise it keeps blocking on Prompter.Ask /
			// PendingInputs after our screen is gone.
			if m.connectCancel != nil {
				m.connectCancel()
			}
			return m, func() tea.Msg { return CancelMsg{} }
		}

	case connectResultMsg:
		m.sessionPath = msg.sessionPath
		m.connectErr = msg.err
		if msg.err != nil {
			m.finished = true
			return m, func() tea.Msg { return FailedMsg{Err: msg.err} }
		}

	case statusTickMsg:
		if m.finished {
			return m, nil
		}
		// Refresh status if we have a session path; reschedule.
		if m.sessionPath != "" {
			if s, err := m.fetchStatus(); err == nil {
				m.status = s.Status
				if m.status.IsConnected() {
					m.finished = true
					return m, func() tea.Msg { return DoneMsg{SessionPath: m.sessionPath} }
				}
			}
		}
		return m, statusTick()

	case spinner.TickMsg:
		var cmd tea.Cmd
		m.spinner, cmd = m.spinner.Update(msg)
		return m, cmd

	case ovpn.StatusChangeEvent:
		// Real-time path: react instantly when openvpn3 reports the
		// tunnel is up, instead of waiting for the next 500ms tick.
		if m.sessionPath != "" && msg.Path != m.sessionPath {
			return m, nil
		}
		m.status = msg.Status
		if m.status.IsConnected() {
			m.finished = true
			return m, func() tea.Msg { return DoneMsg{SessionPath: m.sessionPath} }
		}

	case ovpn.AttentionRequiredEvent:
		// Diagnostic — we already drive UserInput via PendingInputs,
		// but logging here gives the user feedback during the Connect
		// flow that the server asked us something.
		if m.sessionPath != "" && msg.Path == m.sessionPath {
			m.status.Message = "auth challenge: " + msg.Message
		}
	}
	return m, nil
}

func (m *Model) fetchStatus() (ovpn.Session, error) {
	sessions, err := m.svc.ListSessions()
	if err != nil {
		return ovpn.Session{}, err
	}
	// Index-based range — ovpn.Session is ~128 bytes, copying once
	// per iteration was a gocritic rangeValCopy hit.
	for i := range sessions {
		if sessions[i].Path == m.sessionPath {
			return sessions[i], nil
		}
	}
	return ovpn.Session{}, fmt.Errorf("session not found")
}

func (m *Model) View() string {
	if m.width == 0 {
		return "loading…"
	}

	pills := []string{}
	if m.sessionPath != "" {
		short := shortenSession(m.sessionPath)
		pills = append(pills, components.Pill("⟳ "+short, theme.FgBright, theme.PurpleDp))
	}
	switch {
	case m.connectErr != nil:
		pills = append(pills, components.Pill("FAILED", theme.Bg, theme.Red))
	case m.status.IsConnected():
		pills = append(pills, components.Pill("CONNECTED", theme.Bg, theme.Mint))
	default:
		pills = append(pills, components.Pill("AUTH PENDING", theme.Bg, theme.Peach))
	}
	header := components.HeaderBar("ovpn3", "connecting", pills, m.width)

	// Status box: spinner + progress + steps. Progress here is cosmetic
	// time-based — openvpn3 emits StatusChange signals we don't yet
	// subscribe to, so the percentage is a "we're working" hint, not a
	// real measurement.
	progress := m.renderProgress()
	statusBox := components.Box{
		Title:       theme.AccentPink.Render("◆ ") + m.configName,
		Content:     progress,
		Width:       m.width - 4,
		BorderColor: theme.Purple,
		Glow:        true,
	}.Render()

	logBox := components.Box{
		Title:       theme.AccentMint.Render("▤ ") + "log",
		Content:     m.renderLog(),
		Width:       m.width - 4,
		BorderColor: theme.Border,
	}.Render()

	help := components.HelpBar([]components.KeyHelp{
		{Key: "esc", Label: "cancel"},
		{Key: "q", Label: "quit"},
	}, m.width)

	return lipgloss.JoinVertical(lipgloss.Left, header, "", statusBox, "", logBox, "", help)
}

// renderProgress shows the live state: spinner, elapsed seconds, the
// honest connection-phase pill, and the step ribbon. The old
// percentage progress-bar was removed — openvpn3 doesn't expose a
// real ETA, so a bar that always reached 95% in 12 seconds was
// actively misleading on slow handshakes. The spinner is the truthful
// "we're working" affordance; the steps below are the truthful
// "where in the handshake we are".
func (m *Model) renderProgress() string {
	elapsed := time.Since(m.started)
	spin := m.spinner.View()

	var pillText string
	var pillBg lipgloss.Color
	switch {
	case m.connectErr != nil:
		pillText = "failed"
		pillBg = theme.Red
	case m.status.IsConnected():
		pillText = "connected"
		pillBg = theme.Mint
	default:
		pillText = "auth"
		pillBg = theme.Peach
	}

	elapsedStr := fmt.Sprintf("%ds elapsed", int(elapsed.Seconds()))
	line := lipgloss.JoinHorizontal(lipgloss.Top,
		spin, "  ",
		theme.Dim.Render(elapsedStr), "   ",
		components.Pill(pillText, theme.Bg, pillBg),
	)

	return line + "\n" + m.renderSteps()
}

func (m *Model) renderSteps() string {
	type step struct {
		label  string
		state  rune // ✓ done, ● active, ○ pending
		colour lipgloss.Color
	}
	// We don't get fine-grained progress events yet, so derive coarse
	// state from what we *do* know: did we get a session path? has the
	// status reached connected? Everything else is a static prediction.
	hasSession := m.sessionPath != ""
	connected := m.status.IsConnected()

	steps := []step{
		{label: "session", state: '○', colour: theme.FgSubtle},
		{label: "auth", state: '○', colour: theme.FgSubtle},
		{label: "tunnel", state: '○', colour: theme.FgSubtle},
	}
	if hasSession {
		steps[0] = step{"session", '✓', theme.Mint}
		steps[1] = step{"auth", '●', theme.Pink}
	}
	if connected {
		steps[1] = step{"auth", '✓', theme.Mint}
		steps[2] = step{"tunnel", '✓', theme.Mint}
	}
	if m.connectErr != nil {
		// Mark the in-progress step as failed.
		for i := range steps {
			if steps[i].state == '●' {
				steps[i] = step{steps[i].label, '✗', theme.Red}
			}
		}
	}

	var parts []string
	for _, s := range steps {
		dot := lipgloss.NewStyle().Foreground(s.colour).Bold(true).Render(string(s.state))
		lbl := lipgloss.NewStyle().Foreground(s.colour).Render(s.label)
		parts = append(parts, dot+" "+lbl)
	}
	return strings.Join(parts, "    ")
}

func (m *Model) renderLog() string {
	var lines []string
	stamp := func(t time.Time) string {
		return theme.Subtle.Render(t.Format("15:04:05"))
	}
	tag := func(level string, c lipgloss.Color) string {
		return lipgloss.NewStyle().Foreground(c).Width(5).Render(level)
	}
	add := func(t time.Time, level string, lc lipgloss.Color, msg string) {
		lines = append(lines, lipgloss.JoinHorizontal(lipgloss.Top,
			stamp(t), "  ", tag(level, lc), " ", msg,
		))
	}

	add(m.started, "info", theme.Cyan, "initiating connection to "+m.configName)
	if m.sessionPath != "" {
		add(m.started.Add(50*time.Millisecond), "dbus", theme.FgSubtle,
			theme.AccentCyan.Render(m.sessionPath))
	}
	if m.status.Message != "" {
		add(time.Now(), "info", theme.Cyan, m.status.Message)
	}
	if m.connectErr != nil {
		add(time.Now(), "err", theme.Red, m.connectErr.Error())
	} else if m.status.IsConnected() {
		add(time.Now(), "info", theme.Mint, "tunnel up — switching to status view")
	} else {
		add(time.Now(), "...", theme.FgSubtle, "waiting for openvpn3 to finish handshake")
	}
	return strings.Join(lines, "\n")
}

func shortenSession(path string) string {
	// "/net/openvpn/v3/sessions/<hash>" → first 8 of hash; fallback to last segment.
	i := strings.LastIndex(path, "/")
	if i < 0 || i+1 >= len(path) {
		return path
	}
	tail := path[i+1:]
	if len(tail) > 8 {
		tail = tail[:8]
	}
	return "session " + tail
}
