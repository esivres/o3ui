// Package list implements the Profile list screen — the entry point of
// the TUI. Left column lists configurations; right column shows details
// of the highlighted profile.
package list

import (
	"fmt"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/esivres/openvpn3ui/internal/app"
	"github.com/esivres/openvpn3ui/internal/overlay"
	"github.com/esivres/openvpn3ui/internal/ovpn"
	"github.com/esivres/openvpn3ui/internal/tui/components"
	"github.com/esivres/openvpn3ui/internal/tui/theme"
)

// Item is a flat projection of (config + overlay + active-session-state)
// for the list — keeps the row renderer free of D-Bus types.
type Item struct {
	ConfigPath  string
	Name        string
	CountryCode string
	Favorite    bool
	HasSession  bool
	LastUsed    time.Time
}

// reloadMsg is fired when we want the list to refetch from the service.
type reloadMsg struct {
	items []Item
	err   error
}

// ActionMsg is broadcast when the user picks an action on the list. Root
// catches it in its Update() and switches to the appropriate screen.
// Idiomatic Bubble Tea — no custom polling; the message reaches the root
// because Bubble Tea hands every Cmd's output to Root first.
type ActionMsg struct {
	Kind string // "connect" | "disconnect" | "edit" | "import" | "stats"
	Item Item
}

// Model is the bubbletea model for the list screen.
type Model struct {
	svc    *app.Service
	width  int
	height int

	items    []Item
	cursor   int
	loadErr  error
	filter   string // current filter input
	filtMode bool   // are we typing into the filter?

	// renameMode swallows printable keys and routes them into
	// renameDraft until the user confirms with Enter or aborts with
	// Esc. Mirrors filtMode rather than pulling in a full textinput
	// model — one-line rename doesn't justify the extra widget.
	renameMode   bool
	renameDraft  string
	renamingPath string // config path being renamed

	// flash is a transient status line shown above the help bar — used
	// by Root to surface results of cross-screen actions (export
	// produced a file, import restored a profile). flashUntil controls
	// when the message fades; bubbletea has no built-in toast so we
	// schedule a clear-tick.
	flash      string
	flashErr   bool
	flashUntil time.Time
}

// FlashMsg lets Root push a status update into the list view — the
// "exported to /home/…" line shown briefly below the table.
type FlashMsg struct {
	Text    string
	IsError bool
}

type flashClearMsg struct{}

func New(svc *app.Service) *Model { return &Model{svc: svc} }

// HelpKeys is what the ? overlay shows for this screen. Mirrors the
// switch in Update so the two can't drift apart silently.
func (m *Model) HelpKeys() []components.KeyHelp {
	return []components.KeyHelp{
		{Key: "/", Label: "filter (Esc closes mode, Ctrl+U clears)"},
		{Key: "d", Label: "disconnect active (confirm)"},
		{Key: "e", Label: "edit profile"},
		{Key: "f", Label: "toggle favorite"},
		{Key: "i", Label: "import .ovpn or .o3ui.json"},
		{Key: "X", Label: "export profile → JSON (confirm)"},
		{Key: "R", Label: "rename profile"},
		{Key: "D", Label: "delete profile (confirm)"},
		{Key: ",", Label: "settings"},
		{Key: "r", Label: "reload"},
	}
}

// Init is bubbletea's first-tick. We kick off a load so the screen is not
// blank on first render.
func (m *Model) Init() tea.Cmd { return m.loadCmd() }

func (m *Model) loadCmd() tea.Cmd {
	return func() tea.Msg { return m.fetch() }
}

func (m *Model) fetch() reloadMsg {
	cfgs, err := m.svc.ListConfigs()
	if err != nil {
		return reloadMsg{err: err}
	}
	active, _ := m.svc.ActiveSessions()
	hasSession := map[string]bool{}
	// Index-based — Session is ~128B and we read only one field.
	for i := range active {
		hasSession[active[i].ConfigPath] = true
	}
	items := make([]Item, 0, len(cfgs))
	for _, c := range cfgs {
		items = append(items, m.itemFor(c, hasSession[c.Path]))
	}
	return reloadMsg{items: items}
}

func (m *Model) itemFor(c ovpn.Config, hasSess bool) Item {
	it := Item{ConfigPath: c.Path, Name: c.Name, HasSession: hasSess}
	if o, ok := m.svc.GetOverlay(c.Path); ok {
		it.CountryCode = o.CountryCode
		it.Favorite = o.Favorite
		it.LastUsed = o.LastConnectedAt
	} else {
		_ = overlay.Overlay{} // keep import live for diff-readability
	}
	return it
}

// SetSize is called by the root model on every WindowSizeMsg.
func (m *Model) SetSize(w, h int) { m.width, m.height = w, h }

// emitCmd packages a user action into a tea.Cmd that yields ActionMsg.
// Returns nil when the cursor is invalid so callers can chain it safely.
func (m *Model) emitCmd(kind string) tea.Cmd {
	it := m.currentItem()
	if it == nil {
		return nil
	}
	cur := *it
	return func() tea.Msg { return ActionMsg{Kind: kind, Item: cur} }
}

func (m *Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.SetSize(msg.Width, msg.Height)

	case reloadMsg:
		// Preserve the user's selection across realtime reloads:
		// remember the ConfigPath at the cursor, refresh the list,
		// then put the cursor back on the same row. Without this, a
		// signal arriving while a profile is deleted elsewhere
		// clamps the cursor to the last row and yanks the user out
		// of context for no reason.
		var selected string
		if cur := m.currentItem(); cur != nil {
			selected = cur.ConfigPath
		}
		m.items = msg.items
		m.loadErr = msg.err
		if selected != "" {
			vis := m.visible()
			for i, idx := range vis {
				if m.items[idx].ConfigPath == selected {
					m.cursor = i
					break
				}
			}
		}
		if m.cursor >= len(m.items) {
			m.cursor = len(m.items) - 1
		}
		if m.cursor < 0 {
			m.cursor = 0
		}

	// Real-time refresh on D-Bus signals — no need for the user to press 'r'.
	case ovpn.SessionCreatedEvent, ovpn.SessionDestroyedEvent,
		ovpn.ConfigCreatedEvent, ovpn.ConfigDestroyedEvent,
		ovpn.StatusChangeEvent:
		return m, m.loadCmd()

	case FlashMsg:
		m.flash = msg.Text
		m.flashErr = msg.IsError
		m.flashUntil = time.Now().Add(6 * time.Second)
		return m, tea.Tick(6*time.Second, func(time.Time) tea.Msg {
			return flashClearMsg{}
		})

	case flashClearMsg:
		if time.Now().After(m.flashUntil) {
			m.flash = ""
			m.flashErr = false
		}
		return m, nil

	case tea.KeyMsg:
		// Filter input mode swallows printable keys until Esc/Enter.
		// Esc only leaves the input mode — the filter itself stays so
		// the user can aim the cursor with the keys still narrowing
		// the view. Second Esc (or Ctrl+U) clears the buffer. fzf and
		// ripgrep work the same way; clearing on first Esc as an
		// earlier version did broke muscle memory.
		if m.filtMode {
			switch msg.String() {
			case "esc":
				m.filtMode = false
			case "ctrl+u":
				m.filter = ""
			case "enter":
				m.filtMode = false
			case "backspace":
				if len(m.filter) > 0 {
					m.filter = m.filter[:len(m.filter)-1]
				}
			default:
				if k := msg.String(); len(k) == 1 {
					m.filter += k
				}
			}
			return m, nil
		}

		// Rename input mode — same shape as filtMode, different sink.
		if m.renameMode {
			switch msg.String() {
			case "esc":
				m.renameMode = false
				m.renameDraft = ""
				m.renamingPath = ""
			case "enter":
				newName := strings.TrimSpace(m.renameDraft)
				path := m.renamingPath
				m.renameMode = false
				m.renameDraft = ""
				m.renamingPath = ""
				if newName == "" || path == "" {
					return m, nil
				}
				if err := m.svc.RenameConfig(path, newName); err != nil {
					return m, func() tea.Msg {
						return FlashMsg{Text: "rename failed: " + err.Error(), IsError: true}
					}
				}
				return m, tea.Batch(
					m.loadCmd(),
					func() tea.Msg { return FlashMsg{Text: "✓ renamed to " + newName} },
				)
			case "backspace":
				if len(m.renameDraft) > 0 {
					r := []rune(m.renameDraft)
					m.renameDraft = string(r[:len(r)-1])
				}
			default:
				if k := msg.String(); len(k) >= 1 && k != "left" && k != "right" && k != "up" && k != "down" {
					m.renameDraft += k
				}
			}
			return m, nil
		}

		switch msg.String() {
		case "up", "k":
			if m.cursor > 0 {
				m.cursor--
			}
		case "down", "j":
			if m.cursor < len(m.visible())-1 {
				m.cursor++
			}
		case "/":
			m.filtMode = true
		case "enter":
			it := m.currentItem()
			if it == nil {
				break
			}
			// Live session: enter opens the Connected screen (live stats);
			// d disconnects. Idle profile: enter starts the connect flow.
			kind := "connect"
			if it.HasSession {
				kind = "view"
			}
			return m, m.emitCmd(kind)
		case "d":
			it := m.currentItem()
			if it != nil && it.HasSession {
				return m, m.emitCmd("disconnect")
			}
		case "e":
			return m, m.emitCmd("edit")
		case "i":
			// Import doesn't act on the current row — it opens a
			// filepicker. Must fire even on an empty profile list,
			// so we bypass emitCmd's "needs a selection" guard.
			return m, func() tea.Msg { return ActionMsg{Kind: "import"} }
		case "X":
			// Capital X — destructive in spirit (writes a file
			// containing credentials), so we want a key that needs
			// deliberate Shift to hit. Lowercase x is reserved for
			// future "remove" semantics in the same spirit.
			return m, m.emitCmd("export")
		case "R":
			// Inline rename — prefill with the current name so users
			// can patch a typo without retyping the whole thing. Shift
			// to make this a deliberate action.
			if it := m.currentItem(); it != nil {
				m.renameMode = true
				m.renameDraft = it.Name
				m.renamingPath = it.ConfigPath
			}
		case "D":
			// Shift-D — destructive, hence the capital. Lowercase d is
			// the disconnect verb; pairing rename / delete on R / D
			// keeps "edit the row" affordances in one fingering.
			return m, m.emitCmd("delete")
		// `s` (stats) was a dead key — Root had no handler. Removed
		// rather than wired to something speculative; we'll add it
		// back when there's a real stats screen to point at.
		case ",":
			return m, func() tea.Msg { return ActionMsg{Kind: "settings"} }
		case "f":
			// Toggle favorite. Best-effort; ignore overlay errors so the
			// UI stays responsive even on a half-broken DB.
			if it := m.currentItem(); it != nil {
				_ = m.svc.SetFavorite(it.ConfigPath, !it.Favorite)
				return m, m.loadCmd()
			}
		case "r":
			return m, m.loadCmd()
		case "q":
			// Root only catches Ctrl+C; the list is the home screen, so
			// it owns the user-friendly "press q to quit" affordance.
			// But: if a tunnel is up, dropping out without a sanity
			// check is the kind of thing a user does once and then has
			// to figure out why their VPN is still running. Emit a
			// confirm ActionMsg — Root has the modal primitive, list
			// doesn't need its own.
			for i := range m.items {
				if m.items[i].HasSession {
					return m, func() tea.Msg { return ActionMsg{Kind: "quit-confirm"} }
				}
			}
			return m, tea.Quit
		}
	}
	return m, nil
}

func (m *Model) currentItem() *Item {
	v := m.visible()
	if m.cursor < 0 || m.cursor >= len(v) {
		return nil
	}
	idx := v[m.cursor]
	return &m.items[idx]
}

// visible returns the indices into m.items that match the current filter.
func (m *Model) visible() []int {
	if m.filter == "" {
		out := make([]int, len(m.items))
		for i := range m.items {
			out[i] = i
		}
		return out
	}
	needle := strings.ToLower(m.filter)
	var out []int
	for i, it := range m.items {
		if strings.Contains(strings.ToLower(it.Name), needle) {
			out = append(out, i)
		}
	}
	return out
}

func (m *Model) View() string {
	if m.width == 0 || m.height == 0 {
		return "loading…"
	}
	header := components.HeaderBar("ovpn3", "profiles", m.headerPills(), m.width)

	// Footer keeps only essentials; the `?` overlay (handled at Root)
	// surfaces the full key map. This is what fixes the wrap at ~80
	// columns — wide glyphs (↑↓, ⏎) made every "extra" item a risk.
	help := components.HelpBar([]components.KeyHelp{
		{Key: "↑↓", Label: "nav"},
		{Key: "⏎", Label: "open"},
		{Key: "/", Label: "find"},
		{Key: "?", Label: "help"},
		{Key: "q", Label: "quit"},
	}, m.width)

	// No empty spacer rows — every visible cell is accounted for. Boxes
	// provide their own visual gap via the rounded borders.
	headerH := lipgloss.Height(header)
	helpH := lipgloss.Height(help)
	bodyH := m.height - headerH - helpH
	if bodyH < 12 {
		bodyH = 12
	}

	leftW := m.width * 6 / 10
	rightW := m.width - leftW - 2

	// Box.Height is the *content* area; lipgloss adds top + bottom
	// border rows on top of it. Pass bodyH-2 so the final rendered
	// box is exactly bodyH lines tall — otherwise a clip pass either
	// chops the borders off (looks like the border vanished entirely)
	// or pushes the help bar off-screen.
	innerH := bodyH - 2
	if innerH < 6 {
		innerH = 6
	}
	left := m.renderListBox(leftW, innerH)
	right := m.renderDetailBox(rightW, innerH)
	body := lipgloss.JoinHorizontal(lipgloss.Top, left, "  ", right)

	if m.flash != "" {
		color := theme.Mint
		if m.flashErr {
			color = theme.Red
		}
		flash := lipgloss.NewStyle().Foreground(color).Bold(true).Render(m.flash)
		return lipgloss.JoinVertical(lipgloss.Left, header, body, flash, help)
	}
	return lipgloss.JoinVertical(lipgloss.Left, header, body, help)
}

// renderListBox returns the Profile-list panel as a fixed-size Box so
// it consumes the full available height, matching the design.
func (m *Model) renderListBox(width, height int) string {
	title := lipgloss.JoinHorizontal(lipgloss.Top,
		theme.AccentPink.Render("› "),
		theme.Bright.Render("Profiles"),
		" ",
		theme.Dim.Render(fmt.Sprintf("· %d saved", len(m.items))),
	)

	content := m.renderListContent()
	// `height` is the box's inner content area (set as lipgloss Height).
	// Subtract 2 rows for the title and the dotted divider that Box
	// stuffs into the same content area above our content.
	innerH := height - 2
	if innerH < 1 {
		innerH = 1
	}
	content = clipLines(content, innerH)
	return components.Box{
		Title:       title,
		Content:     content,
		Width:       width,
		Height:      height,
		BorderColor: theme.BorderLt,
	}.Render()
}

// clipLines truncates s to at most n lines and pads with empty lines
// when shorter. Keeps Box rendering deterministic — overflow used to
// push the header off the top of the terminal.
func clipLines(s string, n int) string {
	lines := strings.Split(s, "\n")
	if len(lines) > n {
		lines = lines[:n]
	}
	for len(lines) < n {
		lines = append(lines, "")
	}
	return strings.Join(lines, "\n")
}

func (m *Model) renderListContent() string {
	if m.loadErr != nil {
		return theme.AccentRed.Render("error: " + m.loadErr.Error())
	}
	visible := m.visible()
	if len(visible) == 0 {
		return theme.Dim.Render("(no profiles — press i to import)")
	}

	rows := []string{m.renderColumnHeader()}
	for screenIdx, dataIdx := range visible {
		it := m.items[dataIdx]
		rows = append(rows, m.renderRow(dataIdx, it, screenIdx == m.cursor))
	}
	if m.filter != "" || m.filtMode {
		marker := theme.AccentPurple.Render("filter ›")
		val := lipgloss.NewStyle().
			Background(theme.Surface).
			Foreground(theme.FgBright).
			Padding(0, 1).
			Render(m.filter + caret(m.filtMode))
		count := components.Pill(
			fmt.Sprintf("%d/%d", len(visible), len(m.items)),
			theme.Mint, theme.Panel2,
		)
		rows = append(rows, "", marker+" "+val+"  "+theme.Subtle.Render("matches ")+count)
	}
	if m.renameMode {
		marker := theme.AccentPink.Render("rename ›")
		val := lipgloss.NewStyle().
			Background(theme.Surface).
			Foreground(theme.FgBright).
			Padding(0, 1).
			Render(m.renameDraft + caret(true))
		hint := theme.Subtle.Render("⏎ save · esc cancel")
		rows = append(rows, "", marker+" "+val+"  "+hint)
	}
	return strings.Join(rows, "\n")
}

// renderColumnHeader prints the table header row matching the design:
// gutter / status / idx / code / fav / name / auth / proto / last.
// Uses the same plain-padding construction as renderRow so widths line
// up exactly and nothing wraps to a second line. The fav slot is its
// own 1-cell column so the star never bleeds into NAME on long names.
func (m *Model) renderColumnHeader() string {
	width := m.listInnerWidth()
	cols := tableColumns(width)

	row := "  " + " " + // gutter spacer (1) + space + status spacer (1)
		" " +
		padRight("#", cols.idx) +
		padRight("CC", cols.code) +
		padRight(" ", cols.fav+1) + // fav slot + 1-space gap to NAME
		padRight("NAME", cols.name) + "  "
	if cols.auth > 0 {
		row += padRight("AUTH", cols.auth)
	}
	if cols.proto > 0 {
		row += padRight("PROTO", cols.proto)
	}
	last := "LAST"
	gap := width - visibleWidth(row) - len(last)
	if gap < 1 {
		gap = 1
	}
	row += strings.Repeat(" ", gap) + last

	return lipgloss.NewStyle().Foreground(theme.FgSubtle).Render(row)
}

// columnWidths bundles the table's fixed-width column sizes; "name" and
// "last" flex to fill the row. fav is always 1 cell, kept as its own
// slot so the favorite glyph never shifts the AUTH column.
type columnWidths struct {
	idx, code, fav, name, auth, proto, last int
}

// tableColumns picks column sizes that fit the row width while staying
// readable. Below ~64 cells we collapse the auth + proto columns. Fav
// is always present (1 cell) — its empty space costs nothing and keeps
// every row aligned with the header.
func tableColumns(rowWidth int) columnWidths {
	c := columnWidths{idx: 4, code: 3, fav: 1, auth: 9, proto: 5, last: 10}
	// Status glyph + gutter (3 cells) + 1 space after status (1) +
	// fav-to-name gap (1) = 5 fixed cells before idx column.
	const fixed = 5
	used := fixed + c.idx + c.code + c.fav + c.auth + c.proto + c.last
	if rowWidth < 64 {
		c.auth = 0
		c.proto = 0
		used = fixed + c.idx + c.code + c.fav + c.last
	}
	c.name = rowWidth - used
	if c.name < 8 {
		c.name = 8
	}
	return c
}

func caret(active bool) string {
	if active {
		return "▏"
	}
	return ""
}

// listInnerWidth is the column we have available for one row of the list
// content — outer Box width minus border (2) and padding (2) and the
// inner left/right whitespace we leave for breathing room (2).
func (m *Model) listInnerWidth() int {
	w := m.width*6/10 - 6
	if w < 40 {
		w = 40
	}
	return w
}

// renderRow takes Item by value — gocritic flags 80B as heavy, but
// rendering is dominated by the lipgloss styling that follows, not by
// the copy. Passing by pointer would invite goroutine aliasing in a
// future event-driven render path; keeping value-semantics here is
// deliberate.
//
//nolint:gocritic // hugeParam: see comment above
func (m *Model) renderRow(idx int, it Item, selected bool) string {
	width := m.listInnerWidth()
	cols := tableColumns(width)

	statusGlyph := "○"
	statusColor := theme.FgSubtle
	if it.HasSession {
		statusGlyph = "●"
		statusColor = theme.Mint
	}
	gutterRune := " "
	if selected {
		gutterRune = "▎"
	}

	codeColor := theme.FgDim
	nameColor := theme.Fg
	idxColor := theme.FgSubtle
	switch {
	case selected:
		codeColor = theme.Cyan
		nameColor = theme.FgBright
		idxColor = theme.Purple
	case it.HasSession:
		nameColor = theme.Mint
	}

	code := it.CountryCode
	if code == "" {
		code = "··"
	}

	// Favorite glyph lives in its own 1-cell column. ASCII '*' avoids
	// the wide-glyph trap that ★ falls into on emoji-fallback fonts —
	// reliably one cell across every terminal we care about.
	favGlyph := " "
	if it.Favorite {
		favGlyph = "*"
	}
	name := padRight(truncateWidth(it.Name, cols.name), cols.name)

	// Build the row as plain text columns first (manual space padding),
	// then colour each piece. Avoids the lipgloss Width-inside-Width
	// trap where ANSI inside a cell makes the outer width miscount and
	// wraps the row to a second line.
	row := padRight(gutterRune, 1) + " " +
		padRight(statusGlyph, 1) + " " +
		padRight(fmt.Sprintf("[%d]", idx), cols.idx) +
		padRight(code, cols.code) +
		padRight(favGlyph, cols.fav) + " " +
		name

	if cols.auth > 0 {
		row += padRight("—", cols.auth)
	}
	if cols.proto > 0 {
		row += padRight("—", cols.proto)
	}

	last := relTime(it.LastUsed)
	gap := width - visibleWidth(row) - len(last)
	if gap < 1 {
		gap = 1
	}
	row += strings.Repeat(" ", gap) + last

	// Per-piece colourisation by literal substitution. Each piece is
	// unique within the row, so first-occurrence Replace is safe.
	colored := colourRow(row, idx, code, it.Name, statusGlyph, gutterRune, last,
		statusColor, idxColor, codeColor, nameColor, it.Favorite)

	if selected {
		// Subdued surface lets the per-piece foreground colours stay
		// readable — the loud PinkSoft fill we used to ship buried
		// the cyan/purple/mint accents under low contrast.
		return lipgloss.NewStyle().
			Background(theme.Panel2).
			Width(width).
			Render(colored)
	}
	return colored
}

// colourRow re-applies foreground colours on top of the plain padded
// row. Replacements run first-match-only on substrings unique within
// the row, so column ordering is preserved without us having to track
// byte offsets. The favourite star (yellow ★) is painted last so its
// colour wins over the surrounding name colour.
func colourRow(row string, idx int, code, name, statusGlyph, gutter, last string,
	statusColor, idxColor, codeColor, nameColor lipgloss.Color, favorite bool) string {

	r := row
	r = strings.Replace(r, statusGlyph, lipgloss.NewStyle().Foreground(statusColor).Bold(true).Render(statusGlyph), 1)
	if gutter == "▎" {
		r = strings.Replace(r, "▎", lipgloss.NewStyle().Foreground(theme.Pink).Bold(true).Render("▎"), 1)
	}
	idxText := fmt.Sprintf("[%d]", idx)
	r = strings.Replace(r, idxText, lipgloss.NewStyle().Foreground(idxColor).Render(idxText), 1)
	r = strings.Replace(r, code, lipgloss.NewStyle().Foreground(codeColor).Render(code), 1)
	// Name may have been truncated to fit the column. strings.Replace
	// with N=1 is a no-op when the substring isn't present, so the
	// Contains-guard is redundant — same outcome, less code.
	r = strings.Replace(r, name, lipgloss.NewStyle().Foreground(nameColor).Render(name), 1)
	if favorite {
		r = strings.Replace(r, "*", theme.AccentYellow.Render("*"), 1)
	}
	r = strings.Replace(r, last, lipgloss.NewStyle().Foreground(theme.FgSubtle).Render(last), 1)
	return r
}

// padRight pads s with trailing spaces to reach width n in display cells;
// truncates with "…" if the input is wider than n.
func padRight(s string, n int) string {
	w := visibleWidth(s)
	if w == n {
		return s
	}
	if w > n {
		return truncateWidth(s, n)
	}
	return s + strings.Repeat(" ", n-w)
}

// truncateWidth truncates s to at most n display cells, appending "…"
// when truncation actually happens.
func truncateWidth(s string, n int) string {
	if visibleWidth(s) <= n {
		return s
	}
	if n <= 1 {
		return "…"
	}
	out := make([]rune, 0, n)
	w := 0
	for _, r := range s {
		rw := lipgloss.Width(string(r))
		if w+rw > n-1 {
			break
		}
		out = append(out, r)
		w += rw
	}
	return string(out) + "…"
}

func visibleWidth(s string) int { return lipgloss.Width(s) }

// renderDetailBox is the right-hand profile-detail panel. It used to
// share its column with a "tip" hint box below, but that box just
// duplicated the footer shortcuts and ate three rows — gone now, all
// the height belongs to the detail panel.
func (m *Model) renderDetailBox(width, height int) string {
	it := m.currentItem()
	if it == nil {
		return components.Box{
			Title:       theme.AccentCyan.Render("◆ ") + theme.Subtle.Render("no selection"),
			Content:     theme.Dim.Render("(empty list)"),
			Width:       width,
			Height:      height,
			BorderColor: theme.Purple,
		}.Render()
	}
	auto := false
	if o, ok := m.svc.GetOverlay(it.ConfigPath); ok {
		auto = o.AutoConnect
	}
	rows := []string{
		kv("status", statusText(*it)),
		kv("host", theme.Subtle.Render("—")),
		kv("cipher", theme.Subtle.Render("—")),
		kv("auth", theme.Subtle.Render("—")),
		kv("country", or(it.CountryCode, theme.Subtle.Render("—"))),
		kv("favorite", boolMark(it.Favorite)),
		kv("auto", boolMark(auto)),
		kv("last", relTime(it.LastUsed)),
	}
	body := clipLines(strings.Join(rows, "\n"), height-2)
	return components.Box{
		Title:       theme.AccentCyan.Render("◆ ") + it.Name,
		Content:     body,
		Width:       width,
		Height:      height,
		BorderColor: theme.Purple,
		Glow:        true,
	}.Render()
}

// headerPills builds the right-aligned status indicator strip. Reflects
// actual state — number of active sessions and total configs — instead of
// the placeholder "● running" badge that shipped in the first cut.
func (m *Model) headerPills() []string {
	active := 0
	for _, it := range m.items {
		if it.HasSession {
			active++
		}
	}
	var first string
	switch active {
	case 0:
		first = components.Pill("○ idle", theme.FgDim, theme.Panel2)
	case 1:
		first = components.Pill("● 1 active", theme.Mint, theme.MintDp)
	default:
		first = components.Pill(fmt.Sprintf("● %d active", active), theme.Mint, theme.MintDp)
	}
	return []string{
		first,
		components.Pill(fmt.Sprintf("%d configs", len(m.items)), theme.FgDim, theme.Panel2),
	}
}

//nolint:gocritic // hugeParam: same reasoning as renderRow above
func statusText(it Item) string {
	if it.HasSession {
		return components.Pill("● connected", theme.Mint, theme.MintDp)
	}
	return components.Pill("○ disconnected", theme.FgDim, theme.Panel2)
}

func kv(k, v string) string {
	keyStyle := lipgloss.NewStyle().Foreground(theme.FgDim).Width(10)
	return keyStyle.Render(k) + " " + v
}

func boolMark(b bool) string {
	if b {
		return theme.AccentMint.Render("✓")
	}
	return theme.Subtle.Render("—")
}

func or(s, fallback string) string {
	if s == "" {
		return fallback
	}
	return s
}

// relTime mirrors the design's "2h ago / yesterday / 3d ago / never" tags.
func relTime(t time.Time) string {
	if t.IsZero() {
		return "never"
	}
	d := time.Since(t)
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	case d < 48*time.Hour:
		return "yesterday"
	case d < 14*24*time.Hour:
		return fmt.Sprintf("%dd ago", int(d.Hours()/24))
	case d < 60*24*time.Hour:
		return fmt.Sprintf("%dw ago", int(d.Hours()/(24*7)))
	default:
		return t.Format("2006-01-02")
	}
}
