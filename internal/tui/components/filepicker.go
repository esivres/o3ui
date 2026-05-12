package components

import (
	"os"
	"path/filepath"
	"sort"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/esivres/openvpn3ui/internal/tui/theme"
)

// FilePicker is a small, opinionated file browser. The bubbles version
// is fine when all you need is "pick a file" with extension whitelist,
// but it has no substring search and no toggle to widen the filter at
// runtime. Both screens that import files (otpimport for QR, the
// portable-profile picker) wanted those affordances; rolling our own
// kept the code shorter than wrapping bubbles in tricks would.
//
// Behaviour:
//
//   - ↑/↓ or j/k             navigate
//   - Enter                  open directory / select file
//   - h or backspace         go up one directory
//   - Tab                    cycle the active extension filter
//   - /                      start typing a substring filter
//   - Esc (in filter mode)   close the input (filter text kept)
//   - Esc (otherwise)        FilePicker doesn't own it; the host
//     screen decides what Esc means
//
// FilterCycle is the host-supplied list of named filters. nil/empty
// disables Tab cycling entirely. Each filter is a predicate plus a
// short label shown in the header pill.
type FilePicker struct {
	cwd     string
	entries []entry

	cursor    int
	filter    string // substring, applied to entry.Name()
	filtMode  bool
	cycleIdx  int // index into FilterCycle, 0 always means "all"
	cycle     []FilePickerFilter
	height    int // visible rows
	width     int
	err       string
	allowDirs bool
	picked    string // last selected file path; consumed by host
}

// FilePickerFilter describes a named subset of files. Label appears in
// the header pill. Match returns true for entries to keep; nil Match
// means "everything passes" (used by the first 'all files' slot).
type FilePickerFilter struct {
	Label string
	Match func(e os.DirEntry) bool
}

type entry struct {
	os.DirEntry
	abs string
}

// NewFilePicker constructs a picker starting at the given directory.
// If start is empty, falls back to CWD then $HOME. cycle defines the
// extension filter Tab cycles through; pass nil to disable Tab.
func NewFilePicker(start string, cycle []FilePickerFilter) *FilePicker {
	if start == "" {
		if cwd, err := os.Getwd(); err == nil {
			start = cwd
		} else if home, err := os.UserHomeDir(); err == nil {
			start = home
		} else {
			start = "/"
		}
	}
	p := &FilePicker{cwd: start, cycle: cycle, height: 16, allowDirs: true}
	p.readDir()
	return p
}

// SetSize fits the picker into a viewport. The host normally hands it
// (totalHeight - chrome) so the picker can show as many rows as fit.
func (p *FilePicker) SetSize(w, h int) {
	p.width = w
	p.height = h
	if p.height < 4 {
		p.height = 4
	}
}

// CurrentDir returns the directory the picker is currently showing,
// useful for breadcrumb display.
func (p *FilePicker) CurrentDir() string { return p.cwd }

// Picked returns the path of the last file the user selected (Enter
// on a file), then clears it. Hosts call this from Update to consume
// the selection.
func (p *FilePicker) Picked() string {
	v := p.picked
	p.picked = ""
	return v
}

// FilterMode reports whether the substring-input strip is currently
// open; hosts may want to disable their own key handling while it is.
func (p *FilePicker) FilterMode() bool { return p.filtMode }

func (p *FilePicker) readDir() {
	p.err = ""
	raw, err := os.ReadDir(p.cwd)
	if err != nil {
		p.err = err.Error()
		p.entries = nil
		return
	}
	out := make([]entry, 0, len(raw))
	for _, e := range raw {
		// Hide dotfiles unconditionally — `Show hidden` would be a
		// separate axis we don't need yet.
		if strings.HasPrefix(e.Name(), ".") {
			continue
		}
		out = append(out, entry{DirEntry: e, abs: filepath.Join(p.cwd, e.Name())})
	}
	sort.Slice(out, func(i, j int) bool {
		// Dirs first, then alphabetical — what every other file
		// browser does.
		if out[i].IsDir() != out[j].IsDir() {
			return out[i].IsDir()
		}
		return strings.ToLower(out[i].Name()) < strings.ToLower(out[j].Name())
	})
	p.entries = out
	p.cursor = 0
}

// visible returns the indices into entries that pass both the
// substring filter and the active extension filter.
func (p *FilePicker) visible() []int {
	needle := strings.ToLower(p.filter)
	var f *FilePickerFilter
	if p.cycleIdx > 0 && p.cycleIdx < len(p.cycle) {
		c := p.cycle[p.cycleIdx]
		f = &c
	}
	out := []int{}
	for i, e := range p.entries {
		if needle != "" && !strings.Contains(strings.ToLower(e.Name()), needle) {
			continue
		}
		// Directory entries always show — descending into a folder is
		// always a valid action regardless of filter.
		if f != nil && !e.IsDir() && f.Match != nil && !f.Match(e) {
			continue
		}
		out = append(out, i)
	}
	return out
}

// Update returns the new picker model plus an optional Cmd. The host
// passes every tea.Msg through; we consume keys and ignore the rest.
func (p *FilePicker) Update(msg tea.Msg) (*FilePicker, tea.Cmd) {
	k, ok := msg.(tea.KeyMsg)
	if !ok {
		return p, nil
	}
	if p.filtMode {
		switch k.String() {
		case "esc":
			p.filtMode = false
		case "enter":
			// Treat Enter as "leave filter mode and act on cursor".
			// Saves a keystroke versus Esc-then-Enter.
			p.filtMode = false
			p.activateCursor()
		case "backspace":
			if r := []rune(p.filter); len(r) > 0 {
				p.filter = string(r[:len(r)-1])
				p.cursor = 0
			}
		case "ctrl+u":
			p.filter = ""
			p.cursor = 0
		default:
			if s := k.String(); len(s) == 1 {
				p.filter += s
				p.cursor = 0
			}
		}
		return p, nil
	}

	switch k.String() {
	case "up", "k":
		if p.cursor > 0 {
			p.cursor--
		}
	case "down", "j":
		if vis := p.visible(); p.cursor < len(vis)-1 {
			p.cursor++
		}
	case "enter":
		p.activateCursor()
	case "h", "backspace":
		// Up one directory. If we're at /, no-op.
		parent := filepath.Dir(p.cwd)
		if parent != "" && parent != p.cwd {
			p.cwd = parent
			p.readDir()
		}
	case "tab":
		if len(p.cycle) > 1 {
			p.cycleIdx = (p.cycleIdx + 1) % len(p.cycle)
			p.cursor = 0
		}
	case "/":
		p.filtMode = true
	}
	return p, nil
}

func (p *FilePicker) activateCursor() {
	vis := p.visible()
	if p.cursor < 0 || p.cursor >= len(vis) {
		return
	}
	e := p.entries[vis[p.cursor]]
	if e.IsDir() {
		p.cwd = e.abs
		p.readDir()
		return
	}
	p.picked = e.abs
}

// View renders the picker: breadcrumb header, list of rows, footer
// hints. The host wraps it in whatever Box / borders it likes.
func (p *FilePicker) View() string {
	vis := p.visible()

	// Header line: filter pill + path.
	pills := []string{}
	if p.cycleIdx >= 0 && p.cycleIdx < len(p.cycle) {
		pills = append(pills, Pill(p.cycle[p.cycleIdx].Label, theme.Bg, theme.Purple))
	}
	if p.filter != "" {
		pills = append(pills, Pill("/"+p.filter, theme.Bg, theme.Pink))
	}
	head := lipgloss.JoinHorizontal(lipgloss.Top, pills...) +
		"  " + theme.Subtle.Render(p.cwd)

	rows := make([]string, 0, p.height)
	if p.err != "" {
		rows = append(rows, theme.AccentRed.Render("error: "+p.err))
	}
	// Window the visible slice around the cursor so long lists scroll.
	rowsAvail := p.height - 3
	if rowsAvail < 3 {
		rowsAvail = 3
	}
	start := 0
	if p.cursor > rowsAvail-1 {
		start = p.cursor - (rowsAvail - 1)
	}
	end := start + rowsAvail
	if end > len(vis) {
		end = len(vis)
	}
	for i := start; i < end; i++ {
		e := p.entries[vis[i]]
		marker := "  "
		nameStyle := lipgloss.NewStyle().Foreground(theme.Fg)
		if i == p.cursor {
			marker = theme.AccentPink.Render("› ")
			nameStyle = lipgloss.NewStyle().Foreground(theme.FgBright).Bold(true)
		}
		name := e.Name()
		if e.IsDir() {
			name += "/"
			nameStyle = nameStyle.Foreground(theme.Cyan)
		}
		rows = append(rows, marker+nameStyle.Render(name))
	}
	if len(vis) == 0 && p.err == "" {
		rows = append(rows, theme.Dim.Render("(no matches)"))
	}

	footer := theme.Subtle.Render(
		"↑↓ nav · ⏎ open/select · h up · tab filter · / search · esc back",
	)
	if p.filtMode {
		footer = theme.AccentPink.Render("/"+p.filter+"_") +
			"  " + theme.Subtle.Render("esc close · ⌫ delete · ctrl+u clear")
	}
	return lipgloss.JoinVertical(lipgloss.Left, head, "", strings.Join(rows, "\n"), "", footer)
}
