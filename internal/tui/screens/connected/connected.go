// Package connected renders the live-tunnel screen: header pills with
// IP/throughput/uptime, a stat-card row, throughput sparklines from the
// Sampler, and tunnel + D-Bus session info boxes.
package connected

import (
	"context"
	"fmt"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/esivres/openvpn3ui/internal/app"
	"github.com/esivres/openvpn3ui/internal/ovpn"
	"github.com/esivres/openvpn3ui/internal/probe"
	"github.com/esivres/openvpn3ui/internal/tui/components"
	"github.com/esivres/openvpn3ui/internal/tui/theme"
)

// DisconnectedMsg signals root that the session went away (user-driven or
// remote-driven). Root drops back to the list.
type DisconnectedMsg struct{}

// BackMsg signals root the user pressed "q" to hide the screen — list returns.
type BackMsg struct{}

// OpenEditMsg asks root to switch to the edit screen for the underlying
// config of the live session. Tunnel keeps running — edit only touches
// overlay metadata and config overrides, none of which interrupts an
// already-established connection.
type OpenEditMsg struct {
	ConfigPath string
	ConfigName string
}

type tickMsg struct{}

type Model struct {
	svc         *app.Service
	width       int
	height      int
	sessionPath string

	session ovpn.Session
	loadErr error

	// log is a ring buffer of openvpn3 Log signals for this session.
	// On Connected the cap is higher than on Connecting because there's
	// room for it on a stable-state screen; once full, oldest lines
	// scroll off the top.
	log []components.LogEntry

	// probe holds the most recent successful HTTPS probe result —
	// public IP, country, RTT. probeErr captures the most recent
	// failure so the user sees a dim "probe down" hint when the
	// tunnel stops carrying traffic even though openvpn3 still
	// reports CONNECTED (split-tunnel breakage, DNS hijack, etc).
	probe    probe.Result
	probeErr error
}

const logRingCap = 12

// probeMsg is the typed event the probe goroutine sends back. err is
// preserved so the UI can render a dim "—" when the tunnel goes dark
// without surfacing the raw stack to the user.
type probeMsg struct {
	r   probe.Result
	err error
}

// probeInterval is how often we re-run the Cloudflare trace probe. 15s
// is short enough to feel "live" without making the connected screen
// noisy in tcpdump.
const probeInterval = 15 * time.Second

func New(svc *app.Service, sessionPath string) *Model {
	return &Model{svc: svc, sessionPath: sessionPath}
}

func (m *Model) Init() tea.Cmd { return tea.Batch(tick(), runProbe()) }

// runProbe fires one Cloudflare trace request in a separate goroutine.
// Bubble Tea handles the goroutine plumbing — we just return a Cmd; the
// emitted probeMsg flows back through Update which reschedules the next
// probe via tea.Tick.
func runProbe() tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		r, err := probe.Run(ctx)
		return probeMsg{r: r, err: err}
	}
}

// scheduleProbe re-fires the probe after probeInterval. Split from
// runProbe so the cmd graph is obvious: tick → probe, probe → tick.
func scheduleProbe() tea.Cmd {
	return tea.Tick(probeInterval, func(time.Time) tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		r, err := probe.Run(ctx)
		return probeMsg{r: r, err: err}
	})
}

// HelpKeys feeds the `?` overlay.
func (m *Model) HelpKeys() []components.KeyHelp {
	return []components.KeyHelp{
		{Key: "d", Label: "disconnect this session"},
		{Key: "e", Label: "edit profile (tunnel stays up)"},
		{Key: "q / esc", Label: "hide (tunnel stays up)"},
	}
}

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
		case "e":
			// Jump to edit for the underlying profile. Tunnel keeps
			// running while the user changes overlay/overrides — those
			// only take effect on the next Connect, so live state is
			// untouched.
			cp := m.session.ConfigPath
			cn := m.session.ConfigName
			if cp == "" {
				return m, nil
			}
			return m, func() tea.Msg {
				return OpenEditMsg{ConfigPath: cp, ConfigName: cn}
			}
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

	case probeMsg:
		if msg.err != nil {
			m.probeErr = msg.err
		} else {
			m.probe = msg.r
			m.probeErr = nil
		}
		return m, scheduleProbe()

	case ovpn.SessionLogEvent:
		if msg.Path != m.sessionPath {
			return m, nil
		}
		m.log = append(m.log, components.LogEntry{
			At: time.Now(), Level: msg.Level, Message: strings.TrimSpace(msg.Message),
		})
		if len(m.log) > logRingCap {
			m.log = m.log[len(m.log)-logRingCap:]
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
	// Index-based — `ovpn.Session` is ~128 bytes and ranging by value
	// copies the whole struct each iteration. Loop only reads `Path`,
	// so taking the address sidesteps the copy without changing
	// semantics.
	for i := range all {
		if all[i].Path == m.sessionPath {
			return all[i], nil
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
	// Probe pill goes last so when the tunnel is up but external
	// reachability fails (split-tunnel exclude, DNS broken), the warning
	// sits next to the throughput pills where the user is already
	// looking — not buried in the tunnel-info box.
	if m.probe.IP != "" {
		label := "⊕ " + m.probe.IP
		if m.probe.Country != "" {
			label += " · " + m.probe.Country
		}
		pills = append(pills, components.Pill(label, theme.Mint, theme.Panel2))
	} else if m.probeErr != nil {
		pills = append(pills, components.Pill("⊕ probe down", theme.Peach, theme.Panel2))
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

	logBox := components.Box{
		Title:       theme.AccentMint.Render("▤ ") + "session log " + theme.Dim.Render("· live"),
		Content:     m.renderLog(),
		Width:       m.width - 4,
		BorderColor: theme.Border,
	}.Render()

	help := components.HelpBar([]components.KeyHelp{
		{Key: "d", Label: "disconnect"},
		{Key: "e", Label: "edit"},
		{Key: "q", Label: "hide"},
	}, m.width)

	return lipgloss.JoinVertical(lipgloss.Left,
		header, "", statBox, "", throughputBox, "", bottom, "", logBox, "", help,
	)
}

func (m *Model) renderLog() string {
	if len(m.log) == 0 {
		return theme.Dim.Render("no log lines yet — openvpn3 emits Log signals on state changes and verbose handshakes")
	}
	rows := make([]string, 0, len(m.log))
	for i := range m.log {
		rows = append(rows, components.RenderLogLine(m.log[i]))
	}
	return strings.Join(rows, "\n")
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
		kv("public ip", m.publicIPLine()),
		kv("rtt", m.rttLine()),
	}
	_ = stats
	return strings.Join(rows, "\n")
}

// publicIPLine renders the IP cell on the tunnel info box. Returns a
// dim "—" during the first ~5s after the screen opens (probe in
// flight), and a peach hint when the most recent probe failed —
// distinguishing "haven't measured yet" from "tunnel doesn't carry
// outside traffic" is worth the extra branch.
func (m *Model) publicIPLine() string {
	if m.probe.IP != "" {
		ip := theme.AccentMint.Render(m.probe.IP)
		if m.probe.Country != "" {
			return ip + theme.Dim.Render("  · "+m.probe.Country)
		}
		return ip
	}
	if m.probeErr != nil {
		return theme.AccentPeach.Render("unreachable")
	}
	return theme.Dim.Render("probing…")
}

func (m *Model) rttLine() string {
	if m.probe.IP == "" {
		if m.probeErr != nil {
			return theme.Dim.Render("—")
		}
		return theme.Dim.Render("…")
	}
	rtt := m.probe.RTT
	switch {
	case rtt >= 200*time.Millisecond:
		return theme.AccentPeach.Render(fmt.Sprintf("%d ms", rtt.Milliseconds()))
	case rtt >= 80*time.Millisecond:
		return theme.AccentCyan.Render(fmt.Sprintf("%d ms", rtt.Milliseconds()))
	default:
		return theme.AccentMint.Render(fmt.Sprintf("%d ms", rtt.Milliseconds()))
	}
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
