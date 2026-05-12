// Package components is the Bubble Tea / Charm-styled view kit shared
// across screens: pill labels, rounded boxes, headers, help bars, dotted
// separators. Pure rendering helpers — no state, no I/O.
package components

import (
	"github.com/charmbracelet/lipgloss"

	"github.com/esivres/openvpn3ui/internal/tui/theme"
)

// Pill is a small inline label with horizontal padding and a coloured
// background — Charm's signature "● running", "auth required" tag style.
// fg may be empty (theme.Fg used as default).
func Pill(text string, fg, bg lipgloss.Color) string {
	if fg == "" {
		fg = theme.Fg
	}
	return lipgloss.NewStyle().
		Foreground(fg).
		Background(bg).
		Padding(0, 1).
		Bold(true).
		Render(text)
}

// BrandPill is the bright header badge ("ovpn3"). The original
// GradientPill API took a second colour for a fake gradient that never
// landed; dropped in favour of an honest single-colour pill.
func BrandPill(text string, bg lipgloss.Color) string {
	return lipgloss.NewStyle().
		Foreground(theme.FgBright).
		Background(bg).
		Padding(0, 2).
		Bold(true).
		Render(text)
}
