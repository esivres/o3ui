// Package edit renders the per-config editor — for v1 the focus is the
// "authentication" tab: stored username/password and a TOTP card with
// import flows (URI, manual base32, account picker for migration QRs).
// Other tabs from the design (general/network/advanced/raw) are stubbed
// so the layout matches the mock without pretending to do work it can't.
package edit

import (
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/esivres/openvpn3ui/internal/app"
	"github.com/esivres/openvpn3ui/internal/otp"
	"github.com/esivres/openvpn3ui/internal/otpimport"
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
	modeEnterURI
	modeEnterManual
	modeEnterQRPath
	modeEnterUsername
	modeEnterPassword
	modePickAccount
	modeRemoveOTPConfirm
)

type Model struct {
	svc     *app.Service
	width   int
	height  int
	cfgPath string
	cfgName string

	mode  mode
	input textinput.Model

	// Picker state — populated when an imported URI yields multiple
	// accounts (typical of Google Authenticator's bulk export QR).
	accounts      []otpimport.Account
	accountCursor int

	flash    string // ephemeral success message (cleared on next mode change)
	flashErr string
}

func New(svc *app.Service, configPath, configName string) *Model {
	return &Model{
		svc:     svc,
		cfgPath: configPath,
		cfgName: configName,
	}
}

func (m *Model) Init() tea.Cmd { return tick() }

// HelpKeys feeds the `?` overlay. Mirrors the helpbar at the bottom
// of the screen but stays exhaustive — overlay is for discovery,
// footer for muscle memory.
func (m *Model) HelpKeys() []components.KeyHelp {
	return []components.KeyHelp{
		{Key: "u", Label: "edit / set username"},
		{Key: "p", Label: "edit / set password"},
		{Key: "d", Label: "clear saved credentials"},
		{Key: "i", Label: "open OTP import screen"},
		{Key: "x", Label: "remove the TOTP secret"},
		{Key: "q / esc", Label: "back to the profile list"},
	}
}

func (m *Model) SetSize(w, h int) { m.width, m.height = w, h }

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
	case modeEnterURI:
		m.input = m.newInput("otpauth:// or otpauth-migration://offline?data=…", false)
	case modeEnterManual:
		m.input = m.newInput("base32 secret (e.g. JBSWY3DPEHPK3PXP)", false)
	case modeEnterQRPath:
		m.input = m.newInput("absolute path to PNG/JPEG with QR code", false)
	case modeEnterUsername:
		ti := m.newInput("vpn login", false)
		if user, _, _ := m.svc.GetCredentials(m.cfgPath); user != "" {
			ti.SetValue(user)
		}
		m.input = ti
	case modeEnterPassword:
		m.input = m.newInput("vpn password", true)
	}
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

	if m.mode != modeView && m.mode != modePickAccount && m.mode != modeRemoveOTPConfirm {
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

		switch m.mode {
		case modePickAccount:
			switch key {
			case "up", "k":
				if m.accountCursor > 0 {
					m.accountCursor--
				}
			case "down", "j":
				if m.accountCursor < len(m.accounts)-1 {
					m.accountCursor++
				}
			}
			return m, nil
		case modeRemoveOTPConfirm:
			return m, nil
		}

		var cmd tea.Cmd
		m.input, cmd = m.input.Update(msg)
		return m, cmd
	}

	// View mode — top-level shortcuts.
	switch key {
	case "esc", "q":
		return m, func() tea.Msg { return BackMsg{} }
	case "u":
		m.enterMode(modeEnterUsername)
	case "p":
		m.enterMode(modeEnterPassword)
	case "i":
		// Hand off to the dedicated import screen — three-tab layout,
		// file picker for QR, live preview after success.
		cp, cn := m.cfgPath, m.cfgName
		return m, func() tea.Msg { return OpenOTPImportMsg{ConfigPath: cp, ConfigName: cn} }
	case "m":
		m.enterMode(modeEnterManual)
	case "g":
		m.enterMode(modeEnterQRPath)
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

// commit handles Enter inside a sub-form. Each branch performs the
// service call and either returns to view with a flash, or transitions
// to another sub-mode (the account picker after a multi-account import).
func (m *Model) commit() (tea.Model, tea.Cmd) {
	switch m.mode {
	case modeEnterURI:
		raw := strings.TrimSpace(m.input.Value())
		accs, err := otpimport.ParseURI(raw)
		if err != nil {
			m.flashErr = err.Error()
			return m, nil
		}
		return m.handleParsedAccounts(accs)

	case modeEnterQRPath:
		path := strings.TrimSpace(m.input.Value())
		f, err := os.Open(path)
		if err != nil {
			m.flashErr = err.Error()
			return m, nil
		}
		defer f.Close()
		uri, err := otpimport.DecodeQRImage(f)
		if err != nil {
			m.flashErr = err.Error()
			return m, nil
		}
		accs, err := otpimport.ParseURI(uri)
		if err != nil {
			m.flashErr = err.Error()
			return m, nil
		}
		return m.handleParsedAccounts(accs)

	case modeEnterManual:
		secret := strings.TrimSpace(m.input.Value())
		if err := m.svc.SetOTP(m.cfgPath, secret); err != nil {
			m.flashErr = err.Error()
			return m, nil
		}
		m.flash = "OTP saved"
		m.mode = modeView
		return m, nil

	case modePickAccount:
		if m.accountCursor < 0 || m.accountCursor >= len(m.accounts) {
			return m, nil
		}
		picked := m.accounts[m.accountCursor]
		if err := m.svc.SetOTP(m.cfgPath, picked.Secret); err != nil {
			m.flashErr = err.Error()
			return m, nil
		}
		m.flash = fmt.Sprintf("OTP imported: %s", picked.Label())
		m.accounts = nil
		m.accountCursor = 0
		m.mode = modeView
		return m, nil

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
	}
	return m, nil
}

func (m *Model) handleParsedAccounts(accs []otpimport.Account) (tea.Model, tea.Cmd) {
	if len(accs) == 0 {
		m.flashErr = "no accounts found in import"
		return m, nil
	}
	if len(accs) == 1 {
		if err := m.svc.SetOTP(m.cfgPath, accs[0].Secret); err != nil {
			m.flashErr = err.Error()
			return m, nil
		}
		m.flash = "OTP imported"
		m.mode = modeView
		return m, nil
	}
	m.accounts = accs
	m.accountCursor = 0
	m.mode = modePickAccount
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
	// The decorative 5-tab sidebar (general / network / advanced /
	// raw .ovpn) is gone — only the authentication tab actually does
	// anything, so a sidebar made up of dead controls actively
	// undermined trust. When more tabs land we'll bring it back with
	// real wiring; for now the screen uses the full width.
	body := lipgloss.NewStyle().Width(m.width - 4).Render(m.renderRight())
	help := components.HelpBar(m.helpKeys(), m.width)
	return lipgloss.JoinVertical(lipgloss.Left, header, "", body, "", help)
}

func (m *Model) renderRight() string {
	creds := m.renderCreds()
	totp := m.renderTOTP()
	flash := m.renderFlash()
	parts := []string{creds, "", totp}
	if flash != "" {
		parts = append(parts, "", flash)
	}
	return strings.Join(parts, "\n")
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
	case modeEnterURI:
		content = m.renderInputForm("paste URI", "otpauth:// or otpauth-migration://offline?data=…")
	case modeEnterManual:
		content = m.renderInputForm("base32 secret", "Will be validated and stored in the system keyring.")
	case modeEnterQRPath:
		content = m.renderInputForm("QR image path", "Local PNG or JPEG containing an otpauth QR.")
	case modePickAccount:
		content = m.renderAccountPicker()
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
			theme.Subtle.Render("[m] quick manual base32 entry"),
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
		theme.Subtle.Render("[m] manual"),
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

func (m *Model) renderAccountPicker() string {
	rows := []string{theme.Bright.Render("Multiple accounts found — pick one:"), ""}
	for i, a := range m.accounts {
		marker := "  "
		style := lipgloss.NewStyle().Foreground(theme.Fg)
		if i == m.accountCursor {
			marker = theme.AccentPink.Render("› ")
			style = lipgloss.NewStyle().Foreground(theme.FgBright).Bold(true)
		}
		rows = append(rows, marker+style.Render(a.Label()))
	}
	rows = append(rows, "",
		theme.Dim.Render("↑/↓")+theme.Subtle.Render(" choose · ")+
			theme.Dim.Render("enter")+theme.Subtle.Render(" save · ")+
			theme.Dim.Render("esc")+theme.Subtle.Render(" cancel"))
	return strings.Join(rows, "\n")
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
			{Key: "i", Label: "import OTP"}, {Key: "m", Label: "manual base32"},
		}
		if m.svc.HasOTP(m.cfgPath) {
			base = append(base, components.KeyHelp{Key: "x", Label: "remove OTP"})
		}
		return append(base, components.KeyHelp{Key: "q/esc", Label: "back"})
	case modePickAccount:
		return []components.KeyHelp{
			{Key: "↑/↓", Label: "choose"}, {Key: "enter", Label: "use"}, {Key: "esc", Label: "cancel"},
		}
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
