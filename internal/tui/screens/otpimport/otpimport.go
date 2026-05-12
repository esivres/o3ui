// Package otpimport hosts the dedicated "import OTP" screen — a focused
// flow with three tabs (paste URI, pick QR file, type secret manually),
// account picker for migration QRs, and a live preview of the resulting
// code so the user knows the import worked before they leave.
package otpimport

import (
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/filepicker"
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/esivres/openvpn3ui/internal/app"
	"github.com/esivres/openvpn3ui/internal/otpimport"
	"github.com/esivres/openvpn3ui/internal/tui/components"
	"github.com/esivres/openvpn3ui/internal/tui/theme"
)

// BackMsg signals the root that the user is done — no save or save-then-back.
type BackMsg struct{}

type tickMsg struct{}

// tab is the top-level mode the screen is in.
type tab int

const (
	tabURI tab = iota
	tabQR
	tabManual
)

// stage is a tiny inner state for after-import flows: pick an account
// when the import yielded several, or sit on the success preview.
type stage int

const (
	stageInput stage = iota
	stagePicker
	stageDone
)

type Model struct {
	svc     *app.Service
	width   int
	height  int
	cfgPath string
	cfgName string

	tab         tab
	stage       stage
	uriInput    textinput.Model
	manualInput textinput.Model
	picker      filepicker.Model

	accounts      []otpimport.Account
	accountCursor int

	previewSecret string // base32 we just imported, used for live preview
	flashErr      string
	flashOK       string
}

func New(svc *app.Service, configPath, configName string) *Model {
	uri := textinput.New()
	uri.CharLimit = 4096
	uri.Width = 64
	uri.Prompt = ""
	uri.Placeholder = "otpauth:// or otpauth-migration://offline?data=…"
	uri.Focus()

	manual := textinput.New()
	manual.CharLimit = 256
	manual.Width = 40
	manual.Prompt = ""
	manual.Placeholder = "JBSWY3DPEHPK3PXP"

	fp := filepicker.New()
	// Don't restrict by extension — bubbles' canSelect() does a
	// case-sensitive HasSuffix, so ".png" rejects "Screenshot.PNG"
	// silently. Easier UX: let any file through and surface a clear
	// "not an image" error from our QR decoder if it's wrong.
	fp.AllowedTypes = nil
	// Start in the directory the user invoked us from. Authenticator-app
	// QR screenshots usually land in CWD or Downloads; jumping straight
	// to $HOME forces extra navigation.
	if cwd, err := os.Getwd(); err == nil {
		fp.CurrentDirectory = cwd
	} else if home, err := os.UserHomeDir(); err == nil {
		fp.CurrentDirectory = home
	}
	fp.ShowHidden = false

	return &Model{
		svc: svc, cfgPath: configPath, cfgName: configName,
		uriInput: uri, manualInput: manual, picker: fp,
	}
}

func (m *Model) Init() tea.Cmd {
	return tea.Batch(textinput.Blink, m.picker.Init(), tick())
}

func tick() tea.Cmd {
	return tea.Tick(time.Second, func(time.Time) tea.Msg { return tickMsg{} })
}

func (m *Model) SetSize(w, h int) {
	m.width, m.height = w, h
	// Filepicker height is content rows; reserve room for header + tabs + footer.
	ph := h - 14
	if ph < 8 {
		ph = 8
	}
	m.picker.SetHeight(ph)
}

func (m *Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.SetSize(msg.Width, msg.Height)
		return m, nil

	case tickMsg:
		// Re-render so live TOTP preview ticks.
		return m, tick()

	case tea.KeyMsg:
		return m.handleKey(msg)

	case importErrMsg, importParsedMsg:
		return m.handleImportMsgs(msg)
	}

	// Non-key messages (file-read results, cursor blink, etc.) must land
	// in every sub-component regardless of which tab is currently active.
	// Earlier we only forwarded to the active tab, which meant filepicker
	// dropped its initial readDir result while the user was on the URI
	// tab — when they later switched to the QR tab the picker showed an
	// empty / wrong directory.
	var c1, c2, c3 tea.Cmd
	m.uriInput, c1 = m.uriInput.Update(msg)
	m.manualInput, c2 = m.manualInput.Update(msg)
	m.picker, c3 = m.picker.Update(msg)
	return m, tea.Batch(c1, c2, c3)
}

func (m *Model) handleKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	key := msg.String()

	// Global: esc / q always backs out, regardless of stage.
	if key == "esc" {
		return m, func() tea.Msg { return BackMsg{} }
	}
	if key == "q" && m.stage != stageInput {
		// In input stages, "q" might be a typed character; only treat
		// it as quit on done/picker stages.
		return m, func() tea.Msg { return BackMsg{} }
	}

	if m.stage == stagePicker {
		switch key {
		case "up", "k":
			if m.accountCursor > 0 {
				m.accountCursor--
			}
		case "down", "j":
			if m.accountCursor < len(m.accounts)-1 {
				m.accountCursor++
			}
		case "enter":
			return m, m.commitAccount()
		}
		return m, nil
	}

	if m.stage == stageDone {
		switch key {
		case "enter", "esc":
			return m, func() tea.Msg { return BackMsg{} }
		}
		return m, nil
	}

	// Tab-switch shortcuts. Apply BEFORE forwarding so the per-tab
	// widget never sees them. We deliberately don't bind a single
	// digit / "tab" inside the URI/Manual textinputs.
	switch key {
	case "tab":
		m.tab = (m.tab + 1) % 3
		m.refocus()
		return m, nil
	case "shift+tab":
		m.tab = (m.tab + 2) % 3
		m.refocus()
		return m, nil
	}
	if m.tab != tabURI && m.tab != tabManual {
		// On the QR tab, "1/2/3" can be a quick tab-switch — text
		// inputs need them as literal characters, so only intercept
		// when the focused widget isn't a text field.
		switch key {
		case "1":
			m.tab = tabURI
			m.refocus()
			return m, nil
		case "2":
			m.tab = tabQR
			m.refocus()
			return m, nil
		case "3":
			m.tab = tabManual
			m.refocus()
			return m, nil
		}
	}

	// Per-tab key handling.
	switch m.tab {
	case tabURI:
		if key == "enter" {
			return m, m.commitInput()
		}
		var cmd tea.Cmd
		m.uriInput, cmd = m.uriInput.Update(msg)
		return m, cmd
	case tabManual:
		if key == "enter" {
			return m, m.commitInput()
		}
		var cmd tea.Cmd
		m.manualInput, cmd = m.manualInput.Update(msg)
		return m, cmd
	case tabQR:
		// Enter / Right go through to the file picker — bubbles
		// filepicker uses Enter as the "select file" key and
		// Right/l only to enter directories.
		var cmd tea.Cmd
		m.picker, cmd = m.picker.Update(msg)
		if did, path := m.picker.DidSelectFile(msg); did {
			m.flashErr = ""
			return m, m.importFromFile(path)
		}
		// Tell the user when they tried to select a file the picker
		// rejected (wrong extension) — silent rejection is the worst
		// possible UX here.
		if did, path := m.picker.DidSelectDisabledFile(msg); did {
			m.flashErr = "not an image file: " + path
		}
		return m, cmd
	}
	return m, nil
}

func (m *Model) refocus() {
	m.uriInput.Blur()
	m.manualInput.Blur()
	switch m.tab {
	case tabURI:
		m.uriInput.Focus()
	case tabManual:
		m.manualInput.Focus()
	}
}

func (m *Model) commitInput() tea.Cmd {
	switch m.tab {
	case tabURI:
		raw := strings.TrimSpace(m.uriInput.Value())
		accs, err := otpimport.ParseURI(raw)
		if err != nil {
			m.flashErr = err.Error()
			return nil
		}
		return m.handleParsed(accs)
	case tabManual:
		secret := strings.TrimSpace(m.manualInput.Value())
		return m.saveSecret(secret, "manual entry")
	}
	return nil
}

func (m *Model) importFromFile(path string) tea.Cmd {
	return func() tea.Msg {
		f, err := os.Open(path)
		if err != nil {
			return importErrMsg{err: err}
		}
		defer f.Close()
		uri, err := otpimport.DecodeQRImage(f)
		if err != nil {
			return importErrMsg{err: fmt.Errorf("decode QR: %w", err)}
		}
		accs, err := otpimport.ParseURI(uri)
		if err != nil {
			return importErrMsg{err: err}
		}
		return importParsedMsg{accounts: accs}
	}
}

type importErrMsg struct{ err error }
type importParsedMsg struct{ accounts []otpimport.Account }

func (m *Model) handleParsed(accs []otpimport.Account) tea.Cmd {
	if len(accs) == 0 {
		m.flashErr = "no accounts found in import"
		return nil
	}
	if len(accs) == 1 {
		return m.saveSecret(accs[0].Secret, accs[0].Label())
	}
	m.accounts = accs
	m.accountCursor = 0
	m.stage = stagePicker
	m.flashErr = ""
	return nil
}

func (m *Model) commitAccount() tea.Cmd {
	if m.accountCursor < 0 || m.accountCursor >= len(m.accounts) {
		return nil
	}
	a := m.accounts[m.accountCursor]
	return m.saveSecret(a.Secret, a.Label())
}

func (m *Model) saveSecret(secret, source string) tea.Cmd {
	if err := m.svc.SetOTP(m.cfgPath, secret); err != nil {
		m.flashErr = err.Error()
		return nil
	}
	m.previewSecret = secret
	m.flashOK = "imported from " + source
	m.flashErr = ""
	m.stage = stageDone
	return nil
}

func (m *Model) Update_(msg tea.Msg) (tea.Model, tea.Cmd) { return m, nil } // unused, kept for future split

// ----- View ---------------------------------------------------------------

func (m *Model) View() string {
	if m.width == 0 {
		return "loading…"
	}
	header := components.HeaderBar(
		"ovpn3",
		"import OTP · "+m.cfgName,
		[]string{components.Pill("two-factor", theme.Pink, theme.PinkSoft)},
		m.width,
	)

	body := m.renderCard()
	help := components.HelpBar(m.helpKeys(), m.width)
	return lipgloss.JoinVertical(lipgloss.Left, header, "", body, "", help)
}

func (m *Model) renderCard() string {
	cardWidth := minInt(78, m.width-4)

	var content string
	switch m.stage {
	case stagePicker:
		content = m.renderAccountPicker()
	case stageDone:
		content = m.renderDone()
	default:
		content = m.renderInput()
	}

	tabsRow := m.renderTabs()
	flash := m.renderFlash()
	inner := tabsRow + "\n\n" + content
	if flash != "" {
		inner += "\n\n" + flash
	}

	card := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(theme.Pink).
		Background(theme.Panel).
		Padding(1, 2).
		Width(cardWidth).
		Render(inner)

	return lipgloss.Place(m.width, m.height-6,
		lipgloss.Center, lipgloss.Center,
		card,
	)
}

// import handler messages
func (m *Model) handleImportMsgs(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case importErrMsg:
		m.flashErr = msg.err.Error()
	case importParsedMsg:
		return m, m.handleParsed(msg.accounts)
	}
	return m, nil
}

func (m *Model) renderTabs() string {
	pill := func(active bool, label string) string {
		bg := theme.Panel2
		fg := theme.FgDim
		if active {
			bg = theme.Pink
			fg = theme.FgBright
		}
		return components.Pill(label, fg, bg)
	}
	return strings.Join([]string{
		pill(m.tab == tabURI, "1 · paste URI"),
		pill(m.tab == tabQR, "2 · QR image"),
		pill(m.tab == tabManual, "3 · manual"),
	}, "  ") + "   " + theme.Subtle.Render("tab to switch")
}

func (m *Model) renderInput() string {
	switch m.tab {
	case tabURI:
		field := lipgloss.NewStyle().
			Background(theme.Surface).
			Border(lipgloss.RoundedBorder()).
			BorderForeground(theme.Pink).
			Padding(0, 1).
			Width(64).
			Render(m.uriInput.View())
		return strings.Join([]string{
			theme.AccentPink.Render("› ") + theme.Bright.Render("paste otpauth URI"),
			field,
			"",
			theme.Subtle.Render("supports otpauth:// (single account) and otpauth-migration:// (Google Authenticator export with multiple accounts)"),
		}, "\n")
	case tabQR:
		title := theme.AccentPink.Render("› ") + theme.Bright.Render("pick a QR image")
		// The default filepicker view doesn't show the cwd anywhere,
		// which made it look like the cursor was "lost" after a few
		// dir hops. Surface it as a breadcrumb above the listing.
		path := theme.AccentCyan.Render(m.picker.CurrentDirectory)
		help := theme.Subtle.Render("↑↓ navigate · enter open dir / select file · backspace up")
		return title + "  " + path + "\n\n" + m.picker.View() + "\n" + help
	case tabManual:
		field := lipgloss.NewStyle().
			Background(theme.Surface).
			Border(lipgloss.RoundedBorder()).
			BorderForeground(theme.Pink).
			Padding(0, 1).
			Width(40).
			Render(m.manualInput.View())
		return strings.Join([]string{
			theme.AccentPink.Render("› ") + theme.Bright.Render("type base32 secret"),
			field,
			"",
			theme.Subtle.Render("manual fallback — paste the raw shared secret your authenticator app showed you"),
		}, "\n")
	}
	return ""
}

func (m *Model) renderAccountPicker() string {
	rows := []string{
		theme.Bright.Render("Multiple accounts found — pick one:"),
		"",
	}
	for i, a := range m.accounts {
		marker := "  "
		nameStyle := lipgloss.NewStyle().Foreground(theme.Fg)
		if i == m.accountCursor {
			marker = theme.AccentPink.Render("› ")
			nameStyle = lipgloss.NewStyle().Foreground(theme.FgBright).Bold(true)
		}
		rows = append(rows, marker+nameStyle.Render(a.Label()))
	}
	rows = append(rows, "", theme.Subtle.Render("↑/↓ choose · enter use · esc cancel"))
	return strings.Join(rows, "\n")
}

func (m *Model) renderDone() string {
	code, ok := m.svc.PreviewOTP(m.cfgPath)
	preview := theme.Subtle.Render("(preview unavailable)")
	if ok {
		preview = lipgloss.NewStyle().
			Background(theme.PinkSoft).
			Foreground(theme.FgBright).
			Bold(true).
			Padding(0, 3).
			Render(prettyCode(code))
	}
	rem := 30 - int(time.Now().Unix()%30)

	return strings.Join([]string{
		theme.AccentMint.Render("✓ Import successful"),
		"",
		preview,
		"",
		theme.Dim.Render(fmt.Sprintf("refresh in %ds", rem)),
		"",
		theme.Subtle.Render("press enter or esc to return"),
	}, "\n")
}

func (m *Model) renderFlash() string {
	if m.flashErr != "" {
		return theme.AccentRed.Render("✗ " + m.flashErr)
	}
	if m.flashOK != "" && m.stage != stageDone {
		return theme.AccentMint.Render("✓ " + m.flashOK)
	}
	return ""
}

func (m *Model) helpKeys() []components.KeyHelp {
	switch m.stage {
	case stagePicker:
		return []components.KeyHelp{
			{Key: "↑/↓", Label: "choose"}, {Key: "enter", Label: "use"}, {Key: "esc", Label: "cancel"},
		}
	case stageDone:
		return []components.KeyHelp{{Key: "enter/esc", Label: "back"}}
	default:
		return []components.KeyHelp{
			{Key: "tab", Label: "switch"}, {Key: "enter", Label: "import"}, {Key: "esc", Label: "back"},
		}
	}
}

func prettyCode(code string) string {
	if len(code) <= 4 {
		return code
	}
	mid := len(code) / 2
	return code[:mid] + " " + code[mid:]
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}
