package theme

import "fyne.io/fyne/v2"

// Tray icons. Three states, encoded as inline SVG so the binary stays
// self-contained. The shapes are intentionally simple (a shield) so they
// stay legible at 22×22 / 16×16 in any tray.
var (
	IconDisconnected = &fyne.StaticResource{
		StaticName:    "openvpn3ui-disconnected.svg",
		StaticContent: []byte(svgShield("#9CA3AF")), // neutral gray
	}
	IconConnecting = &fyne.StaticResource{
		StaticName:    "openvpn3ui-connecting.svg",
		StaticContent: []byte(svgShield("#F59E0B")), // amber
	}
	IconConnected = &fyne.StaticResource{
		StaticName:    "openvpn3ui-connected.svg",
		StaticContent: []byte(svgShield("#10B981")), // emerald
	}
)

func svgShield(fill string) string {
	return `<?xml version="1.0" encoding="UTF-8"?>
<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 24 24" width="24" height="24">
  <path fill="` + fill + `" d="M12 2 4 5v6c0 5 3.4 9.5 8 11 4.6-1.5 8-6 8-11V5l-8-3z"/>
  <path fill="#FFFFFF" d="m10.6 14.2-2.3-2.3 1.4-1.4 0.9 0.9 3.4-3.4 1.4 1.4-4.8 4.8z"/>
</svg>`
}
