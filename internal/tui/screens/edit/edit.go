// Package edit renders the per-config editor — three tabs:
//
//	1 general         profile name (read-only), country flag, favorite /
//	                  auto-connect toggles, parsed .ovpn summary
//	2 authentication  stored username + password (keyring), TOTP card
//	                  with live code preview and import-screen entry
//	3 raw .ovpn       read-only viewport over the file openvpn3 stored
//	                  when the profile was imported; useful for debug
//
// `network` and `advanced` from the original mock are deliberately not
// added until we have real per-config knobs from openvpn3 to bind them
// to. Decorative tabs were already shown to undermine trust in Sprint 1.
package edit

import (
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/textinput"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/esivres/openvpn3ui/internal/app"
	"github.com/esivres/openvpn3ui/internal/otp"
	"github.com/esivres/openvpn3ui/internal/ovpnconf"
	"github.com/esivres/openvpn3ui/internal/tui/components"
	"github.com/esivres/openvpn3ui/internal/tui/theme"
)

// BackMsg signals the root that the user is done with this screen.
type BackMsg struct{}

// OpenOTPImportMsg asks the root to switch to the dedicated TOTP import
// screen for the current config. Edit fires this from the "i" shortcut
// when in view mode — heavy import workflows belong on a focused screen,
// not crammed into the auth panel.
type OpenOTPImportMsg struct {
	ConfigPath string
	ConfigName string
}

type tickMsg struct{}

// mode is the small state machine inside the edit screen — view is the
// landing state; the rest are inline forms or pickers.
type mode int

const (
	modeView mode = iota
	modeEnterUsername
	modeEnterPassword
	modeEnterCountry
	modeRemoveOTPConfirm
)

// The OTP-specific inline modes (paste URI / type base32 / pick QR
// file) used to live here too. They were a parallel implementation
// of the same thing the otpimport screen does on its own, with a
// better filepicker and the migration-URI account picker. Pressing
// `i` is the only OTP-import entry point now.

// tab identifies which tab is currently active.
type tab int

const (
	tabGeneral tab = iota
	tabAuth
	tabRaw
)

type Model struct {
	svc     *app.Service
	width   int
	height  int
	cfgPath string
	cfgName string

	tab   tab
	mode  mode
	input textinput.Model

	// raw-tab viewport. Lazily initialised on first switch to that
	// tab so we don't pay a Fetch() round-trip on every edit-screen
	// open even when the user only edits credentials.
	raw       viewport.Model
	rawLoaded bool
	rawErr    string

	flash    string // ephemeral success message (cleared on next mode change)
	flashErr string
}

func New(svc *app.Service, configPath, configName string) *Model {
	return &Model{
		svc:     svc,
		cfgPath: configPath,
		cfgName: configName,
		tab:     tabGeneral,
		raw:     viewport.New(0, 0),
	}
}

func (m *Model) Init() tea.Cmd { return tick() }

// HelpKeys feeds the `?` overlay. Mirrors the helpbar at the bottom
// of the screen but stays exhaustive — overlay is for discovery,
// footer for muscle memory. Tab-specific keys are tagged to make the
// overlay self-explanatory.
func (m *Model) HelpKeys() []components.KeyHelp {
	return []components.KeyHelp{
		{Key: "tab / 1-3", Label: "switch tab (general / auth / raw)"},
		{Key: "f", Label: "[general] toggle favorite"},
		{Key: "a", Label: "[general] toggle auto-connect"},
		{Key: "c", Label: "[general] edit country code"},
		{Key: "u", Label: "[auth] edit / set username"},
		{Key: "p", Label: "[auth] edit / set password"},
		{Key: "d", Label: "[auth] clear saved credentials"},
		{Key: "i", Label: "[auth] open OTP import screen"},
		{Key: "x", Label: "[auth] remove the TOTP secret"},
		{Key: "↑↓ pgup pgdn", Label: "[raw] scroll"},
		{Key: "q / esc", Label: "back to the profile list"},
	}
}

func (m *Model) SetSize(w, h int) {
	m.width, m.height = w, h
	// raw viewport fits the available content area: total height
	// minus header (3) - tabbar (2) - footer (2) - chrome (a couple
	// extra for the box border + breathing room).
	vh := h - 10
	if vh < 6 {
		vh = 6
	}
	m.raw.Width = w - 4
	m.raw.Height = vh
}

func tick() tea.Cmd {
	return tea.Tick(time.Second, func(time.Time) tea.Msg { return tickMsg{} })
}

func (m *Model) newInput(placeholder string, hidden bool) textinput.Model {
	ti := textinput.New()
	ti.CharLimit = 4096
	ti.Width = 60
	ti.Prompt = ""
	ti.Placeholder = placeholder
	if hidden {
		ti.EchoMode = textinput.EchoPassword
		ti.EchoCharacter = '•'
	}
	ti.Focus()
	return ti
}

func (m *Model) enterMode(next mode) {
	m.mode = next
	m.flash = ""
	m.flashErr = ""
	switch next {
	case modeEnterUsername:
		ti := m.newInput("vpn login", false)
		if user, _, _ := m.svc.GetCredentials(m.cfgPath); user != "" {
			ti.SetValue(user)
		}
		m.input = ti
	case modeEnterPassword:
		m.input = m.newInput("vpn password", true)
	case modeEnterCountry:
		ti := m.newInput("two-letter code, e.g. DE", false)
		ti.CharLimit = 4
		ti.Width = 8
		if o, ok := m.svc.GetOverlay(m.cfgPath); ok && o.CountryCode != "" {
			ti.SetValue(o.CountryCode)
		}
		m.input = ti
	}
}

// ensureRawLoaded fetches the .ovpn body once per screen lifetime and
// wires it into the viewport. We don't preload because most edit-screen
// visits don't open the raw tab.
func (m *Model) ensureRawLoaded() {
	if m.rawLoaded {
		return
	}
	m.rawLoaded = true
	body, err := m.svc.FetchConfig(m.cfgPath)
	if err != nil {
		m.rawErr = err.Error()
		return
	}
	// Colourise inline blocks dimly so the eye lands on the human-
	// readable directives. Pure cosmetic — no syntax tree.
	lines := strings.Split(body, "\n")
	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		switch {
		case strings.HasPrefix(trimmed, "#"), strings.HasPrefix(trimmed, ";"):
			lines[i] = theme.Subtle.Render(line)
		case strings.HasPrefix(trimmed, "<"), strings.HasPrefix(trimmed, "-----"):
			lines[i] = theme.Dim.Render(line)
		}
	}
	m.raw.SetContent(strings.Join(lines, "\n"))
}

func (m *Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.SetSize(msg.Width, msg.Height)
		return m, nil

	case tickMsg:
		// Re-render so the TOTP code refreshes every second.
		return m, tick()

	case tea.KeyMsg:
		return m.handleKey(msg)
	}

	if m.mode != modeView && m.mode != modeRemoveOTPConfirm {
		var cmd tea.Cmd
		m.input, cmd = m.input.Update(msg)
		return m, cmd
	}
	return m, nil
}

func (m *Model) handleKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	key := msg.String()

	// Modal modes — Esc returns to view; Enter is the mode-specific commit.
	if m.mode != modeView {
		switch key {
		case "esc":
			m.enterMode(modeView)
			return m, nil
		case "enter":
			return m.commit()
		}

		if m.mode == modeRemoveOTPConfirm {
			return m, nil
		}

		var cmd tea.Cmd
		m.input, cmd = m.input.Update(msg)
		return m, cmd
	}

	// Tab switching — applies across all tabs, before any tab-local
	// dispatch. Number keys 1-3 jump directly; tab/shift+tab cycle.
	switch key {
	case "tab":
		m.tab = (m.tab + 1) % 3
		if m.tab == tabRaw {
			m.ensureRawLoaded()
		}
		return m, nil
	case "shift+tab":
		m.tab = (m.tab + 2) % 3
		if m.tab == tabRaw {
			m.ensureRawLoaded()
		}
		return m, nil
	case "1":
		m.tab = tabGeneral
		return m, nil
	case "2":
		m.tab = tabAuth
		return m, nil
	case "3":
		m.tab = tabRaw
		m.ensureRawLoaded()
		return m, nil
	}

	// Global view-mode shortcuts.
	switch key {
	case "esc", "q":
		return m, func() tea.Msg { return BackMsg{} }
	}

	// Tab-specific dispatch.
	switch m.tab {
	case tabGeneral:
		return m.handleGeneralKey(key)
	case tabAuth:
		return m.handleAuthKey(key)
	case tabRaw:
		// Viewport handles its own scroll keys; forward the raw msg.
		var cmd tea.Cmd
		m.raw, cmd = m.raw.Update(msg)
		return m, cmd
	}
	return m, nil
}

// handleGeneralKey services the `general` tab keymap: favorite / auto
// toggles and the country-code editor.
func (m *Model) handleGeneralKey(key string) (tea.Model, tea.Cmd) {
	switch key {
	case "f":
		fav := false
		if o, ok := m.svc.GetOverlay(m.cfgPath); ok {
			fav = o.Favorite
		}
		if err := m.svc.SetFavorite(m.cfgPath, !fav); err != nil {
			m.flashErr = err.Error()
		} else if fav {
			m.flash = "favorite cleared"
		} else {
			m.flash = "favorite set"
		}
	case "a":
		auto := false
		if o, ok := m.svc.GetOverlay(m.cfgPath); ok {
			auto = o.AutoConnect
		}
		if err := m.svc.SetAutoConnect(m.cfgPath, !auto); err != nil {
			m.flashErr = err.Error()
		} else if auto {
			m.flash = "auto-connect off"
		} else {
			m.flash = "auto-connect on"
		}
	case "c":
		m.enterMode(modeEnterCountry)
	}
	return m, nil
}

// handleAuthKey is the original edit-screen view-mode dispatch, now
// scoped to the auth tab.
func (m *Model) handleAuthKey(key string) (tea.Model, tea.Cmd) {
	switch key {
	case "u":
		m.enterMode(modeEnterUsername)
	case "p":
		m.enterMode(modeEnterPassword)
	case "i":
		cp, cn := m.cfgPath, m.cfgName
		return m, func() tea.Msg { return OpenOTPImportMsg{ConfigPath: cp, ConfigName: cn} }
	case "x":
		if m.svc.HasOTP(m.cfgPath) {
			m.mode = modeRemoveOTPConfirm
		}
	case "y":
		if m.mode == modeRemoveOTPConfirm {
			if err := m.svc.RemoveOTP(m.cfgPath); err != nil {
				m.flashErr = err.Error()
			} else {
				m.flash = "OTP removed"
			}
			m.mode = modeView
		}
	case "n":
		if m.mode == modeRemoveOTPConfirm {
			m.mode = modeView
		}
	case "d":
		if err := m.svc.ClearCredentials(m.cfgPath); err != nil {
			m.flashErr = err.Error()
		} else {
			m.flash = "credentials cleared"
		}
	}
	return m, nil
}

// commit handles Enter inside a sub-form (username / password /
// country). OTP flows live in the otpimport screen.
func (m *Model) commit() (tea.Model, tea.Cmd) {
	switch m.mode {
	case modeEnterUsername:
		if err := m.svc.RememberUsername(m.cfgPath, strings.TrimSpace(m.input.Value())); err != nil {
			m.flashErr = err.Error()
			return m, nil
		}
		m.flash = "username saved"
		m.mode = modeView
		return m, nil

	case modeEnterPassword:
		if err := m.svc.RememberPassword(m.cfgPath, m.input.Value()); err != nil {
			m.flashErr = err.Error()
			return m, nil
		}
		m.flash = "password saved to keyring"
		m.mode = modeView
		return m, nil

	case modeEnterCountry:
		cc := strings.ToUpper(strings.TrimSpace(m.input.Value()))
		if err := m.svc.SetCountryCode(m.cfgPath, cc); err != nil {
			m.flashErr = err.Error()
			return m, nil
		}
		if cc == "" {
			m.flash = "country cleared"
		} else {
			m.flash = "country set to " + cc
		}
		m.mode = modeView
		return m, nil
	}
	return m, nil
}

func (m *Model) View() string {
	if m.width == 0 {
		return "loading…"
	}
	header := components.HeaderBar(
		"ovpn3", "edit · "+m.cfgName,
		[]string{components.Pill(shortPath(m.cfgPath), theme.FgDim, theme.Panel2)},
		m.width,
	)
	tabbar := m.renderTabBar()
	var body string
	switch m.tab {
	case tabGeneral:
		body = m.renderGeneralTab()
	case tabAuth:
		body = m.renderAuthTab()
	case tabRaw:
		body = m.renderRawTab()
	}
	body = lipgloss.NewStyle().Width(m.width - 4).Render(body)
	if flash := m.renderFlash(); flash != "" {
		body = body + "\n\n" + flash
	}
	help := components.HelpBar(m.helpKeys(), m.width)
	return lipgloss.JoinVertical(lipgloss.Left, header, tabbar, "", body, "", help)
}

// renderTabBar shows 1/2/3 tab pills. Active one uses the accent fill,
// inactive ones the muted panel background — same idiom as the desklet
// tab strip.
func (m *Model) renderTabBar() string {
	tabs := []struct {
		idx   tab
		label string
	}{
		{tabGeneral, "1 general"},
		{tabAuth, "2 authentication"},
		{tabRaw, "3 raw .ovpn"},
	}
	pieces := make([]string, 0, len(tabs))
	for _, t := range tabs {
		fg, bg := theme.FgDim, theme.Panel2
		if t.idx == m.tab {
			fg, bg = theme.Bg, theme.Pink
		}
		pieces = append(pieces, components.Pill(t.label, fg, bg))
	}
	return lipgloss.JoinHorizontal(lipgloss.Top, pieces...)
}

// renderGeneralTab: a row of kv pairs (parsed .ovpn summary + overlay
// flags). All read-only except for `f`/`a`/`c` actions, which mutate
// in place and flash the result.
func (m *Model) renderGeneralTab() string {
	// Inline country editor lives in the same area as the value, like
	// the credential editors do on the auth tab — feels consistent.
	if m.mode == modeEnterCountry {
		return m.renderInputForm("country code (2 letters)",
			"Used for the CC badge on the list. Leave empty to clear.")
	}

	fav, auto, cc := false, false, ""
	if o, ok := m.svc.GetOverlay(m.cfgPath); ok {
		fav, auto, cc = o.Favorite, o.AutoConnect, o.CountryCode
	}

	// Parse .ovpn body lazily for the host / cipher / auth summary.
	// Failures here are non-fatal: the tab still renders the overlay
	// half of the info.
	host := theme.Subtle.Render("—")
	proto := theme.Subtle.Render("—")
	cipher := theme.Subtle.Render("—")
	authStr := theme.Subtle.Render("—")
	if body, err := m.svc.FetchConfig(m.cfgPath); err == nil {
		if prof, perr := ovpnconf.ParseString(body); perr == nil && prof != nil {
			r := prof.PrimaryRemote()
			if r.Host != "" {
				h := theme.AccentCyan.Render(r.Host)
				if r.Port > 0 {
					h += theme.Dim.Render(fmt.Sprintf(":%d", r.Port))
				}
				host = h
			}
			if r.Proto != "" {
				proto = theme.Dim.Render(strings.ToUpper(r.Proto))
			}
			if prof.Cipher != "" {
				cipher = theme.Dim.Render(prof.Cipher)
			}
			if am := prof.AuthMethod(); am != "" {
				authStr = theme.AccentPink.Render(am)
			}
		}
	}

	ccText := theme.Subtle.Render("—")
	if cc != "" {
		ccText = theme.AccentCyan.Render(cc)
	}

	parsed := components.Box{
		Title: theme.AccentCyan.Render("◆ ") + "from .ovpn",
		Content: strings.Join([]string{
			kv("host", host),
			kv("proto", proto),
			kv("cipher", cipher),
			kv("auth", authStr),
		}, "\n"),
		Width:       m.width - 4,
		BorderColor: theme.BorderLt,
	}.Render()

	overlayBox := components.Box{
		Title: theme.AccentPink.Render("◆ ") + "overlay",
		Content: strings.Join([]string{
			kv("name", theme.Bright.Render(m.cfgName)) + "  " + theme.Subtle.Render("[R on list]"),
			kv("country", ccText) + "  " + theme.Subtle.Render("[c edit]"),
			kv("favorite", boolToggle(fav)) + "  " + theme.Subtle.Render("[f toggle]"),
			kv("auto", boolToggle(auto)) + "  " + theme.Subtle.Render("[a toggle]"),
		}, "\n"),
		Width:       m.width - 4,
		BorderColor: theme.Pink,
	}.Render()

	return parsed + "\n" + overlayBox
}

// renderAuthTab — the original credentials + TOTP layout.
func (m *Model) renderAuthTab() string {
	creds := m.renderCreds()
	totp := m.renderTOTP()
	return creds + "\n" + totp
}

// renderRawTab — viewport over the .ovpn body. Errors are surfaced
// as a one-line message instead of an empty box.
func (m *Model) renderRawTab() string {
	if m.rawErr != "" {
		return theme.AccentRed.Render("fetch failed: " + m.rawErr)
	}
	return components.Box{
		Title:       theme.AccentCyan.Render("◆ ") + "raw .ovpn (read-only)",
		Content:     m.raw.View(),
		Width:       m.width - 4,
		BorderColor: theme.BorderLt,
	}.Render()
}

func boolToggle(b bool) string {
	if b {
		return theme.AccentMint.Render("on ✓")
	}
	return theme.Subtle.Render("off")
}

func (m *Model) renderCreds() string {
	user, _, hasPwd := m.svc.GetCredentials(m.cfgPath)
	userText := theme.Subtle.Render("—")
	if user != "" {
		userText = theme.AccentPeach.Render(user)
	}
	pwdText := theme.Subtle.Render("not set")
	if hasPwd {
		pwdText = lipgloss.JoinHorizontal(lipgloss.Top,
			theme.Dim.Render(strings.Repeat("•", 14)), " ",
			components.Pill("keyring", theme.Mint, theme.MintDp),
		)
	}

	var content string
	switch m.mode {
	case modeEnterUsername:
		content = m.renderInputForm("username", "Type your VPN login and press Enter.")
	case modeEnterPassword:
		content = m.renderInputForm("password", "Stored in the system keyring on submit.")
	default:
		content = strings.Join([]string{
			kv("user", userText) + "  " + theme.Subtle.Render("[u edit]"),
			kv("pass", pwdText) + "  " + theme.Subtle.Render("[p edit]  [d clear]"),
		}, "\n")
	}

	return components.Box{
		Title:   theme.AccentCyan.Render("◆ ") + "credentials",
		Content: content,
		Width:   m.width - 28,
	}.Render()
}

func (m *Model) renderTOTP() string {
	var content string
	switch m.mode {
	case modeRemoveOTPConfirm:
		content = strings.Join([]string{
			theme.AccentPeach.Render("Remove OTP secret for ") + theme.Bright.Render(m.cfgName) + theme.AccentPeach.Render("?"),
			"",
			theme.Dim.Render("Press y to confirm, n to keep."),
		}, "\n")
	default:
		content = m.renderTOTPSummary()
	}

	return components.Box{
		Title:       theme.AccentPink.Render("🔑 ") + "two-factor (TOTP)",
		Content:     content,
		Width:       m.width - 28,
		BorderColor: theme.Pink,
		Glow:        true,
	}.Render()
}

func (m *Model) renderTOTPSummary() string {
	if !m.svc.HasOTP(m.cfgPath) {
		return strings.Join([]string{
			theme.Dim.Render("No OTP secret attached to this profile."),
			"",
			theme.AccentPink.Render("[i] open import screen") + theme.Subtle.Render("  · URI / QR file / manual"),
		}, "\n")
	}
	code, _ := m.svc.PreviewOTP(m.cfgPath)
	codePill := lipgloss.NewStyle().
		Background(theme.PinkSoft).
		Foreground(theme.FgBright).
		Bold(true).
		Padding(0, 2).
		Render(prettyCode(code))

	now := time.Now().Unix()
	rem := 30 - int(now%30)
	bar := miniBar(40, (30-rem)*100/30, theme.Purple, theme.Pink)
	countdown := lipgloss.JoinHorizontal(lipgloss.Top,
		theme.Dim.Render("refresh in "), theme.Bright.Render(fmt.Sprintf("%2ds", rem)), "  ", bar,
	)

	actions := []string{
		theme.AccentPink.Render("[i] import screen"),
		theme.AccentRed.Render("[x] remove"),
	}
	return strings.Join([]string{
		codePill,
		countdown,
		"",
		strings.Join(actions, "   "),
	}, "\n")
}

func (m *Model) renderInputForm(label, hint string) string {
	field := lipgloss.NewStyle().
		Background(theme.Surface).
		Foreground(theme.Fg).
		Border(lipgloss.RoundedBorder()).
		BorderForeground(theme.Pink).
		Padding(0, 1).
		Width(m.width - 38).
		Render(m.input.View())

	return strings.Join([]string{
		theme.AccentPink.Render("› ") + theme.Bright.Render(label),
		field,
		theme.Subtle.Render(hint),
		"",
		theme.Dim.Render("enter") + theme.Subtle.Render(" save · ") +
			theme.Dim.Render("esc") + theme.Subtle.Render(" cancel"),
	}, "\n")
}

func (m *Model) renderFlash() string {
	switch {
	case m.flashErr != "":
		return theme.AccentRed.Render("✗ " + m.flashErr)
	case m.flash != "":
		return theme.AccentMint.Render("✓ " + m.flash)
	}
	return ""
}

func (m *Model) helpKeys() []components.KeyHelp {
	switch m.mode {
	case modeView:
		base := []components.KeyHelp{
			{Key: "u", Label: "username"}, {Key: "p", Label: "password"}, {Key: "d", Label: "clear creds"},
			{Key: "i", Label: "import OTP"},
		}
		if m.svc.HasOTP(m.cfgPath) {
			base = append(base, components.KeyHelp{Key: "x", Label: "remove OTP"})
		}
		return append(base, components.KeyHelp{Key: "q/esc", Label: "back"})
	case modeRemoveOTPConfirm:
		return []components.KeyHelp{{Key: "y", Label: "remove"}, {Key: "n", Label: "keep"}}
	default:
		return []components.KeyHelp{{Key: "enter", Label: "save"}, {Key: "esc", Label: "cancel"}}
	}
}

func kv(k, v string) string {
	return theme.Dim.Width(8).Render(k) + " " + v
}

// prettyCode breaks a 6/7/8-digit code into space-separated triplets — easier
// to read at a glance ("738 294" instead of "738294").
func prettyCode(code string) string {
	if len(code) <= 4 {
		return code
	}
	mid := len(code) / 2
	return code[:mid] + " " + code[mid:]
}

func miniBar(width, pct int, from, to lipgloss.Color) string {
	if width < 4 {
		width = 4
	}
	filled := width * pct / 100
	if filled > width {
		filled = width
	}
	bar := lipgloss.NewStyle().Background(from).Width(filled).Render("")
	rest := lipgloss.NewStyle().Background(theme.Panel2).Width(width - filled).Render("")
	return bar + rest + "  " + theme.Subtle.Render(fmt.Sprintf("%d%%", pct))
}

func shortPath(p string) string {
	i := strings.LastIndex(p, "/")
	if i < 0 || i+1 >= len(p) {
		return p
	}
	tail := p[i+1:]
	if len(tail) > 16 {
		tail = tail[:16] + "…"
	}
	return tail
}

// Reference imports kept live so a future refactor doesn't accidentally
// drop them from the import block.
var _ = otp.Now
