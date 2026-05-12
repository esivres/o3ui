package components

import (
	"strings"

	"github.com/charmbracelet/lipgloss"

	"github.com/esivres/openvpn3ui/internal/tui/theme"
)

// KeyHelp is one entry in the bottom help strip.
type KeyHelp struct {
	Key, Label string
}

// helpItemSep is the inter-item separator. A space-padded middle dot
// reads as a group boundary; with 5-essentials footers we can afford
// the extra cell, and the dot is far easier to scan than two spaces.
const helpItemSep = " · "

// HelpBar renders the dimmed footer:
//
//	↑/↓ navigate · enter connect · / filter · ?  help
//
// When `width` is too small to hold every item on one line, items wrap to
// additional rows (left-aligned). Flex behaviour for narrow terminals so
// nothing gets clipped.
func HelpBar(items []KeyHelp, width int) string {
	keyStyle := lipgloss.NewStyle().Foreground(theme.Purple).Bold(true)
	labelStyle := lipgloss.NewStyle().Foreground(theme.FgDim)

	// Tighter inter-key layout: just "key label" with one space, no
	// middle dot. Fits more items on one line in a normal terminal.
	rendered := make([]string, len(items))
	widths := make([]int, len(items))
	for i, it := range items {
		s := keyStyle.Render(it.Key) + " " + labelStyle.Render(it.Label)
		rendered[i] = s
		widths[i] = lipgloss.Width(s)
	}

	sepW := lipgloss.Width(helpItemSep)

	if width <= 0 {
		return strings.Join(rendered, helpItemSep)
	}

	rows := [][]string{}
	cur := []string{}
	curW := 0
	for i, item := range rendered {
		need := widths[i]
		if len(cur) > 0 {
			need += sepW
		}
		if curW+need > width {
			rows = append(rows, cur)
			cur = nil
			curW = 0
			need = widths[i]
		}
		cur = append(cur, item)
		curW += need
	}
	if len(cur) > 0 {
		rows = append(rows, cur)
	}

	out := make([]string, len(rows))
	for i, row := range rows {
		out[i] = strings.Join(row, helpItemSep)
	}
	return strings.Join(out, "\n")
}

// Dotted is the very dim line of dots used as an in-content separator.
func Dotted(width int) string {
	return lipgloss.NewStyle().
		Foreground(theme.FgSubtle).
		Render(strings.Repeat("·", width))
}
