package components

import (
	"strings"

	"github.com/charmbracelet/lipgloss"

	"github.com/esivres/openvpn3ui/internal/tui/theme"
)

// Box wraps content in a rounded border with an optional title strip.
// borderColor may be empty (theme.Border default). When a title is given,
// it sits above a dashed inner divider, matching the design's style.
//
// Width and Height are both optional. When Height is set the box pads
// the content area downwards so its bottom border lands at the desired
// row — useful for full-height list/detail panels.
type Box struct {
	Title       string
	Content     string
	Width       int
	Height      int
	BorderColor lipgloss.Color
	Glow        bool // currently informational; reserved for future fx
}

func (b Box) Render() string {
	border := b.BorderColor
	if border == "" {
		border = theme.Border
	}
	body := b.Content
	if b.Title != "" {
		titleLine := lipgloss.NewStyle().
			Foreground(theme.FgBright).
			Bold(true).
			Render(b.Title)
		divider := lipgloss.NewStyle().
			Foreground(theme.Border).
			Render(strings.Repeat("·", boxInnerWidth(b.Width)))
		body = titleLine + "\n" + divider + "\n" + b.Content
	}

	style := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(border).
		Padding(0, 1)
	if b.Width > 0 {
		style = style.Width(b.Width)
	}
	if b.Height > 0 {
		style = style.Height(b.Height)
	}
	return style.Render(body)
}

func boxInnerWidth(boxWidth int) int {
	// Account for the rounded-border (2 cells) + horizontal padding (2).
	w := boxWidth - 4
	if w < 4 {
		w = 4
	}
	if w > 200 {
		w = 200
	}
	return w
}
