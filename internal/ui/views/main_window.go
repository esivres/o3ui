package views

import (
	"context"
	"fmt"
	"path/filepath"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/widget"

	"github.com/esivres/openvpn3ui/internal/app"
	"github.com/esivres/openvpn3ui/internal/ovpn"
)

// MainWindow renders the configuration list with import/connect controls.
// All actions go through *app.Service — the view holds no domain state.
type MainWindow struct {
	svc      *app.Service
	win      fyne.Window
	list     *widget.List
	configs  []ovpn.Config
	sessions map[string]ovpn.Session // configPath → session, for status indicator
	selected int
	status   *widget.Label

	connectBtn    *widget.Button
	disconnectBtn *widget.Button
	otpBtn        *widget.Button

	// onChange fires after any successful state-changing action so external
	// observers (e.g. the system tray) can refresh their projections.
	onChange func()
}

// SetOnChange registers a callback fired after Connect/Disconnect/Import.
func (mw *MainWindow) SetOnChange(f func()) { mw.onChange = f }

func (mw *MainWindow) notifyChanged() {
	if mw.onChange != nil {
		mw.onChange()
	}
}

func NewMainWindow(svc *app.Service, win fyne.Window) *MainWindow {
	mw := &MainWindow{svc: svc, win: win, selected: -1, sessions: map[string]ovpn.Session{}}
	mw.status = widget.NewLabel("")

	mw.list = widget.NewList(
		func() int { return len(mw.configs) },
		func() fyne.CanvasObject {
			return widget.NewLabel("template")
		},
		func(i widget.ListItemID, o fyne.CanvasObject) {
			c := mw.configs[i]
			mark := "·"
			if !c.Valid {
				mark = "!"
			}
			if _, on := mw.sessions[c.Path]; on {
				mark = "●"
			}
			o.(*widget.Label).SetText(fmt.Sprintf("%s  %s", mark, c.Name))
		},
	)
	mw.list.OnSelected = func(id widget.ListItemID) {
		mw.selected = id
		mw.refreshButtons()
	}
	mw.list.OnUnselected = func(widget.ListItemID) {
		mw.selected = -1
		mw.refreshButtons()
	}

	importBtn := widget.NewButton("Import .ovpn", mw.importDialog)
	refreshBtn := widget.NewButton("Refresh", mw.Refresh)
	mw.connectBtn = widget.NewButton("Connect", mw.connectSelected)
	mw.disconnectBtn = widget.NewButton("Disconnect", mw.disconnectSelected)
	mw.otpBtn = widget.NewButton("OTP…", mw.otpDialog)
	mw.refreshButtons()

	toolbar := container.NewHBox(importBtn, refreshBtn, mw.connectBtn, mw.disconnectBtn, mw.otpBtn)
	root := container.NewBorder(toolbar, mw.status, nil, nil, mw.list)
	win.SetContent(root)
	return mw
}

func (mw *MainWindow) Refresh() {
	cfgs, err := mw.svc.ListConfigs()
	if err != nil {
		mw.status.SetText("error: " + err.Error())
		return
	}
	mw.configs = cfgs

	// Use ActiveSessions, not ListSessions — disconnected-but-still-listed
	// sessions otherwise keep the UI buttons in the "connected" state
	// forever after Disconnect.
	mw.sessions = map[string]ovpn.Session{}
	if sessions, err := mw.svc.ActiveSessions(); err == nil {
		for _, s := range sessions {
			mw.sessions[s.ConfigPath] = s
		}
	}

	mw.list.Refresh()
	mw.refreshButtons()

	active := len(mw.sessions)
	mw.status.SetText(fmt.Sprintf("%d configs, %d active", len(cfgs), active))
}

func (mw *MainWindow) refreshButtons() {
	if mw.selected < 0 || mw.selected >= len(mw.configs) {
		mw.connectBtn.Disable()
		mw.disconnectBtn.Disable()
		return
	}
	cfg := mw.configs[mw.selected]
	if _, on := mw.sessions[cfg.Path]; on {
		mw.connectBtn.Disable()
		mw.disconnectBtn.Enable()
	} else {
		mw.connectBtn.Enable()
		mw.disconnectBtn.Disable()
	}
}

func (mw *MainWindow) importDialog() {
	dialog.ShowFileOpen(func(rc fyne.URIReadCloser, err error) {
		if err != nil || rc == nil {
			return
		}
		defer rc.Close()
		uri := rc.URI()
		name := filepath.Base(uri.Path())
		if _, err := mw.svc.ImportFromFile(name, uri.Path()); err != nil {
			dialog.ShowError(err, mw.win)
			return
		}
		mw.Refresh()
		mw.notifyChanged()
	}, mw.win)
}

func (mw *MainWindow) connectSelected() {
	if mw.selected < 0 {
		return
	}
	cfg := mw.configs[mw.selected]
	if _, err := mw.svc.Connect(context.Background(), cfg.Path); err != nil {
		dialog.ShowError(err, mw.win)
		return
	}
	mw.Refresh()
	mw.notifyChanged()
}

func (mw *MainWindow) otpDialog() {
	if mw.selected < 0 {
		return
	}
	cfg := mw.configs[mw.selected]
	ShowOTPDialog(mw.win, mw.svc, cfg.Path, cfg.Name, func() {
		mw.Refresh()
		mw.notifyChanged()
	})
}

func (mw *MainWindow) disconnectSelected() {
	if mw.selected < 0 {
		return
	}
	cfg := mw.configs[mw.selected]
	sess, ok := mw.sessions[cfg.Path]
	if !ok {
		return
	}
	if err := mw.svc.Disconnect(sess.Path); err != nil {
		dialog.ShowError(err, mw.win)
		return
	}
	mw.Refresh()
	mw.notifyChanged()
}
