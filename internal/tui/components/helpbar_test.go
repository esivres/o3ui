package components

import (
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"
	"github.com/stretchr/testify/require"
)

var sampleItems = []KeyHelp{
	{"↑/↓", "navigate"},
	{"enter", "connect"},
	{"e", "edit"},
	{"f", "favorite"},
	{"i", "import"},
	{"/", "filter"},
	{"r", "refresh"},
	{"q", "quit"},
}

// At a generous width every item must fit on a single line — no wrapping.
// Also verifies the bar carries no library/branding tagline.
func TestHelpBar_FitsOnOneLineWhenWide(t *testing.T) {
	out := HelpBar(sampleItems, 200)
	require.Equal(t, 1, strings.Count(out, "\n")+1, "expected one line, got: %q", out)
	require.Contains(t, out, "navigate")
	require.NotContains(t, out, "bubbletea", "no branding tagline allowed in help bar")
	require.NotContains(t, out, "charmbracelet")
}

// At a tight width the bar must wrap into multiple rows. The first row
// must not exceed the requested width — if it did, the user's terminal
// would horizontal-scroll or truncate.
func TestHelpBar_WrapsWhenNarrow(t *testing.T) {
	out := HelpBar(sampleItems, 40)
	rows := strings.Split(out, "\n")
	require.Greater(t, len(rows), 1, "expected wrapping at width=40, got one line: %q", out)
	for i, r := range rows {
		require.LessOrEqual(t, lipgloss.Width(r), 40,
			"row %d exceeds width: %q (w=%d)", i, r, lipgloss.Width(r))
	}
}

// All keys must still be present after wrapping — flex must not drop content.
func TestHelpBar_AllKeysPreserved(t *testing.T) {
	out := HelpBar(sampleItems, 35)
	for _, it := range sampleItems {
		require.Contains(t, out, it.Label, "wrapping must not drop %q", it.Label)
	}
}
