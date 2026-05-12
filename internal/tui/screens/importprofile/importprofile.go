// Package importprofile is a single-screen UI for picking a profile
// off disk. Accepts two formats and sniffs by content:
//
//   - portable `.o3ui.json` bundle  → Service.ImportPortable
//     (round-trip with overlay + credentials + TOTP secret)
//   - raw `.ovpn` / `.conf` config  → Service.ImportFromFile
//     (the classic openvpn3 config-import flow)
//
// Uses our own components.FilePicker rather than bubbles/filepicker so
// the user gets `Tab` to cycle the extension filter and `/` for
// substring search — two affordances bubbles' picker doesn't offer.
package importprofile

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

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

	picker *components.FilePicker
}

func New(svc *app.Service) *Model {
	// Filter cycle: "all" is always the first entry so Tab can wrap
	// back to it; the other two preselect the formats this screen
	// knows how to consume, which is the 99% case for the user.
	cycle := []components.FilePickerFilter{
		{Label: "all files"},
		{
			Label: ".ovpn / .conf",
			Match: func(e os.DirEntry) bool {
				ext := strings.ToLower(filepath.Ext(e.Name()))
				return ext == ".ovpn" || ext == ".conf"
			},
		},
		{
			Label: ".o3ui.json",
			Match: func(e os.DirEntry) bool {
				return strings.HasSuffix(strings.ToLower(e.Name()), ".o3ui.json")
			},
		},
	}
	start, _ := os.Getwd()
	if start == "" {
		start, _ = os.UserHomeDir()
	}
	return &Model{svc: svc, picker: components.NewFilePicker(start, cycle)}
}

func (m *Model) Init() tea.Cmd { return nil }

// HelpKeys feeds the `?` overlay.
func (m *Model) HelpKeys() []components.KeyHelp {
	return []components.KeyHelp{
		{Key: "↑↓", Label: "browse files"},
		{Key: "⏎", Label: "open dir / select file"},
		{Key: "h / backspace", Label: "parent dir"},
		{Key: "tab", Label: "cycle extension filter"},
		{Key: "/", Label: "substring search"},
		{Key: "esc", Label: "back"},
	}
}

func (m *Model) SetSize(w, h int) {
	m.width, m.height = w, h
	ph := h - 10
	if ph < 8 {
		ph = 8
	}
	m.picker.SetSize(w-4, ph)
}

func (m *Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.SetSize(msg.Width, msg.Height)
	case tea.KeyMsg:
		// Esc is ours only when the picker isn't actively swallowing
		// keys for its substring-filter input; otherwise we'd
		// short-circuit the filter close.
		if msg.String() == "esc" && !m.picker.FilterMode() {
			return m, func() tea.Msg { return BackMsg{} }
		}
	}
	var cmd tea.Cmd
	m.picker, cmd = m.picker.Update(msg)
	if path := m.picker.Picked(); path != "" {
		return m, m.consume(path)
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
		{Key: "/", Label: "search"},
		{Key: "tab", Label: "filter"},
		{Key: "esc", Label: "back"},
	}, m.width)
	return lipgloss.JoinVertical(lipgloss.Left, header, hint, body, help)
}
