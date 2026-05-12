// Package components — bigdigits is a tiny figlet-style font used to
// render TOTP codes (and any 0-9 digit string) at ~5× the height of a
// normal cell. The patterns are 3 wide × 5 tall in block characters.
// Anything outside 0-9 falls back to a row of spaces so the layout
// stays grid-aligned even on bad input.
package components

import "strings"

// bigDigitHeight is the number of rows each glyph spans. Useful for
// callers that need to budget vertical space around the rendered code.
const bigDigitHeight = 5

// bigDigits[d][row] is the rendered row of digit d. Width is constant
// at 3 cells — 6 digits + 5 single-cell gaps == 23 cells, which fits in
// the TOTP card without re-flow on a standard 80-col terminal.
var bigDigits = [10][bigDigitHeight]string{
	{"███", "█ █", "█ █", "█ █", "███"}, // 0
	{" █ ", "██ ", " █ ", " █ ", "███"}, // 1
	{"███", "  █", "███", "█  ", "███"}, // 2
	{"███", "  █", "███", "  █", "███"}, // 3
	{"█ █", "█ █", "███", "  █", "  █"}, // 4
	{"███", "█  ", "███", "  █", "███"}, // 5
	{"███", "█  ", "███", "█ █", "███"}, // 6
	{"███", "  █", "  █", "  █", "  █"}, // 7
	{"███", "█ █", "███", "█ █", "███"}, // 8
	{"███", "█ █", "███", "  █", "███"}, // 9
}

// BigDigits renders the digits of s as a 5-line block-character banner.
// Each digit is separated by one blank column for readability. Non-digit
// runes (e.g. the space in "123 456" formatted codes) render as a
// 3-wide blank, so the grid keeps marching forward at a predictable
// pitch.
func BigDigits(s string) string {
	if s == "" {
		return ""
	}
	rows := make([]strings.Builder, bigDigitHeight)
	for i, r := range s {
		if i > 0 {
			for k := range rows {
				rows[k].WriteString(" ")
			}
		}
		if r >= '0' && r <= '9' {
			d := int(r - '0')
			for k := 0; k < bigDigitHeight; k++ {
				rows[k].WriteString(bigDigits[d][k])
			}
			continue
		}
		// Unknown rune — preserve column width so adjacent digits stay
		// aligned. The space pattern is intentionally invisible; the
		// caller is responsible for separating groups visually if they
		// care (e.g. "123 456" already gets a triple-blank gap here).
		for k := 0; k < bigDigitHeight; k++ {
			rows[k].WriteString("   ")
		}
	}
	out := make([]string, bigDigitHeight)
	for i := range rows {
		out[i] = rows[i].String()
	}
	return strings.Join(out, "\n")
}
