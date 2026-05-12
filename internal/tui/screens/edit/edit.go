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
	"errors"
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
	modeEnterServerOverride
	modeEnterPortOverride
	modeEnterProtoOverride
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
	tabNetwork
	tabAdvanced
	tabRaw
)

// tabCount is the number of tabs above; used for tab/shift+tab cycling.
const tabCount = 5

// Override keys we surface on the Network tab. The list is intentionally
// short — these three cover ~all routine reconfiguration without
// touching the .ovpn body. Other override keys (ipv6, dns-fallback, ...)
// can join later as their UX warrants.
const (
	overrideServer = "server-override"
	overridePort   = "port-override"
	overrideProto  = "proto-override"
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
		{Key: "tab / 1-5", Label: "switch tab"},
		{Key: "f", Label: "[general] toggle favorite"},
		{Key: "a", Label: "[general] toggle auto-connect"},
		{Key: "c", Label: "[general] edit country code"},
		{Key: "u", Label: "[auth] edit / set username"},
		{Key: "p", Label: "[auth] edit / set password"},
		{Key: "d", Label: "[auth] clear saved credentials"},
		{Key: "i", Label: "[auth] open OTP import screen"},
		{Key: "x", Label: "[auth] remove the TOTP secret"},
		{Key: "h/p/o", Label: "[network] server / port / proto override"},
		{Key: "k/u/l", Label: "[advanced] dco / public / locked toggle"},
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
	case modeEnterServerOverride:
		ti := m.newInput("host or empty to clear", false)
		ti.SetValue(m.currentOverride(overrideServer))
		m.input = ti
	case modeEnterPortOverride:
		ti := m.newInput("e.g. 1194 (empty clears)", false)
		ti.CharLimit = 6
		ti.SetValue(m.currentOverride(overridePort))
		m.input = ti
	case modeEnterProtoOverride:
		ti := m.newInput("tcp | udp | empty", false)
		ti.CharLimit = 5
		ti.SetValue(m.currentOverride(overrideProto))
		m.input = ti
	}
}

// currentOverride returns the latest known value for an override key
// straight from openvpn3. Empty when unset or on lookup failure — the
// edit-form prefill is best-effort and shouldn't block the user.
func (m *Model) currentOverride(name string) string {
	ovs, err := m.svc.Overrides(m.cfgPath)
	if err != nil {
		return ""
	}
	for _, o := range ovs {
		if o.Name == name {
			return o.Value
		}
	}
	return ""
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
		m.tab = tab((int(m.tab) + 1) % tabCount)
		if m.tab == tabRaw {
			m.ensureRawLoaded()
		}
		return m, nil
	case "shift+tab":
		m.tab = tab((int(m.tab) + tabCount - 1) % tabCount)
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
		m.tab = tabNetwork
		return m, nil
	case "4":
		m.tab = tabAdvanced
		return m, nil
	case "5":
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
	case tabNetwork:
		return m.handleNetworkKey(key)
	case tabAdvanced:
		return m.handleAdvancedKey(key)
	case tabRaw:
		// Viewport handles its own scroll keys; forward the raw msg.
		var cmd tea.Cmd
		m.raw, cmd = m.raw.Update(msg)
		return m, cmd
	}
	return m, nil
}

// handleNetworkKey services the `network` tab: each row of overrides
// has a one-letter shortcut to edit (h/p/o) and a capital variant to
// unset.
func (m *Model) handleNetworkKey(key string) (tea.Model, tea.Cmd) {
	switch key {
	case "h":
		m.enterMode(modeEnterServerOverride)
	case "H":
		return m, m.clearOverrideCmd(overrideServer, "server override cleared")
	case "p":
		m.enterMode(modeEnterPortOverride)
	case "P":
		return m, m.clearOverrideCmd(overridePort, "port override cleared")
	case "o":
		m.enterMode(modeEnterProtoOverride)
	case "O":
		return m, m.clearOverrideCmd(overrideProto, "proto override cleared")
	}
	return m, nil
}

// handleAdvancedKey services the `advanced` tab. The three writable
// boolean flags (DCO, public access, locked down) toggle in-place; the
// rest are read-only on the daemon side and just render as info.
func (m *Model) handleAdvancedKey(key string) (tea.Model, tea.Cmd) {
	switch key {
	case "k":
		m.toggleBoolProperty("dco", "DCO")
	case "u":
		m.toggleBoolProperty("public_access", "public access")
	case "l":
		m.toggleBoolProperty("locked_down", "locked down")
	}
	return m, nil
}

// toggleBoolProperty flips one of the writable bool flags and reflects
// the result via the flash line. The daemon is the source of truth, so
// we read it back after the write rather than echoing our intended
// value — when a SetProperty silently no-ops (insufficient privilege
// for transfer_owner_session, etc) the user sees the actual state.
func (m *Model) toggleBoolProperty(name, label string) {
	props, err := m.svc.ConfigProperties(m.cfgPath)
	if err != nil {
		m.flashErr = err.Error()
		return
	}
	cur := false
	switch name {
	case "dco":
		cur = props.DCO
	case "public_access":
		cur = props.PublicAccess
	case "locked_down":
		cur = props.LockedDown
	}
	if err := m.svc.SetConfigBool(m.cfgPath, name, !cur); err != nil {
		m.flashErr = err.Error()
		return
	}
	if cur {
		m.flash = label + " off"
	} else {
		m.flash = label + " on"
	}
}

// clearOverrideCmd produces a Cmd that drops one override and flashes
// the supplied label. Wrapped so the keymap reads naturally without
// inlining a multi-line closure on every key.
func (m *Model) clearOverrideCmd(name, label string) tea.Cmd {
	return func() tea.Msg {
		if err := m.svc.SetOverride(m.cfgPath, name, ""); err != nil {
			m.flashErr = err.Error()
			return tickMsg{}
		}
		m.flash = label
		return tickMsg{}
	}
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

	case modeEnterServerOverride:
		return m.commitOverride(overrideServer, strings.TrimSpace(m.input.Value()),
			"server", nil)

	case modeEnterPortOverride:
		val := strings.TrimSpace(m.input.Value())
		return m.commitOverride(overridePort, val, "port", validatePort)

	case modeEnterProtoOverride:
		val := strings.ToLower(strings.TrimSpace(m.input.Value()))
		return m.commitOverride(overrideProto, val, "proto", validateProto)
	}
	return m, nil
}

// commitOverride is the shared "submit one network-tab field" path.
// Empty value clears the override; a non-nil validator runs first and
// short-circuits with flashErr on failure, leaving the form open so
// the user can fix the value without re-entering it.
func (m *Model) commitOverride(name, value, label string, validate func(string) error) (tea.Model, tea.Cmd) {
	if validate != nil && value != "" {
		if err := validate(value); err != nil {
			m.flashErr = err.Error()
			return m, nil
		}
	}
	if err := m.svc.SetOverride(m.cfgPath, name, value); err != nil {
		m.flashErr = err.Error()
		return m, nil
	}
	if value == "" {
		m.flash = label + " override cleared"
	} else {
		m.flash = label + " override → " + value
	}
	m.mode = modeView
	return m, nil
}

func validatePort(s string) error {
	for _, r := range s {
		if r < '0' || r > '9' {
			return errors.New("port must be digits only")
		}
	}
	if len(s) > 5 {
		return errors.New("port out of range")
	}
	return nil
}

func validateProto(s string) error {
	if s != "tcp" && s != "udp" {
		return errors.New("proto must be tcp or udp")
	}
	return nil
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
	const sidebarW = 22
	sidebar := m.renderSidebar(sidebarW)
	contentW := m.width - sidebarW - 2 // 2 spaces gap between
	var body string
	switch m.tab {
	case tabGeneral:
		body = m.renderGeneralTab(contentW)
	case tabAuth:
		body = m.renderAuthTab(contentW)
	case tabNetwork:
		body = m.renderNetworkTab(contentW)
	case tabAdvanced:
		body = m.renderAdvancedTab(contentW)
	case tabRaw:
		body = m.renderRawTab(contentW)
	}
	if flash := m.renderFlash(); flash != "" {
		body = body + "\n\n" + flash
	}
	inner := lipgloss.JoinHorizontal(lipgloss.Top, sidebar, "  ", body)
	help := components.HelpBar(m.helpKeys(), m.width)
	return lipgloss.JoinVertical(lipgloss.Left, header, "", inner, "", help)
}

// renderSidebar lists the three real tabs vertically — pink accent bar
// and bright text on the active one, muted on the rest. Matches the
// old decorative sidebar's shape, only with entries that actually do
// something.
func (m *Model) renderSidebar(w int) string {
	tabs := []struct {
		idx   tab
		key   string
		label string
	}{
		{tabGeneral, "1", "general"},
		{tabAuth, "2", "auth"},
		{tabNetwork, "3", "network"},
		{tabAdvanced, "4", "advanced"},
		{tabRaw, "5", "raw"},
	}
	var rows []string
	for _, t := range tabs {
		row := "  " +
			theme.Subtle.Render(t.key+" ") +
			theme.Dim.Render(t.label)
		if t.idx == m.tab {
			row = theme.AccentPink.Render("▎") + " " +
				theme.Subtle.Render(t.key+" ") +
				theme.Bright.Render(t.label)
		}
		rows = append(rows, row)
	}
	return components.Box{
		Content:     strings.Join(rows, "\n"),
		Width:       w - 4, // Box adds 4 (border + padding)
		BorderColor: theme.BorderLt,
	}.Render()
}

// renderGeneralTab: a row of kv pairs (parsed .ovpn summary + overlay
// flags). All read-only except for `f`/`a`/`c` actions, which mutate
// in place and flash the result.
func (m *Model) renderGeneralTab(w int) string {
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
		Width:       w - 4,
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
		Width:       w - 4,
		BorderColor: theme.Pink,
	}.Render()

	return parsed + "\n" + overlayBox
}

// renderNetworkTab — three rows for the supported openvpn3 overrides
// plus the input form when an override edit is in flight. Each row
// shows the active value (or "—") and the keys: lowercase to edit,
// uppercase to clear.
func (m *Model) renderNetworkTab(w int) string {
	switch m.mode {
	case modeEnterServerOverride:
		return m.renderInputForm("server-override",
			"Replaces the remote host from the .ovpn body. Empty value clears the override.")
	case modeEnterPortOverride:
		return m.renderInputForm("port-override",
			"Replaces the remote port. Numeric (1..65535). Empty clears.")
	case modeEnterProtoOverride:
		return m.renderInputForm("proto-override",
			"Force the transport: tcp or udp. Empty restores the .ovpn value.")
	}

	srv, port, proto := "—", "—", "—"
	if ovs, err := m.svc.Overrides(m.cfgPath); err == nil {
		for _, o := range ovs {
			switch o.Name {
			case overrideServer:
				srv = o.Value
			case overridePort:
				port = o.Value
			case overrideProto:
				proto = strings.ToUpper(o.Value)
			}
		}
	}

	overridesBox := components.Box{
		Title: theme.AccentCyan.Render("◆ ") + "transport overrides",
		Content: strings.Join([]string{
			kv("server", overrideValue(srv)) + "  " + theme.Subtle.Render("[h edit  H clear]"),
			kv("port", overrideValue(port)) + "  " + theme.Subtle.Render("[p edit  P clear]"),
			kv("proto", overrideValue(proto)) + "  " + theme.Subtle.Render("[o edit  O clear]"),
		}, "\n"),
		Width:       w - 4,
		BorderColor: theme.Pink,
	}.Render()

	hint := theme.Subtle.Render("Overrides apply on the next Connect — they don't rewrite the .ovpn body.")
	return overridesBox + "\n" + hint
}

// renderAdvancedTab — three writable flags + a read-only info block
// of usage metadata. Helps the user reason about *why* a profile is
// behaving differently than they expect (transferred ownership, locked
// down by another user, DCO experiments).
func (m *Model) renderAdvancedTab(w int) string {
	props, err := m.svc.ConfigProperties(m.cfgPath)
	if err != nil {
		return components.Box{
			Title:       theme.AccentRed.Render("✗ ") + "advanced",
			Content:     theme.AccentRed.Render("fetch failed: " + err.Error()),
			Width:       w - 4,
			BorderColor: theme.Red,
		}.Render()
	}

	flagsBox := components.Box{
		Title: theme.AccentCyan.Render("◆ ") + "flags",
		Content: strings.Join([]string{
			kv("dco", boolToggle(props.DCO)) + "  " + theme.Subtle.Render("[k toggle]") +
				"  " + theme.Dim.Render("· data channel offload"),
			kv("public", boolToggle(props.PublicAccess)) + "  " + theme.Subtle.Render("[u toggle]") +
				"  " + theme.Dim.Render("· visible to other users"),
			kv("locked", boolToggle(props.LockedDown)) + "  " + theme.Subtle.Render("[l toggle]") +
				"  " + theme.Dim.Render("· read-only profile body"),
		}, "\n"),
		Width:       w - 4,
		BorderColor: theme.Pink,
	}.Render()

	infoBox := components.Box{
		Title: theme.AccentCyan.Render("◆ ") + "info " + theme.Dim.Render("· read-only"),
		Content: strings.Join([]string{
			kv("persist", boolToggle(props.Persistent)),
			kv("xfer", boolToggle(props.TransferOwnerSession)),
			kv("uses", fmt.Sprintf("%d", props.UsedCount)),
			kv("import", formatUnix(props.ImportTs)),
			kv("last", formatUnix(props.LastUsedTs)),
		}, "\n"),
		Width:       w - 4,
		BorderColor: theme.BorderLt,
	}.Render()

	return flagsBox + "\n" + infoBox
}

// overrideValue dims placeholders and brightens real values so the eye
// catches what's been customised at a glance.
func overrideValue(v string) string {
	if v == "" || v == "—" {
		return theme.Subtle.Render("—")
	}
	return theme.AccentCyan.Render(v)
}

func formatUnix(ts int64) string {
	if ts <= 0 {
		return theme.Subtle.Render("—")
	}
	return theme.Dim.Render(time.Unix(ts, 0).Format("2006-01-02 15:04"))
}

// renderAuthTab — the original credentials + TOTP layout.
func (m *Model) renderAuthTab(w int) string {
	creds := m.renderCreds(w)
	totp := m.renderTOTP(w)
	return creds + "\n" + totp
}

// renderRawTab — viewport over the .ovpn body. Errors are surfaced
// as a one-line message instead of an empty box.
func (m *Model) renderRawTab(w int) string {
	if m.rawErr != "" {
		return theme.AccentRed.Render("fetch failed: " + m.rawErr)
	}
	// Resize the viewport to the actual content area we got — the
	// SetSize hook only knows the screen width, not how much the
	// sidebar took.
	if m.raw.Width != w-4 {
		m.raw.Width = w - 4
	}
	return components.Box{
		Title:       theme.AccentCyan.Render("◆ ") + "raw .ovpn (read-only)",
		Content:     m.raw.View(),
		Width:       w - 4,
		BorderColor: theme.BorderLt,
	}.Render()
}

func boolToggle(b bool) string {
	if b {
		return theme.AccentMint.Render("on ✓")
	}
	return theme.Subtle.Render("off")
}

func (m *Model) renderCreds(w int) string {
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
		Width:   w - 4,
	}.Render()
}

func (m *Model) renderTOTP(w int) string {
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
		Width:       w - 4,
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
	// Big-font code — takes ~5 rows but reads across the room. The pink
	// foreground keeps it on-brand with the TOTP card border; the small
	// pretty-formatted version under it gives copy/paste-friendly text
	// for callers who don't want to OCR block art.
	big := lipgloss.NewStyle().Foreground(theme.Pink).Bold(true).
		Render(components.BigDigits(code))
	small := lipgloss.NewStyle().Foreground(theme.FgDim).Render(prettyCode(code))

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
		big,
		small,
		"",
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
		Width(m.width - 32).
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

// helpKeys returns the footer keymap for the *current tab and mode*.
// Mixing keys across tabs was the bug that prompted the rewrite:
// general tab showed `u username · p password` even though those
// keys do nothing while on it.
func (m *Model) helpKeys() []components.KeyHelp {
	if m.mode == modeRemoveOTPConfirm {
		return []components.KeyHelp{{Key: "y", Label: "remove"}, {Key: "n", Label: "keep"}}
	}
	if m.mode != modeView {
		return []components.KeyHelp{{Key: "enter", Label: "save"}, {Key: "esc", Label: "cancel"}}
	}
	common := []components.KeyHelp{
		{Key: "1-5", Label: "tab"},
	}
	switch m.tab {
	case tabGeneral:
		return append(common,
			components.KeyHelp{Key: "f", Label: "favorite"},
			components.KeyHelp{Key: "a", Label: "auto"},
			components.KeyHelp{Key: "c", Label: "country"},
			components.KeyHelp{Key: "q/esc", Label: "back"},
		)
	case tabNetwork:
		return append(common,
			components.KeyHelp{Key: "h/H", Label: "server set/clear"},
			components.KeyHelp{Key: "p/P", Label: "port set/clear"},
			components.KeyHelp{Key: "o/O", Label: "proto set/clear"},
			components.KeyHelp{Key: "q/esc", Label: "back"},
		)
	case tabAdvanced:
		return append(common,
			components.KeyHelp{Key: "k", Label: "toggle dco"},
			components.KeyHelp{Key: "u", Label: "toggle public"},
			components.KeyHelp{Key: "l", Label: "toggle locked"},
			components.KeyHelp{Key: "q/esc", Label: "back"},
		)
	case tabAuth:
		rows := make([]components.KeyHelp, 0, 8)
		rows = append(rows, common...)
		rows = append(rows,
			components.KeyHelp{Key: "u", Label: "username"},
			components.KeyHelp{Key: "p", Label: "password"},
			components.KeyHelp{Key: "d", Label: "clear creds"},
			components.KeyHelp{Key: "i", Label: "import OTP"},
		)
		if m.svc.HasOTP(m.cfgPath) {
			rows = append(rows, components.KeyHelp{Key: "x", Label: "remove OTP"})
		}
		rows = append(rows, components.KeyHelp{Key: "q/esc", Label: "back"})
		return rows
	case tabRaw:
		return append(common,
			components.KeyHelp{Key: "↑↓", Label: "scroll"},
			components.KeyHelp{Key: "q/esc", Label: "back"},
		)
	}
	return common
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
