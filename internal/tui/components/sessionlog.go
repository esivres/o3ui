// Package components — sessionlog hosts shared types for the per-session
// Log ring buffer rendered on both the Connecting and Connected screens.
// Same data, same colour mapping, two consumers — extracting it here
// keeps the two screens from drifting apart over time.
package components

import (
	"time"

	"github.com/charmbracelet/lipgloss"

	"github.com/esivres/openvpn3ui/internal/tui/theme"
)

// LogEntry is one line in the per-session ring buffer. Level is the raw
// openvpn3 LogCategory enum; the caller is responsible for cap-trimming
// the buffer (the renderer doesn't know how many rows fit).
type LogEntry struct {
	At      time.Time
	Level   uint32
	Message string
}

// LogTag maps openvpn3's LogCategory enum to (short label, colour).
// Reference values from openvpn3-linux's LogCategory enum: 1=FATAL,
// 2=CRIT, 3=ERROR, 4=WARN, 5=INFO, 6=VERBOSE, 7=DEBUG. Unknown values
// fall back to a neutral dim tone — we never invent severity we don't
// have.
func LogTag(l uint32) (string, lipgloss.Color) {
	switch l {
	case 1:
		return "fatal", theme.Red
	case 2:
		return "crit", theme.Red
	case 3:
		return "err", theme.Red
	case 4:
		return "warn", theme.Peach
	case 5:
		return "info", theme.Cyan
	case 6:
		return "verb", theme.FgSubtle
	case 7:
		return "dbg", theme.FgSubtle
	}
	return "log", theme.FgSubtle
}

// RenderLogLine formats one ring-buffer entry as
// "HH:MM:SS  TAG  message". Padding keeps multiple lines aligned even
// when severities differ; the caller joins lines with "\n".
func RenderLogLine(e LogEntry) string {
	stamp := theme.Subtle.Render(e.At.Format("15:04:05"))
	label, colour := LogTag(e.Level)
	tag := lipgloss.NewStyle().Foreground(colour).Width(5).Render(label)
	return lipgloss.JoinHorizontal(lipgloss.Top, stamp, "  ", tag, " ", e.Message)
}
