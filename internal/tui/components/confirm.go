package components

import (
	"github.com/charmbracelet/lipgloss"

	"github.com/esivres/openvpn3ui/internal/tui/theme"
)

// ConfirmModal renders a centred y/N dialog over the parent screen.
// Pure rendering helper — the host model owns the boolean state for
// "is the modal open" and the callback wiring. Same shape as the
// existing helpOverlay in program.go but exposed as a component so
// disconnect/export/delete-profile flows can reuse it without each
// re-inventing the lipgloss.Place dance.
//
// Convention: Yes is destructive, capitalised; No is the safe default
// and gets the `[Esc]` hint. Avoids accidental confirmations from
// muscle-memory Enter mashing.
type ConfirmModal struct {
	Title    string // short headline, e.g. "Disconnect FarzoomVpn?"
	Body     string // 1-2 lines of explanation, optional
	YesLabel string // defaults to "Yes" if empty
	NoLabel  string // defaults to "Cancel" if empty
	Danger   bool   // when true the Yes button uses the Red accent
}

// Render returns a string ready to be passed to lipgloss.Place. Caller
// composes it with whatever base view they want to dim/overlay.
func (c ConfirmModal) Render(width int) string {
	yes := c.YesLabel
	if yes == "" {
		yes = "Yes"
	}
	no := c.NoLabel
	if no == "" {
		no = "Cancel"
	}
	yesColor := theme.Mint
	if c.Danger {
		yesColor = theme.Red
	}

	titleStyle := lipgloss.NewStyle().Foreground(theme.FgBright).Bold(true)
	bodyStyle := lipgloss.NewStyle().Foreground(theme.Fg)
	hintStyle := lipgloss.NewStyle().Foreground(theme.FgDim)
	yesStyle := lipgloss.NewStyle().Foreground(theme.Bg).Background(yesColor).Bold(true).Padding(0, 2)
	noStyle := lipgloss.NewStyle().Foreground(theme.FgBright).Background(theme.Panel2).Padding(0, 2)

	parts := []string{
		titleStyle.Render(c.Title),
	}
	if c.Body != "" {
		parts = append(parts, "", bodyStyle.Render(c.Body))
	}
	buttons := lipgloss.JoinHorizontal(lipgloss.Top,
		yesStyle.Render("y · "+yes),
		"   ",
		noStyle.Render("n/esc · "+no),
	)
	parts = append(parts, "", buttons, "", hintStyle.Render("press y to confirm, n or Esc to cancel"))

	card := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(theme.BorderLt).
		Padding(1, 3).
		Render(lipgloss.JoinVertical(lipgloss.Left, parts...))
	return card
}
