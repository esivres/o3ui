// Package settings renders the "D-Bus & backend" tab from the design:
// a table of openvpn3 services discovered on the bus, a 1..6 verbosity
// picker for new sessions, and a read-only list of the well-known bus
// paths the app talks to.
package settings

import (
	"fmt"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/esivres/openvpn3ui/internal/app"
	"github.com/esivres/openvpn3ui/internal/ovpn"
	"github.com/esivres/openvpn3ui/internal/tui/components"
	"github.com/esivres/openvpn3ui/internal/tui/theme"
)

// BackMsg signals the root the user is done with this screen.
type BackMsg struct{}

type tickMsg struct{}

type Model struct {
	svc    *app.Service
	width  int
	height int

	services []ovpn.BackendService
	loadErr  error
	flash    string
}

func New(svc *app.Service) *Model { return &Model{svc: svc} }

// HelpKeys feeds the `?` overlay.
func (m *Model) HelpKeys() []components.KeyHelp {
	return []components.KeyHelp{
		{Key: "1–6", Label: "log verbosity for new sessions"},
		{Key: "r", Label: "refresh backend services"},
		{Key: "q / esc", Label: "back"},
	}
}

func (m *Model) Init() tea.Cmd { return tea.Batch(m.refresh(), tick()) }

func (m *Model) SetSize(w, h int) { m.width, m.height = w, h }

func tick() tea.Cmd {
	return tea.Tick(2*time.Second, func(time.Time) tea.Msg { return tickMsg{} })
}

type loadedMsg struct {
	services []ovpn.BackendService
	err      error
}

func (m *Model) refresh() tea.Cmd {
	svc := m.svc
	return func() tea.Msg {
		services, err := svc.BackendServices()
		return loadedMsg{services: services, err: err}
	}
}

func (m *Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.SetSize(msg.Width, msg.Height)

	case loadedMsg:
		m.services = msg.services
		m.loadErr = msg.err

	case tickMsg:
		return m, tea.Batch(m.refresh(), tick())

	case tea.KeyMsg:
		switch msg.String() {
		case "esc", "q":
			return m, func() tea.Msg { return BackMsg{} }
		case "1", "2", "3", "4", "5", "6":
			level := app.LogLevel(int(msg.String()[0]) - '0')
			if err := m.svc.SetPreferredLogLevel(level); err != nil {
				m.flash = "✗ " + err.Error()
			} else {
				m.flash = fmt.Sprintf("✓ default verbosity → %d (%s)", level, levelName(level))
			}
		case "r":
			return m, m.refresh()
		}
	}
	return m, nil
}

func (m *Model) View() string {
	if m.width == 0 {
		return "loading…"
	}
	header := components.HeaderBar(
		"ovpn3", "settings",
		[]string{components.Pill("● net.openvpn.v3", theme.Mint, theme.MintDp)},
		m.width,
	)

	body := lipgloss.JoinHorizontal(lipgloss.Top,
		// Decorative 5-tab sidebar removed (only 'dbus & backend'
		// was real). When connection/network/about land we'll bring
		// it back with working entries. Full width for now.
		lipgloss.NewStyle().Width(m.width-4).Render(m.renderRight()),
	)
	help := components.HelpBar([]components.KeyHelp{
		{Key: "1-6", Label: "log level"},
		{Key: "r", Label: "refresh"},
		{Key: "q/esc", Label: "back"},
	}, m.width)
	return lipgloss.JoinVertical(lipgloss.Left, header, "", body, "", help)
}

func (m *Model) renderRight() string {
	parts := []string{
		m.renderServices(),
		"",
		m.renderVerbosity(),
		"",
		m.renderBusPaths(),
	}
	if m.flash != "" {
		parts = append(parts, "", theme.Dim.Render(m.flash))
	}
	return strings.Join(parts, "\n")
}

func (m *Model) renderServices() string {
	if m.loadErr != nil {
		return components.Box{
			Title:   theme.AccentCyan.Render("🔌 ") + "backend services",
			Content: theme.AccentRed.Render("error: " + m.loadErr.Error()),
			Width:   m.width - 28,
		}.Render()
	}
	if len(m.services) == 0 {
		return components.Box{
			Title:   theme.AccentCyan.Render("🔌 ") + "backend services",
			Content: theme.Dim.Render("no openvpn3 services discovered"),
			Width:   m.width - 28,
		}.Render()
	}

	headerStyle := lipgloss.NewStyle().Foreground(theme.FgSubtle).Bold(true)
	col := func(s string, w int) string {
		return lipgloss.NewStyle().Width(w).Render(s)
	}

	rows := []string{
		lipgloss.JoinHorizontal(lipgloss.Top,
			col(headerStyle.Render("SERVICE"), 36),
			col(headerStyle.Render("STATE"), 14),
			col(headerStyle.Render("PID"), 8),
			headerStyle.Render("UPTIME"),
		),
	}
	for _, s := range m.services {
		state := components.Pill("● running", theme.Mint, theme.MintDp)
		if s.State != "running" {
			state = components.Pill("○ "+s.State, theme.FgDim, theme.Panel2)
		}
		pidStr := "—"
		if s.PID > 0 {
			pidStr = fmt.Sprintf("%d", s.PID)
		}
		up := "—"
		if d := s.Uptime(); d > 0 {
			up = formatDuration(d)
		}
		rows = append(rows, lipgloss.JoinHorizontal(lipgloss.Top,
			col(theme.AccentCyan.Render(s.Name), 36),
			col(state, 14),
			col(theme.AccentPeach.Render(pidStr), 8),
			theme.Dim.Render(up),
		))
	}
	return components.Box{
		Title:   theme.AccentCyan.Render("🔌 ") + "backend services",
		Content: strings.Join(rows, "\n"),
		Width:   m.width - 28,
	}.Render()
}

func (m *Model) renderVerbosity() string {
	cur := m.svc.PreferredLogLevel()
	pills := []string{theme.Dim.Render("default verbosity for new sessions") + "  "}
	for _, l := range []app.LogLevel{
		app.LogFatal, app.LogError, app.LogWarning,
		app.LogInfo, app.LogDebug, app.LogVerbose,
	} {
		label := fmt.Sprintf("%d·%s", l, levelName(l))
		if l == cur {
			pills = append(pills, components.Pill(label, theme.FgBright, theme.Pink))
		} else {
			pills = append(pills, components.Pill(label, theme.FgDim, theme.Panel2))
		}
	}
	return components.Box{
		Title:   theme.AccentPurple.Render("▤ ") + "log verbosity",
		Content: strings.Join(pills, " ") + "\n\n" + theme.Subtle.Render("press 1..6 to change"),
		Width:   m.width - 28,
	}.Render()
}

func (m *Model) renderBusPaths() string {
	paths := []string{
		"/net/openvpn/v3/configuration",
		"/net/openvpn/v3/sessions",
		"/net/openvpn/v3/netcfg",
		"/net/openvpn/v3/log",
		"/net/openvpn/v3/backends",
	}
	var rows []string
	for _, p := range paths {
		rows = append(rows, theme.AccentCyan.Render(p))
	}
	return components.Box{
		Title:   theme.AccentMint.Render("📂 ") + "bus paths " + theme.Dim.Render("· read-only"),
		Content: strings.Join(rows, "\n"),
		Width:   m.width - 28,
	}.Render()
}

func levelName(l app.LogLevel) string {
	switch l {
	case app.LogFatal:
		return "fatal"
	case app.LogError:
		return "err"
	case app.LogWarning:
		return "warn"
	case app.LogInfo:
		return "info"
	case app.LogDebug:
		return "dbg"
	case app.LogVerbose:
		return "verbose"
	}
	return "?"
}

func formatDuration(d time.Duration) string {
	switch {
	case d >= 24*time.Hour:
		days := int(d / (24 * time.Hour))
		hours := int((d % (24 * time.Hour)) / time.Hour)
		return fmt.Sprintf("%dd %02dh", days, hours)
	case d >= time.Hour:
		return fmt.Sprintf("%dh %02dm", int(d/time.Hour), int(d%time.Hour/time.Minute))
	case d >= time.Minute:
		return fmt.Sprintf("%dm %02ds", int(d/time.Minute), int(d%time.Minute/time.Second))
	default:
		return fmt.Sprintf("%ds", int(d/time.Second))
	}
}
