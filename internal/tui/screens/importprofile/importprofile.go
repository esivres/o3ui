// Package importprofile is a single-screen UI for picking a portable
// `.o3ui.json` bundle off disk and feeding it to Service.ImportPortable.
// Mirrors the OTP import screen's filepicker pattern at a smaller scale
// — no tabs, no manual-entry fallback. Errors and successes leave the
// screen with BackMsg + FlashMsg so the list view shows the outcome.
package importprofile

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/charmbracelet/bubbles/filepicker"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/esivres/openvpn3ui/internal/app"
	"github.com/esivres/openvpn3ui/internal/tui/components"
	"github.com/esivres/openvpn3ui/internal/tui/screens/list"
	"github.com/esivres/openvpn3ui/internal/tui/theme"
)

// BackMsg signals root to return to the list screen.
type BackMsg struct{}

type Model struct {
	svc    *app.Service
	width  int
	height int

	picker filepicker.Model
	err    string
}

func New(svc *app.Service) *Model {
	fp := filepicker.New()
	// Accept any extension; the schema check happens after read.
	// Filtering on .json would lock out users who renamed the file.
	fp.AllowedTypes = nil
	fp.ShowHidden = false
	// Start in the user's home where exports default to landing.
	if home, err := os.UserHomeDir(); err == nil {
		fp.CurrentDirectory = home
	} else if cwd, err := os.Getwd(); err == nil {
		fp.CurrentDirectory = cwd
	}
	return &Model{svc: svc, picker: fp}
}

func (m *Model) Init() tea.Cmd { return m.picker.Init() }

func (m *Model) SetSize(w, h int) {
	m.width, m.height = w, h
	ph := h - 10
	if ph < 8 {
		ph = 8
	}
	m.picker.SetHeight(ph)
}

func (m *Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.SetSize(msg.Width, msg.Height)
	case tea.KeyMsg:
		switch msg.String() {
		case "esc", "q":
			return m, func() tea.Msg { return BackMsg{} }
		}
	}

	var cmd tea.Cmd
	m.picker, cmd = m.picker.Update(msg)
	if picked, path := m.picker.DidSelectFile(msg); picked {
		return m, m.consume(path)
	}
	if picked, path := m.picker.DidSelectDisabledFile(msg); picked {
		m.err = "cannot read " + filepath.Base(path)
	}
	return m, cmd
}

// consume reads the file, decodes the portable bundle, and hands it to
// Service.ImportPortable. Whatever happens, we leave the screen — the
// list view picks up the outcome through FlashMsg, which Root drops in
// after switching screens.
func (m *Model) consume(path string) tea.Cmd {
	data, err := os.ReadFile(path)
	if err != nil {
		return chain(
			func() tea.Msg { return BackMsg{} },
			func() tea.Msg { return list.FlashMsg{Text: "read failed: " + err.Error(), IsError: true} },
		)
	}
	p, err := app.UnmarshalPortable(data)
	if err != nil {
		return chain(
			func() tea.Msg { return BackMsg{} },
			func() tea.Msg { return list.FlashMsg{Text: "invalid bundle: " + err.Error(), IsError: true} },
		)
	}
	newPath, err := m.svc.ImportPortable(p)
	if err != nil {
		return chain(
			func() tea.Msg { return BackMsg{} },
			func() tea.Msg { return list.FlashMsg{Text: "import failed: " + err.Error(), IsError: true} },
		)
	}
	msg := fmt.Sprintf("✓ imported %s", p.Name)
	_ = newPath
	return chain(
		func() tea.Msg { return BackMsg{} },
		func() tea.Msg { return list.FlashMsg{Text: msg} },
	)
}

// chain emits a series of messages back-to-back. Bubble Tea's Batch is
// concurrent — for an "switch screens then deliver flash" sequence we
// need ordering, so Sequence is the right primitive.
func chain(cmds ...func() tea.Msg) tea.Cmd {
	tcs := make([]tea.Cmd, len(cmds))
	for i, c := range cmds {
		tcs[i] = c
	}
	return tea.Sequence(tcs...)
}

func (m *Model) View() string {
	if m.width == 0 {
		return "loading…"
	}
	header := components.HeaderBar("ovpn3", "import profile",
		[]string{components.Pill("portable", theme.Pink, theme.PinkSoft)}, m.width)
	hint := lipgloss.NewStyle().Foreground(theme.FgDim).Render(
		"pick a .o3ui.json bundle — credentials and TOTP secret will be restored to the keyring.")
	body := components.Box{
		Title:       theme.AccentPink.Render("› ") + "files",
		Content:     m.picker.View(),
		Width:       m.width - 4,
		BorderColor: theme.BorderLt,
	}.Render()
	help := components.HelpBar([]components.KeyHelp{
		{Key: "↑↓", Label: "nav"},
		{Key: "⏎", Label: "open / select"},
		{Key: "esc", Label: "back"},
	}, m.width)
	pieces := []string{header, hint, body}
	if m.err != "" {
		pieces = append(pieces, lipgloss.NewStyle().Foreground(theme.Red).Render(m.err))
	}
	pieces = append(pieces, help)
	return lipgloss.JoinVertical(lipgloss.Left, pieces...)
}
