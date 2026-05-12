# o3ui — OpenVPN3 controller

Linux-native client for openvpn3-linux via D-Bus. Two surfaces:

- **TUI** (Bubble Tea / lipgloss) — `o3ui` with no arguments. Profile list,
  inline connect/disconnect, edit screen with TOTP import (URI / QR / manual),
  settings, live connected view with throughput sparklines.
- **Cinnamon desklet** — small always-on-screen widget showing status and a
  quick toggle. Drives the CLI under the hood. Install via
  `o3ui desklet install`.

The CLI also exposes a non-interactive surface (`status --json`, `list`,
`connect`, `disconnect`) so the desklet — and your shell scripts — can drive
openvpn3 through the same auth chain (stored credentials, auto-TOTP, prompts)
as the interactive TUI.

## Install

```sh
# Binary release (Debian / Ubuntu / Mint):
#   download the .deb from GitHub Releases and:
sudo apt install ./o3ui_*_linux_amd64.deb

# Then install the desklet (copies it to ~/.local/share/cinnamon/desklets):
o3ui desklet install
```

Add **OVPN3** from System Settings → Desklets to put the widget on your
desktop.

## Build from source

Requires Go 1.22+ and a Linux box with `dbus`, `openvpn3-linux`, and (for the
keyring) the Secret Service daemon (gnome-keyring, KWallet, etc.) running.

```sh
make build        # writes ~/.local/bin/o3ui
make install      # build + run `o3ui desklet install`
make check        # gofmt + go vet + race tests
make lint         # golangci-lint (optional, install separately)
```

## Architecture

```
cmd/openvpn3ui-tui/    main: dispatches subcommands or launches TUI
internal/app/          Service — orchestration, Auth chain, Sampler
internal/ovpn/         D-Bus client (godbus/v5) + retry decorator + Watcher
internal/cli/          status/list/connect/disconnect + desklet install
internal/cli/desklet/  embedded Cinnamon desklet (JS + CSS + icon)
internal/tui/          Bubble Tea screens (list, connecting, connected, …)
internal/overlay/      SQLite per-config metadata (modernc.org/sqlite)
internal/secrets/      Secret Service (zalando/go-keyring)
internal/otp/          TOTP / HOTP
internal/otpimport/    otpauth:// URI + Google-Authenticator migration + QR
```

UI talks only to `*app.Service`. Domain packages know nothing about Bubble
Tea or Cinnamon. The CLI and the TUI both build the same Service the same
way (`buildService` in `internal/cli`, similar wiring in main), so the
desklet path and the interactive path go through identical Auth chains and
overlay/keyring code.

## License

MIT — see [LICENSE](LICENSE).
