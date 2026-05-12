// Package theme defines the custom Fyne theme for openvpn3ui.
// Goal: a calmer, more modern palette than Fyne's default — denser type,
// rounded surfaces, restrained accent.
package theme

import (
	"image/color"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/theme"
)

type Theme struct{}

var _ fyne.Theme = (*Theme)(nil)

func New() fyne.Theme { return &Theme{} }

func (t *Theme) Color(name fyne.ThemeColorName, variant fyne.ThemeVariant) color.Color {
	switch name {
	case theme.ColorNameBackground:
		if variant == theme.VariantLight {
			return color.NRGBA{R: 0xF7, G: 0xF7, B: 0xF8, A: 0xFF}
		}
		return color.NRGBA{R: 0x14, G: 0x16, B: 0x1B, A: 0xFF}
	case theme.ColorNamePrimary:
		return color.NRGBA{R: 0x4F, G: 0x8C, B: 0xFF, A: 0xFF}
	case theme.ColorNameForeground:
		if variant == theme.VariantLight {
			return color.NRGBA{R: 0x1A, G: 0x1B, B: 0x1F, A: 0xFF}
		}
		return color.NRGBA{R: 0xE6, G: 0xE8, B: 0xEC, A: 0xFF}
	}
	return theme.DefaultTheme().Color(name, variant)
}

func (t *Theme) Font(s fyne.TextStyle) fyne.Resource { return theme.DefaultTheme().Font(s) }
func (t *Theme) Icon(n fyne.ThemeIconName) fyne.Resource {
	return theme.DefaultTheme().Icon(n)
}

func (t *Theme) Size(n fyne.ThemeSizeName) float32 {
	switch n {
	case theme.SizeNamePadding:
		return 6
	case theme.SizeNameInnerPadding:
		return 10
	}
	return theme.DefaultTheme().Size(n)
}
