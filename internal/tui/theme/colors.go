// Package theme holds the design tokens — colours, padding values, border
// glyphs — used by every TUI screen. Source of truth for the look from the
// Bubble Tea / Charm-style mock in /tmp/design/openvpn3ui.
package theme

import "github.com/charmbracelet/lipgloss"

// Palette mirrors the C{} table in the design's tui.jsx. Names kept short
// because lipgloss styles compose them often.
var (
	Bg       = lipgloss.Color("#1a1b26")
	Panel    = lipgloss.Color("#1f2030")
	Panel2   = lipgloss.Color("#262738")
	Surface  = lipgloss.Color("#2a2c3e")
	Fg       = lipgloss.Color("#cdd6f4")
	FgBright = lipgloss.Color("#ffffff")
	// Dim/Subtle/Border tiers chosen to stay readable on whichever dark
	// terminal background the user runs (gnome-terminal default, Mint
	// burgundy, plain black). Originals were calibrated against the
	// designer's #1a1b26 canvas — which we no longer paint — so contrast
	// is bumped a notch across the board.
	FgDim    = lipgloss.Color("#8a91ac")
	FgSubtle = lipgloss.Color("#6a6f8e")
	Border   = lipgloss.Color("#6b73a3")
	BorderLt = lipgloss.Color("#8b94c4")

	// Charm-signature accents.
	Pink     = lipgloss.Color("#ff5fa2")
	PinkSoft = lipgloss.Color("#7a2e52")
	Purple   = lipgloss.Color("#a78bfa")
	PurpleDp = lipgloss.Color("#7c3aed")
	Mint     = lipgloss.Color("#74e1c2")
	MintDp   = lipgloss.Color("#2c8c70")
	Peach    = lipgloss.Color("#ffb86c")
	Cyan     = lipgloss.Color("#7dcfff")
	Yellow   = lipgloss.Color("#f9e2af")
	Red      = lipgloss.Color("#f38ba8")
)
