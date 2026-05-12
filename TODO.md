# TODO

Active issues and follow-ups, by priority. Items move out of here into
git history when fixed.

## #0 — авторский надзор от дизайнера

- [ ] **Designer review pass over the live TUI.** The screens were
      implemented from the static handoff bundle without round-trip
      review. Several details (border weights, colour balance against
      the user's terminal palette, table column proportions, status
      pill density, footer copy) drifted from intent and need a real
      look from the designer at a running terminal. Before further
      polish — schedule this and treat the resulting notes as the
      source of truth for the visual section below.

## TUI — visual / layout

- [ ] **Profile list box border barely visible.** Tried `theme.BorderLt`,
      still washed out for the user's terminal palette. Needs a brighter
      colour or a different border style — pick after testing on Mint
      defaults.
- [ ] **Header row sometimes clipped on narrow / short terminals.**
      Even with the `MaxHeight(bodyH)` clip on the body, certain
      terminal sizes still chop the top "ovpn3 / profiles" strip.
      Likely a +1 row miscount somewhere — or the terminal reports
      a smaller height than tea sees. Reproduce + measure
      `lipgloss.Height` of each piece.
- [ ] **Footer wraps awkwardly at ~80 columns.** Even after shortening
      labels (`nav`, `go`, `off`, `↻`), the bar still spills onto two
      lines. Either accept it gracefully or implement a `?` overlay
      with the full key list and shrink the always-visible bar to
      essentials.
- [ ] **Star (★) misaligns when the row is very long.** Truncation
      now keeps it inside `cols.name`, but on the boundary case the
      star can land on a different column than the AUTH header. Add
      a unit-style render test to lock in the geometry.

## TUI — features

- [ ] **`.ovpn` parsing on import** — fill the AUTH/PROTO/CIPHER
      columns and the right-pane host/cipher/auth fields. We have the
      parser (`internal/ovpnconf`) and the body at import time; just
      need to wire results into overlay (new columns) and surface in
      `Service.GetOverlay`.
- [ ] **Live D-Bus log on Connecting screen.** Subscribe to per-session
      `Log` signal (after calling `LogForward(true)`). Replace the
      current static "waiting…" placeholder with a real scroll buffer.
- [ ] **Edit screen tabs are decorative.** `general / network /
      advanced / raw .ovpn` show but don't render anything. Either
      hide them until they work, or implement the `raw .ovpn` viewer
      (read-only) using openvpn3's `Fetch` — least invasive starting
      point.
- [ ] **Settings: backend service `restart`.** Action shown but not
      wired. Decide policy (admin / requires polkit) and either
      implement or remove the column.
- [ ] **Public IP / latency** on Connected screen — currently "—".
      Probe via HTTPS to a known host through the tun.
- [ ] **Auto-connect on login** — overlay flag exists, no runner.
      Needs a tiny systemd-user-service template + bin entrypoint.

## TUI — UX nits

- [ ] **`?` help overlay** with full key reference. The footer carries
      shortcuts; a deeper modal is more discoverable.
- [ ] **`.local` flag on the row** when a config is single-use vs.
      persistent — currently no visual distinction.
- [ ] **Status pill in HeaderBar** could include device name when
      connected (`tun0` / `wg0`) instead of just count.

## Backend

- [ ] **GORM + gorm-gen migration** for `internal/overlay` — task #24
      in the task tracker. Defer until schema settles further.
- [ ] **Webcam QR import** (was skipped during the OTP screen build).
      Not in the v1 ship target; revisit when there's a real demand.
- [ ] **TUI gracefully degrade on missing keyring.** Right now
      `secrets.New()` happily Opens but Set/Get may fail per call;
      surface a friendly "keyring locked" message in the Auth modal.

## Done — link to commits when we cut them

Items finalised (and removed from this list) live in git history.
