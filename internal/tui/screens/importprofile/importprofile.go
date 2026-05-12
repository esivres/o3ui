// Package importprofile is a single-screen UI for picking a profile
// off disk. Accepts two formats and sniffs by content:
//
//   - portable `.o3ui.json` bundle  → Service.ImportPortable
//     (round-trip with overlay + credentials + TOTP secret)
//   - raw `.ovpn` / `.conf` config  → Service.ImportFromFile
//     (the classic openvpn3 config-import flow)
//
// One screen, two file types — the user shouldn't have to remember
// which key opens which import flow.
package importprofile

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

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

// consume reads the file, sniffs the format, and dispatches to the
// right importer. We leave the screen regardless of outcome — the
// list view picks up the result via FlashMsg.
//
// Sniffing rules:
//   - JSON with `"version"` and `"config"` keys → portable bundle.
//   - anything else → raw .ovpn body (openvpn3's importer accepts
//     any text and will mark it invalid if it isn't a config).
func (m *Model) consume(path string) tea.Cmd {
	data, err := os.ReadFile(path)
	if err != nil {
		return backWithFlash("read failed: "+err.Error(), true)
	}
	if isPortableBundle(data) {
		p, err := app.UnmarshalPortable(data)
		if err != nil {
			return backWithFlash("invalid bundle: "+err.Error(), true)
		}
		if _, err := m.svc.ImportPortable(p); err != nil {
			return backWithFlash("import failed: "+err.Error(), true)
		}
		return backWithFlash(fmt.Sprintf("✓ imported %s (portable)", p.Name), false)
	}
	// Raw .ovpn — derive a profile name from the filename, sans ext.
	name := strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
	if name == "" {
		name = "imported"
	}
	if _, err := m.svc.ImportFromFile(name, path); err != nil {
		return backWithFlash("import failed: "+err.Error(), true)
	}
	return backWithFlash(fmt.Sprintf("✓ imported %s (.ovpn)", name), false)
}

func backWithFlash(text string, isErr bool) tea.Cmd {
	return chain(
		func() tea.Msg { return BackMsg{} },
		func() tea.Msg { return list.FlashMsg{Text: text, IsError: isErr} },
	)
}

// isPortableBundle does a fast structural sniff without committing to
// a full parse — JSON files that aren't our bundle should fall through
// to the raw-ovpn path instead of raising "missing version" errors.
func isPortableBundle(data []byte) bool {
	trimmed := strings.TrimLeft(string(data), " \t\r\n")
	if !strings.HasPrefix(trimmed, "{") {
		return false
	}
	var probe struct {
		Version int    `json:"version"`
		Config  string `json:"config"`
	}
	if err := json.Unmarshal(data, &probe); err != nil {
		return false
	}
	return probe.Version > 0 && probe.Config != ""
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
		[]string{components.Pill(".ovpn / .o3ui.json", theme.Pink, theme.PinkSoft)}, m.width)
	hint := lipgloss.NewStyle().Foreground(theme.FgDim).Render(
		"pick a raw .ovpn config or a .o3ui.json portable bundle — format detected automatically.")
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
