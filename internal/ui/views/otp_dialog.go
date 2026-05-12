package views

import (
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/widget"

	"github.com/esivres/openvpn3ui/internal/app"
	"github.com/esivres/openvpn3ui/internal/otp"
)

// ShowOTPDialog opens the "attach OTP secret" dialog for a single config.
// On Save: validates + persists via svc.SetOTP and calls onChanged.
// On Remove: clears the binding via svc.RemoveOTP and calls onChanged.
func ShowOTPDialog(parent fyne.Window, svc *app.Service, configPath, configName string, onChanged func()) {
	secretEntry := widget.NewPasswordEntry()
	secretEntry.SetPlaceHolder("base32 secret (e.g. JBSWY3DPEHPK3PXP)")

	preview := widget.NewLabel("------")
	preview.TextStyle = fyne.TextStyle{Monospace: true, Bold: true}
	progress := widget.NewProgressBar()
	progress.Min, progress.Max = 0, 30

	updatePreview := func() {
		raw := secretEntry.Text
		if raw == "" {
			if code, ok := svc.PreviewOTP(configPath); ok {
				preview.SetText(code)
			} else {
				preview.SetText("------")
			}
		} else if secret, err := otp.DecodeBase32Secret(raw); err == nil {
			preview.SetText(otp.Now(otp.Config{Secret: secret}))
		} else {
			preview.SetText("invalid")
		}
		// 30s window — show fraction remaining.
		now := time.Now().Unix()
		progress.SetValue(float64(30 - (now % 30)))
	}
	updatePreview()
	secretEntry.OnChanged = func(string) { updatePreview() }

	importURIBtn := widget.NewButton("Paste URI", func() {
		promptImportFromURI(parent, func(s importedSecret) {
			secretEntry.SetText(s.Secret)
			updatePreview()
		})
	})
	importQRBtn := widget.NewButton("From QR image", func() {
		promptImportFromQR(parent, func(s importedSecret) {
			secretEntry.SetText(s.Secret)
			updatePreview()
		})
	})
	importRow := container.NewHBox(importURIBtn, importQRBtn)

	form := widget.NewForm(
		widget.NewFormItem("Secret", secretEntry),
		widget.NewFormItem("Import", importRow),
		widget.NewFormItem("Preview", container.NewVBox(preview, progress)),
	)

	// Live tick the preview so the user sees the code rotate. The ticker
	// stops when the dialog closes via the close channel.
	stop := make(chan struct{})
	go func() {
		t := time.NewTicker(time.Second)
		defer t.Stop()
		for {
			select {
			case <-stop:
				return
			case <-t.C:
				fyne.Do(updatePreview)
			}
		}
	}()
	stopOnce := func() {
		select {
		case <-stop: // already closed
		default:
			close(stop)
		}
	}

	// Compose content: form, optionally followed by a Remove button when a
	// secret is already attached. Has to be done before NewCustomConfirm
	// because ConfirmDialog has no SetContent setter.
	content := container.NewVBox(form)
	var d *dialog.ConfirmDialog
	if svc.HasOTP(configPath) {
		removeBtn := widget.NewButton("Remove existing OTP", func() {
			dialog.ShowConfirm("Remove OTP",
				"Detach the OTP secret from this configuration?",
				func(yes bool) {
					if !yes {
						return
					}
					if err := svc.RemoveOTP(configPath); err != nil {
						dialog.ShowError(err, parent)
						return
					}
					if onChanged != nil {
						onChanged()
					}
					stopOnce()
					d.Hide()
				}, parent)
		})
		content.Add(removeBtn)
	}

	d = dialog.NewCustomConfirm(
		"OTP for "+configName, "Save", "Cancel", content,
		func(save bool) {
			stopOnce()
			if !save {
				return
			}
			if secretEntry.Text == "" {
				dialog.ShowError(errEmptySecret, parent)
				return
			}
			if err := svc.SetOTP(configPath, secretEntry.Text); err != nil {
				dialog.ShowError(err, parent)
				return
			}
			if onChanged != nil {
				onChanged()
			}
		},
		parent,
	)

	d.Resize(fyne.NewSize(420, 220))
	d.Show()
}

// sentinel error so we don't allocate a new one on every empty save.
var errEmptySecret = errEmpty("secret cannot be empty")

type errEmpty string

func (e errEmpty) Error() string { return string(e) }
