package views

import (
	"errors"
	"os"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/widget"

	"github.com/esivres/openvpn3ui/internal/otpimport"
)

// importedSecret is what the import flow hands back to the OTP dialog: the
// chosen account's base32 secret, ready to drop into the entry.
type importedSecret struct {
	Secret string
}

// promptImportFromURI shows a small dialog with a multiline text field for
// pasting either an otpauth:// or otpauth-migration:// URI. On success,
// onPick fires with the chosen account's secret.
func promptImportFromURI(parent fyne.Window, onPick func(importedSecret)) {
	entry := widget.NewMultiLineEntry()
	entry.SetPlaceHolder("otpauth://totp/...  or  otpauth-migration://offline?data=...")
	entry.Wrapping = fyne.TextWrapBreak

	d := dialog.NewCustomConfirm(
		"Import OTP from URI", "Import", "Cancel",
		container.NewVBox(
			widget.NewLabel("Paste an otpauth:// or Google Authenticator export URI."),
			entry,
		),
		func(ok bool) {
			if !ok {
				return
			}
			handleImport(parent, entry.Text, onPick, "import from URI")
		},
		parent,
	)
	d.Resize(fyne.NewSize(520, 240))
	d.Show()
}

// promptImportFromQR opens a file picker for a PNG/JPEG containing a QR
// code. Decodes locally — nothing is uploaded anywhere.
func promptImportFromQR(parent fyne.Window, onPick func(importedSecret)) {
	dialog.ShowFileOpen(func(rc fyne.URIReadCloser, err error) {
		if err != nil {
			dialog.ShowError(err, parent)
			return
		}
		if rc == nil {
			return // cancelled
		}
		defer rc.Close()

		uri, err := otpimport.DecodeQRImage(rc)
		if err != nil {
			// Fallback: some Fyne URIReadClosers don't support image
			// decode well from a stream; retry by re-reading the path.
			if path := rc.URI().Path(); path != "" {
				if f, ferr := os.Open(path); ferr == nil {
					defer f.Close()
					if u2, ferr2 := otpimport.DecodeQRImage(f); ferr2 == nil {
						uri = u2
						err = nil
					}
				}
			}
			if err != nil {
				dialog.ShowError(err, parent)
				return
			}
		}
		handleImport(parent, uri, onPick, "import from QR")
	}, parent)
}

func handleImport(parent fyne.Window, raw string, onPick func(importedSecret), label string) {
	accounts, err := otpimport.ParseURI(raw)
	if err != nil {
		dialog.ShowError(err, parent)
		return
	}
	if len(accounts) == 0 {
		dialog.ShowError(errors.New(label+": no accounts found"), parent)
		return
	}
	if len(accounts) == 1 {
		onPick(importedSecret{Secret: accounts[0].Secret})
		return
	}
	// Multiple — let the user choose.
	pickAccount(parent, accounts, func(a otpimport.Account) {
		onPick(importedSecret{Secret: a.Secret})
	})
}

// pickAccount renders a radio list when an import yields several accounts
// (typical for Google Authenticator's bulk export QR).
func pickAccount(parent fyne.Window, accounts []otpimport.Account, onPick func(otpimport.Account)) {
	labels := make([]string, len(accounts))
	for i, a := range accounts {
		labels[i] = a.Label()
	}
	radio := widget.NewRadioGroup(labels, nil)
	radio.SetSelected(labels[0])

	d := dialog.NewCustomConfirm(
		"Choose account",
		"Use this", "Cancel",
		container.NewVBox(
			widget.NewLabel("Multiple accounts found in the import. Pick one:"),
			radio,
		),
		func(ok bool) {
			if !ok || radio.Selected == "" {
				return
			}
			for i, l := range labels {
				if l == radio.Selected {
					onPick(accounts[i])
					return
				}
			}
		},
		parent,
	)
	d.Resize(fyne.NewSize(420, 320))
	d.Show()
}
