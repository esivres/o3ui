// Package auth renders the modal shown when openvpn3 raises a UserInput
// challenge during Connect. The TUI Prompter routes one prompt at a time
// here; the user submits a value (with optional "remember") or cancels.
package auth

import (
	"strings"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/esivres/openvpn3ui/internal/ovpn"
	"github.com/esivres/openvpn3ui/internal/tui/components"
	"github.com/esivres/openvpn3ui/internal/tui/theme"
)

// SubmitMsg is sent when the user confirms the input.
type SubmitMsg struct {
	Value    string
	Remember bool
}

// CancelMsg is sent when the user dismisses the modal.
type CancelMsg struct{}

type Model struct {
	width      int
	height     int
	configName string
	prompt     ovpn.InputPrompt

	input    textinput.Model
	remember bool
}

func New(configName string, p ovpn.InputPrompt) *Model {
	ti := textinput.New()
	ti.CharLimit = 256
	ti.Width = 32
	ti.Prompt = ""
	ti.Placeholder = ""
	if p.Hidden {
		ti.EchoMode = textinput.EchoPassword
		ti.EchoCharacter = '•'
	}
	ti.Focus()

	return &Model{
		configName: configName,
		prompt:     p,
		input:      ti,
		remember:   true, // sensible default: don't make the user re-type next time
	}
}

func (m *Model) Init() tea.Cmd { return textinput.Blink }

func (m *Model) SetSize(w, h int) { m.width, m.height = w, h }

func (m *Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.SetSize(msg.Width, msg.Height)

	case tea.KeyMsg:
		switch msg.String() {
		case "enter":
			val := m.input.Value()
			rem := m.remember
			return m, func() tea.Msg { return SubmitMsg{Value: val, Remember: rem} }
		case "esc":
			return m, func() tea.Msg { return CancelMsg{} }
		case "tab":
			m.remember = !m.remember
			return m, nil
		}
	}

	var cmd tea.Cmd
	m.input, cmd = m.input.Update(msg)
	return m, cmd
}

func (m *Model) View() string {
	if m.width == 0 {
		return ""
	}

	titleBar := lipgloss.NewStyle().
		Background(theme.Purple).
		Foreground(theme.FgBright).
		Bold(true).
		Padding(0, 2).
		Render("🔐 Authentication required")

	subtitle := lipgloss.NewStyle().
		Foreground(theme.FgDim).
		Render(m.configName)

	// Field header — pink "›" arrow + prompt name; uses description as
	// a smaller hint when the server provides one.
	label := theme.AccentPink.Render("› ") + theme.Bright.Render(promptLabel(m.prompt))
	if m.prompt.Description != "" && m.prompt.Description != m.prompt.Name {
		label += "  " + theme.Subtle.Render(m.prompt.Description)
	}

	field := lipgloss.NewStyle().
		Background(theme.Surface).
		Foreground(theme.Fg).
		Border(lipgloss.RoundedBorder()).
		BorderForeground(theme.Pink).
		Padding(0, 1).
		Width(36).
		Render(m.input.View())

	rememberMark := theme.Subtle.Render("[ ]")
	if m.remember {
		rememberMark = theme.AccentMint.Render("[✓]")
	}
	rememberRow := rememberMark + " " + theme.Dim.Render("remember (saved to system keyring)") +
		theme.Subtle.Render("   tab to toggle")

	submit := lipgloss.NewStyle().
		Background(theme.Pink).
		Foreground(theme.FgBright).
		Bold(true).
		Padding(0, 2).
		Render("authenticate ✓")
	cancel := lipgloss.NewStyle().
		Background(theme.Panel2).
		Foreground(theme.FgDim).
		Padding(0, 2).
		Render("cancel")
	buttons := lipgloss.JoinHorizontal(lipgloss.Top, submit, "  ", cancel)

	body := lipgloss.JoinVertical(lipgloss.Left,
		titleBar,
		"",
		subtitle,
		"",
		label,
		field,
		"",
		rememberRow,
		"",
		buttons,
	)
	modal := lipgloss.NewStyle().
		Padding(1, 2).
		Border(lipgloss.RoundedBorder()).
		BorderForeground(theme.Purple).
		Background(theme.Panel).
		Render(body)

	help := components.HelpBar([]components.KeyHelp{
		{Key: "enter", Label: "authenticate"},
		{Key: "tab", Label: "toggle remember"},
		{Key: "esc", Label: "cancel"},
	}, m.width)

	// Center the modal vertically (give it ~70% of height) and horizontally.
	canvas := lipgloss.Place(m.width, m.height-2,
		lipgloss.Center, lipgloss.Center,
		modal,
	)
	return lipgloss.JoinVertical(lipgloss.Left, canvas, help)
}

// promptLabel turns the openvpn3 prompt name into something nicer than the
// raw "static_challenge" / "auth_pass" identifiers.
func promptLabel(p ovpn.InputPrompt) string {
	name := strings.ToLower(p.Name)
	switch {
	case strings.Contains(name, "user"):
		return "username"
	case strings.Contains(name, "pass"):
		return "password"
	case strings.Contains(name, "challenge"), strings.Contains(name, "otp"):
		return "one-time code"
	default:
		return p.Name
	}
}
