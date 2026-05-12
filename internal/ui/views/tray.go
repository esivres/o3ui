package views

import (
	"context"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/driver/desktop"

	"github.com/esivres/openvpn3ui/internal/app"
	"github.com/esivres/openvpn3ui/internal/ovpn"
	"github.com/esivres/openvpn3ui/internal/ui/theme"
)

// Tray owns the system-tray menu and icon. It is a thin projection of the
// service state: every Refresh() rebuilds the menu and updates the icon.
//
// On desktops without tray support (or when fyne.App is not a desktop.App),
// Tray is a no-op and the rest of the app keeps working normally.
type Tray struct {
	app  fyne.App
	desk desktop.App // nil if tray unsupported
	svc  *app.Service
	win  fyne.Window
}

func InstallTray(a fyne.App, svc *app.Service, win fyne.Window) *Tray {
	t := &Tray{app: a, svc: svc, win: win}
	if d, ok := a.(desktop.App); ok {
		t.desk = d
		t.desk.SetSystemTrayIcon(theme.IconDisconnected)
	}
	// Closing the window hides it instead of exiting; the tray keeps the
	// app alive in the background.
	win.SetCloseIntercept(func() { win.Hide() })
	t.Refresh()
	return t
}

// Refresh rebuilds the tray menu and updates the status icon based on the
// current service state. Cheap enough to call after every user action.
func (t *Tray) Refresh() {
	if t.desk == nil {
		return
	}

	configs, _ := t.svc.ListConfigs()
	sessions, _ := t.svc.ActiveSessions()
	active := map[string]ovpn.Session{}
	for _, s := range sessions {
		active[s.ConfigPath] = s
	}

	items := make([]*fyne.MenuItem, 0, len(configs)+4)
	items = append(items, fyne.NewMenuItem("Show window", func() {
		t.win.Show()
		t.win.RequestFocus()
	}))
	items = append(items, fyne.NewMenuItemSeparator())

	if len(configs) == 0 {
		empty := fyne.NewMenuItem("(no configs)", func() {})
		empty.Disabled = true
		items = append(items, empty)
	}
	for _, c := range configs {
		c := c
		item := fyne.NewMenuItem(c.Name, func() {
			if sess, on := active[c.Path]; on {
				_ = t.svc.Disconnect(sess.Path)
			} else {
				_, _ = t.svc.Connect(context.Background(), c.Path)
			}
			t.Refresh()
		})
		if _, on := active[c.Path]; on {
			item.Checked = true
		}
		items = append(items, item)
	}

	items = append(items,
		fyne.NewMenuItemSeparator(),
		fyne.NewMenuItem("Quit", func() { t.app.Quit() }),
	)

	t.desk.SetSystemTrayMenu(fyne.NewMenu("openvpn3ui", items...))

	switch {
	case len(active) > 0:
		t.desk.SetSystemTrayIcon(theme.IconConnected)
	default:
		t.desk.SetSystemTrayIcon(theme.IconDisconnected)
	}
}
