package theme

import "github.com/charmbracelet/lipgloss"

// Dim, Subtle, Bright are the three muted-foreground tiers used across
// the design (key/label dimming, separator dots, highlighted values).
var (
	Dim    = lipgloss.NewStyle().Foreground(FgDim)
	Subtle = lipgloss.NewStyle().Foreground(FgSubtle)
	Bright = lipgloss.NewStyle().Foreground(FgBright).Bold(true)
)

// Accent text helpers for inline bits ("vpn.acme-corp.de:1194", "AES-256-GCM").
var (
	AccentPink   = lipgloss.NewStyle().Foreground(Pink).Bold(true)
	AccentPurple = lipgloss.NewStyle().Foreground(Purple).Bold(true)
	AccentMint   = lipgloss.NewStyle().Foreground(Mint).Bold(true)
	AccentCyan   = lipgloss.NewStyle().Foreground(Cyan)
	AccentPeach  = lipgloss.NewStyle().Foreground(Peach)
	AccentYellow = lipgloss.NewStyle().Foreground(Yellow)
	AccentRed    = lipgloss.NewStyle().Foreground(Red)
)
