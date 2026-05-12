package main

import (
	"log"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/app"

	openapp "github.com/esivres/openvpn3ui/internal/app"
	"github.com/esivres/openvpn3ui/internal/overlay"
	"github.com/esivres/openvpn3ui/internal/ovpn"
	"github.com/esivres/openvpn3ui/internal/secrets"
	"github.com/esivres/openvpn3ui/internal/ui/theme"
	"github.com/esivres/openvpn3ui/internal/ui/views"
)

// sessionAdapter widens *ovpn.SessionManager.Control's concrete return type
// to the openapp.SessionControl interface. It's the seam that lets the
// service layer remain mockable without forcing the ovpn package to know
// about it.
type sessionAdapter struct{ *ovpn.SessionManager }

func (a sessionAdapter) Control(path string) openapp.SessionControl {
	return a.SessionManager.Control(path)
}

func main() {
	conn, err := ovpn.ConnectSystemBus()
	if err != nil {
		log.Fatalf("connect system bus: %v", err)
	}
	defer conn.Close()

	overlayPath, err := overlay.DefaultPath()
	if err != nil {
		log.Fatalf("overlay path: %v", err)
	}
	ov, err := overlay.Open(overlayPath)
	if err != nil {
		log.Fatalf("open overlay: %v", err)
	}
	defer ov.Close()

	secs := secrets.New()

	svc := openapp.New(
		ovpn.NewConfigManager(conn),
		sessionAdapter{ovpn.NewSessionManager(conn)},
	)
	svc.SetStorage(ov, secs)
	svc.SetAuth(openapp.ChainAuth{Layers: []openapp.Auth{
		openapp.NewAutoTOTPAuth(ov, secs),
		// TODO: UI prompt fallback for username/password.
	}})

	a := app.New()
	a.Settings().SetTheme(theme.New())
	w := a.NewWindow("openvpn3ui")
	w.Resize(fyne.NewSize(720, 480))

	mw := views.NewMainWindow(svc, w)
	tray := views.InstallTray(a, svc, w)
	mw.SetOnChange(tray.Refresh)
	mw.Refresh()
	w.ShowAndRun()
}
