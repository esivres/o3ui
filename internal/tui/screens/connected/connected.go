// Package connected renders the live-tunnel screen: header pills with
// IP/throughput/uptime, a stat-card row, throughput sparklines from the
// Sampler, and tunnel + D-Bus session info boxes.
package connected

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

// DisconnectedMsg signals root that the session went away (user-driven or
// remote-driven). Root drops back to the list.
type DisconnectedMsg struct{}

// BackMsg signals root the user pressed "q" to hide the screen — list returns.
type BackMsg struct{}

type tickMsg struct{}

type Model struct {
	svc         *app.Service
	width       int
	height      int
	sessionPath string

	session ovpn.Session
	loadErr error
}

func New(svc *app.Service, sessionPath string) *Model {
	return &Model{svc: svc, sessionPath: sessionPath}
}

func (m *Model) Init() tea.Cmd { return tick() }

func (m *Model) SetSize(w, h int) { m.width, m.height = w, h }

func tick() tea.Cmd {
	return tea.Tick(time.Second, func(time.Time) tea.Msg { return tickMsg{} })
}

func (m *Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.SetSize(msg.Width, msg.Height)

	case tea.KeyMsg:
		switch msg.String() {
		case "d":
			err := m.svc.Disconnect(m.sessionPath)
			if err != nil {
				m.loadErr = err
				return m, nil
			}
			return m, func() tea.Msg { return DisconnectedMsg{} }
		case "q", "esc":
			return m, func() tea.Msg { return BackMsg{} }
		}

	case tickMsg:
		s, err := m.fetchSession()
		if err == nil {
			m.session = s
			if !s.Status.IsActive() {
				return m, func() tea.Msg { return DisconnectedMsg{} }
			}
		}
		return m, tick()

	case ovpn.StatusChangeEvent:
		if msg.Path != m.sessionPath {
			return m, nil
		}
		m.session.Status = msg.Status
		if !msg.Status.IsActive() {
			return m, func() tea.Msg { return DisconnectedMsg{} }
		}

	case ovpn.SessionDestroyedEvent:
		// openvpn3 may evict the session shortly after Disconnect;
		// either way it means we should bounce the user back.
		if msg.Path == m.sessionPath {
			return m, func() tea.Msg { return DisconnectedMsg{} }
		}
	}
	return m, nil
}

func (m *Model) fetchSession() (ovpn.Session, error) {
	all, err := m.svc.ListSessions()
	if err != nil {
		return ovpn.Session{}, err
	}
	for _, s := range all {
		if s.Path == m.sessionPath {
			return s, nil
		}
	}
	return ovpn.Session{}, fmt.Errorf("session not found")
}

func (m *Model) View() string {
	if m.width == 0 {
		return "loading…"
	}

	uptime := time.Duration(0)
	if !m.session.CreatedAt.IsZero() {
		uptime = time.Since(m.session.CreatedAt)
	}

	hist := m.svc.ThroughputHistory(m.sessionPath)
	curIn, curOut := currentRates(hist)

	pills := []string{
		components.Pill("● "+m.session.DeviceName, theme.Mint, theme.MintDp),
		components.Pill("↓ "+humanRate(curIn), theme.Cyan, theme.Panel2),
		components.Pill("↑ "+humanRate(curOut), theme.Peach, theme.Panel2),
		components.Pill(uptimeStr(uptime), theme.FgDim, theme.Panel2),
	}
	header := components.HeaderBar("ovpn3", "connected", pills, m.width)

	statBox := components.Box{
		Title: theme.AccentMint.Render("◆ ") + m.session.SessionName + "  " +
			components.Pill("CONNECTED", theme.Mint, theme.MintDp),
		Content:     m.renderStats(curIn, curOut),
		Width:       m.width - 4,
		BorderColor: theme.Mint,
		Glow:        true,
	}.Render()

	throughputBox := components.Box{
		Title:       theme.AccentPink.Render("▎") + " throughput  " + theme.Dim.Render("· last 60s"),
		Content:     m.renderSparklines(hist),
		Width:       m.width - 4,
		BorderColor: theme.Border,
	}.Render()

	leftW := (m.width - 6) / 2
	rightW := m.width - 4 - leftW - 2
	tunnelBox := components.Box{
		Title:       theme.AccentPurple.Render("⚙ ") + "tunnel",
		Content:     m.renderTunnel(),
		Width:       leftW,
		BorderColor: theme.Border,
	}.Render()
	dbusBox := components.Box{
		Title:       theme.AccentCyan.Render("🔌 ") + "dbus session",
		Content:     m.renderDBus(),
		Width:       rightW,
		BorderColor: theme.Border,
	}.Render()
	bottom := lipgloss.JoinHorizontal(lipgloss.Top, tunnelBox, "  ", dbusBox)

	help := components.HelpBar([]components.KeyHelp{
		{Key: "d", Label: "disconnect"},
		{Key: "q", Label: "hide"},
	}, m.width)

	return lipgloss.JoinVertical(lipgloss.Left,
		header, "", statBox, "", throughputBox, "", bottom, "", help,
	)
}

func (m *Model) renderStats(curIn, curOut int64) string {
	cell := func(label, value, sub string, accent lipgloss.Color) string {
		l := lipgloss.NewStyle().Foreground(theme.FgSubtle).Render(strings.ToUpper(label))
		v := lipgloss.NewStyle().Foreground(accent).Bold(true).Render(value)
		s := theme.Dim.Render(sub)
		return l + "\n" + v + "\n" + s
	}
	tunIP := m.session.DeviceName
	row := lipgloss.JoinHorizontal(lipgloss.Top,
		cellWidth(cell("tunnel", or(tunIP, "—"), "device", theme.Cyan), m.width/4-2), "  ",
		cellWidth(cell("session", shortName(m.session.SessionName), "config", theme.Purple), m.width/4-2), "  ",
		cellWidth(cell("down", humanRate(curIn), "current", theme.Cyan), m.width/4-2), "  ",
		cellWidth(cell("up", humanRate(curOut), "current", theme.Peach), m.width/4-2),
	)
	return row
}

func cellWidth(s string, w int) string {
	if w < 4 {
		w = 4
	}
	return lipgloss.NewStyle().Width(w).Render(s)
}

func (m *Model) renderTunnel() string {
	stats := map[string]int64{}
	if c, err := m.svc.SessionLogLevel(m.sessionPath); err == nil {
		_ = c
	}
	rows := []string{
		kv("device", or(m.session.DeviceName, "—")),
		kv("session", or(shortName(m.session.SessionName), "—")),
		kv("config", or(shortName(m.session.ConfigName), "—")),
		kv("status", statusPill(m.session.Status)),
		kv("created", or(m.session.CreatedAt.Format("15:04:05"), "—")),
	}
	_ = stats
	return strings.Join(rows, "\n")
}

func (m *Model) renderDBus() string {
	rows := []string{
		theme.AccentCyan.Render(m.sessionPath),
		"",
		kv("config", theme.Subtle.Render(shortPath(m.session.ConfigPath))),
	}
	return strings.Join(rows, "\n")
}

func (m *Model) renderSparklines(hist []app.Sample) string {
	if len(hist) < 2 {
		return theme.Dim.Render("collecting samples…")
	}
	width := m.width - 8
	if width < 20 {
		width = 20
	}
	in := makeSpark(hist, true, width)
	out := makeSpark(hist, false, width)
	maxIn, maxOut := peakRates(hist)
	legend := lipgloss.JoinHorizontal(lipgloss.Top,
		theme.AccentCyan.Render("━━ down"), "   ",
		theme.AccentPeach.Render("━━ up"), "   ",
		theme.Dim.Render(fmt.Sprintf("peak ↓ %s · ↑ %s", humanRate(maxIn), humanRate(maxOut))),
	)
	timeline := lipgloss.JoinHorizontal(lipgloss.Top,
		theme.Subtle.Render("−60s"), strings.Repeat(" ", width/4),
		theme.Subtle.Render("−45s"), strings.Repeat(" ", width/4),
		theme.Subtle.Render("−30s"), strings.Repeat(" ", width/4),
		theme.Subtle.Render("−15s"), strings.Repeat(" ", width/8),
		theme.Bright.Render("now"),
	)
	return strings.Join([]string{
		legend,
		theme.AccentCyan.Render(in),
		theme.AccentPeach.Render(out),
		timeline,
	}, "\n")
}

// makeSpark renders the throughput history as a row of block characters.
// `forIn` selects the BytesIn vs BytesOut series. The output is exactly
// `width` cells wide; older samples are dropped from the left if the
// history exceeds the rendered width.
func makeSpark(hist []app.Sample, forIn bool, width int) string {
	values := make([]int64, len(hist))
	for i, s := range hist {
		if forIn {
			values[i] = s.DeltaIn
		} else {
			values[i] = s.DeltaOut
		}
	}
	if len(values) > width {
		values = values[len(values)-width:]
	}
	var max int64
	for _, v := range values {
		if v > max {
			max = v
		}
	}
	blocks := []rune("▁▂▃▄▅▆▇█")
	var b strings.Builder
	for _, v := range values {
		if max == 0 {
			b.WriteRune(blocks[0])
			continue
		}
		idx := int(v * 7 / max)
		if idx > 7 {
			idx = 7
		}
		b.WriteRune(blocks[idx])
	}
	// Pad on the left so the most-recent sample stays right-aligned.
	if pad := width - lipgloss.Width(b.String()); pad > 0 {
		return strings.Repeat(" ", pad) + b.String()
	}
	return b.String()
}

func currentRates(hist []app.Sample) (int64, int64) {
	if len(hist) == 0 {
		return 0, 0
	}
	last := hist[len(hist)-1]
	return last.DeltaIn, last.DeltaOut
}

func peakRates(hist []app.Sample) (int64, int64) {
	var inMax, outMax int64
	for _, s := range hist {
		if s.DeltaIn > inMax {
			inMax = s.DeltaIn
		}
		if s.DeltaOut > outMax {
			outMax = s.DeltaOut
		}
	}
	return inMax, outMax
}

func humanRate(bps int64) string {
	switch {
	case bps >= 1<<30:
		return fmt.Sprintf("%.1f GB/s", float64(bps)/(1<<30))
	case bps >= 1<<20:
		return fmt.Sprintf("%.1f MB/s", float64(bps)/(1<<20))
	case bps >= 1<<10:
		return fmt.Sprintf("%.1f KB/s", float64(bps)/(1<<10))
	default:
		return fmt.Sprintf("%d B/s", bps)
	}
}

func uptimeStr(d time.Duration) string {
	if d <= 0 {
		return "00:00:00"
	}
	h := int(d.Hours())
	min := int(d.Minutes()) % 60
	sec := int(d.Seconds()) % 60
	return fmt.Sprintf("%02d:%02d:%02d", h, min, sec)
}

// statusPill renders the colored connection-state badge. Foreground is
// always theme.Bg so the bold text reads on the accent fill — this was
// the inconsistency the design pass flagged: CONNECTING was rendering
// peach-on-pinksoft (~3:1 contrast). All three states now use the same
// dark-text-on-bright-fill recipe.
func statusPill(s ovpn.Status) string {
	switch {
	case s.IsConnected():
		return components.Pill("CONNECTED", theme.Bg, theme.Mint)
	case s.IsActive():
		return components.Pill("CONNECTING", theme.Bg, theme.Peach)
	default:
		return components.Pill("DISCONNECTED", theme.Bg, theme.FgDim)
	}
}

func kv(k, v string) string {
	return theme.Dim.Width(10).Render(k) + " " + v
}

func or(s, fallback string) string {
	if s == "" {
		return fallback
	}
	return s
}

func shortName(s string) string {
	if len(s) <= 28 {
		return s
	}
	return s[:25] + "…"
}

func shortPath(p string) string {
	i := strings.LastIndex(p, "/")
	if i < 0 {
		return p
	}
	prefix := "…/"
	tail := p[i+1:]
	return prefix + tail
}
