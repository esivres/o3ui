// Package components — palette is a single-line fuzzy command launcher
// shown as a floating overlay on top of any screen. The Root owns the
// palette state; this file provides the rendering and key handling for
// one instance of it.
//
// Design follows the lazygit / vscode / gh dash idiom: `:` or `Ctrl+P`
// pops it open, the user types a few characters, the best match floats
// to the top, Enter fires the bound Cmd. No screen transition — closing
// the palette returns the user exactly where they were.
package components

import (
	"strings"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/sahilm/fuzzy"

	"github.com/esivres/openvpn3ui/internal/tui/theme"
)

// PaletteItem is one command the user can pick. Title is what the fuzzy
// matcher scores against; Detail is the dim right-side hint (e.g.
// profile country, action category). Run is the Cmd Root executes when
// the user confirms.
type PaletteItem struct {
	Title  string
	Detail string
	Run    tea.Cmd
}

// PalettePickMsg fires when the user confirms a selection. Root catches
// it, closes the palette, and dispatches Run.
type PalettePickMsg struct{ Run tea.Cmd }

// PaletteCancelMsg fires on Esc.
type PaletteCancelMsg struct{}

// Palette is the floating command-launcher model.
type Palette struct {
	width  int
	height int

	items []PaletteItem
	input textinput.Model

	// filtered indexes into items. Recomputed on every input change.
	// Cursor is over filtered, not items.
	filtered []int
	cursor   int
}

// NewPalette creates an open palette pre-populated with the given items.
// Caller sets size separately so the same component can be reused on
// re-open without rebuilding from scratch.
func NewPalette(items []PaletteItem) *Palette {
	ti := textinput.New()
	ti.Prompt = ""
	ti.Placeholder = "type a command…"
	ti.CharLimit = 128
	ti.Width = 48
	ti.Focus()

	p := &Palette{items: items, input: ti}
	p.recompute()
	return p
}

// SetSize stores terminal dimensions for centring on render.
func (p *Palette) SetSize(w, h int) { p.width, p.height = w, h }

// HelpKeys feeds the `?` overlay if the palette is open.
func (p *Palette) HelpKeys() []KeyHelp {
	return []KeyHelp{
		{Key: "↑↓", Label: "navigate"},
		{Key: "⏎", Label: "run selected command"},
		{Key: "esc", Label: "close palette"},
	}
}

// Update advances the internal state. Returns the (possibly mutated)
// palette plus a tea.Cmd. On confirm/cancel the cmd carries a
// PalettePickMsg / PaletteCancelMsg that Root catches at the top level.
func (p *Palette) Update(msg tea.Msg) (*Palette, tea.Cmd) {
	if km, ok := msg.(tea.KeyMsg); ok {
		switch km.String() {
		case "esc":
			return p, func() tea.Msg { return PaletteCancelMsg{} }
		case "enter":
			if len(p.filtered) == 0 {
				return p, nil
			}
			it := p.items[p.filtered[p.cursor]]
			return p, func() tea.Msg { return PalettePickMsg{Run: it.Run} }
		case "up", "ctrl+k":
			if p.cursor > 0 {
				p.cursor--
			}
			return p, nil
		case "down", "ctrl+j":
			if p.cursor+1 < len(p.filtered) {
				p.cursor++
			}
			return p, nil
		}
	}
	prev := p.input.Value()
	var cmd tea.Cmd
	p.input, cmd = p.input.Update(msg)
	if p.input.Value() != prev {
		p.recompute()
	}
	return p, cmd
}

// recompute applies the current query against items via sahilm/fuzzy.
// Empty query falls back to "all items, original order" — gives the
// palette a useful initial state (the user sees their full vocabulary
// before they type a single character).
func (p *Palette) recompute() {
	q := strings.TrimSpace(p.input.Value())
	if q == "" {
		p.filtered = p.filtered[:0]
		for i := range p.items {
			p.filtered = append(p.filtered, i)
		}
		p.cursor = 0
		return
	}
	matches := fuzzy.FindFrom(q, paletteSource(p.items))
	p.filtered = p.filtered[:0]
	for _, m := range matches {
		p.filtered = append(p.filtered, m.Index)
	}
	p.cursor = 0
}

// paletteSource adapts []PaletteItem to fuzzy.Source by indexing into
// the slice — fuzzy scores against the Title of each item.
type paletteSource []PaletteItem

func (s paletteSource) String(i int) string { return s[i].Title }
func (s paletteSource) Len() int            { return len(s) }

// View renders the floating card. Caller positions it via
// lipgloss.Place once it has measured surrounding chrome — keeping the
// raw card here makes it easy to embed elsewhere later.
func (p *Palette) View() string {
	width := 60
	if p.width > 0 && p.width-8 < width {
		width = p.width - 8
	}
	if width < 30 {
		width = 30
	}

	header := lipgloss.NewStyle().
		Background(theme.Purple).
		Foreground(theme.FgBright).
		Bold(true).
		Padding(0, 2).
		Render("⌘ Command palette")

	field := lipgloss.NewStyle().
		Background(theme.Surface).
		Foreground(theme.Fg).
		Border(lipgloss.RoundedBorder()).
		BorderForeground(theme.Pink).
		Padding(0, 1).
		Width(width).
		Render(p.input.View())

	body := []string{header, "", field}

	if len(p.filtered) == 0 {
		body = append(body, "", theme.Subtle.Render("no matches"))
	} else {
		const visible = 8
		start := 0
		if p.cursor >= visible {
			start = p.cursor - visible + 1
		}
		end := start + visible
		if end > len(p.filtered) {
			end = len(p.filtered)
		}
		for i := start; i < end; i++ {
			it := p.items[p.filtered[i]]
			body = append(body, paletteRow(it, i == p.cursor, width))
		}
	}

	body = append(body, "",
		theme.Subtle.Render("⏎ run · esc close · ↑↓ navigate"),
	)

	return lipgloss.NewStyle().
		Padding(1, 2).
		Border(lipgloss.RoundedBorder()).
		BorderForeground(theme.Purple).
		Background(theme.Panel).
		Render(strings.Join(body, "\n"))
}

func paletteRow(it PaletteItem, selected bool, width int) string {
	title := it.Title
	detail := it.Detail
	titleStyle := lipgloss.NewStyle().Foreground(theme.Fg)
	detailStyle := lipgloss.NewStyle().Foreground(theme.FgDim)
	marker := "  "
	if selected {
		marker = theme.AccentPink.Render("▎ ")
		titleStyle = titleStyle.Foreground(theme.FgBright).Bold(true)
	}
	// Right-align detail when there's room; otherwise truncate title.
	titleW := width - lipgloss.Width(detail) - 4
	if titleW < 8 {
		titleW = 8
	}
	titleCell := lipgloss.NewStyle().Width(titleW).Render(titleStyle.Render(truncate(title, titleW)))
	return marker + titleCell + " " + detailStyle.Render(detail)
}

func truncate(s string, w int) string {
	if w <= 0 {
		return ""
	}
	if lipgloss.Width(s) <= w {
		return s
	}
	if w < 2 {
		return strings.Repeat(".", w)
	}
	return s[:w-1] + "…"
}
