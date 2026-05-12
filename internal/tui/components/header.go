package components

import (
	"github.com/charmbracelet/lipgloss"

	"github.com/esivres/openvpn3ui/internal/tui/theme"
)

// HeaderBar is the top strip on every screen. Layout:
//
//	[gradient title]  subtitle   ………………………………………………………………………………………………  pill1 pill2 …
//
// width is the available terminal width; pills are pre-rendered strings
// (use Pill / BrandPill).
func HeaderBar(title, subtitle string, pills []string, width int) string {
	left := BrandPill(title, theme.Pink)
	if subtitle != "" {
		// Visible "/" between the brand pill and the route label —
		// reads like a breadcrumb. FgDim subtitle on its own used to
		// look like dropped pixels next to the bright pill.
		sep := lipgloss.NewStyle().
			Foreground(theme.Purple).
			Bold(true).
			Render(" / ")
		left = lipgloss.JoinHorizontal(lipgloss.Top,
			left, sep,
			lipgloss.NewStyle().Foreground(theme.Fg).Render(subtitle),
		)
	}

	right := ""
	for i, p := range pills {
		if i > 0 {
			right = lipgloss.JoinHorizontal(lipgloss.Top, right, " ")
		}
		right = lipgloss.JoinHorizontal(lipgloss.Top, right, p)
	}

	leftW := lipgloss.Width(left)
	rightW := lipgloss.Width(right)
	gap := width - leftW - rightW
	if gap < 1 {
		gap = 1
	}
	return lipgloss.JoinHorizontal(lipgloss.Top,
		left,
		lipgloss.NewStyle().Width(gap).Render(""),
		right,
	)
}
