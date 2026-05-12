# TODO

Active issues and follow-ups, by priority. Items move out of here into
git history when fixed.

## Sprint 5 — comfort + integration

- [ ] **Mouse-support** (`tea.WithMouseAllMotion`) — scroll, click on
      tabs / rows. One line to enable, the work is hit-test wiring.
- [ ] **Auto-connect on login** — overlay flag exists, runner does
      not. Needs systemd-user-service template + bin entrypoint that
      walks profiles with `auto_connect=true` and connects them.
- [ ] **`settings: connection`-таб** — defaults for auto-connect
      (depends on the runner above) + global log verbosity slider in
      one place instead of scattered.
- [ ] **`settings: network`** — DNS / routing globals, if openvpn3
      exposes per-service properties for them. Needs bus introspection
      first to see what's writable.

## TUI — visual / layout (отложенные дизайнерские)

- [ ] **Profile list box border barely visible.** Tried
      `theme.BorderLt`, still washed out on some terminals. Need a
      brighter colour or a different border style — pick on Mint
      defaults.
- [ ] **Header row clipped на узких терминалах.** Reproduce + measure
      `lipgloss.Height` of each part.
- [ ] **Footer wraps на ~80 cols.** Partially addressed via `?`
      overlay + tightened footer, but wide-glyph `↑↓ ⏎` still a risk.

## Backend

- [ ] **TUI gracefully degrade on missing keyring.** `secrets.New()`
      Open-s silently, but Set/Get fail per call; surface a friendly
      "keyring locked" hint in Auth modal.
- [ ] **GORM + gorm-gen migration** for `internal/overlay` — deferred
      until the schema settles.
- [ ] **Webcam QR import** — skipped when the OTP screen was built.
      Not in v1; revisit when there's demand.
- [ ] **Settings: backend service `restart` action.** Button is
      rendered, not wired. Needs a policy decision — polkit prompt,
      or remove the column.

## Done — moved into git history

Items finalised live in git history. Recent closures:

- Sprint 1 — UX foundation: confirm primitive, per-screen HelpKeys,
  cursor preservation on reload, drop decorative tab-sidebars,
  Esc-in-filter keeps the filter, dead `s` action gone.
- Sprint 2 — content delivery: .ovpn parser → list detail pane,
  edit-screen tabs (general / auth / raw) with proper left sidebar,
  settings tabs (backend / about) with buildinfo stamps, drop the
  fake connecting progress-bar, remove duplicate inline OTP modes,
  custom filepicker with extension cycle + substring search, layered
  auth modal, delete profile (D), Q vs q quit-confirm on active
  tunnel.
- Sprint 3 — wow-features: live openvpn3 Log stream on Connecting +
  Connected screens (ring buffer, severity colours), command palette
  `:` / `Ctrl+P` (fuzzy via sahilm/fuzzy), big-font ASCII TOTP code
  on the edit screen, diagnostic next-steps on Connect failure
  (auth / tls / network / port / service categories with concrete
  hints).
- Sprint 4 — comfort + content: public IP / latency probe on
  Connected (Cloudflare trace), `0..9` quick-jump on list, session
  history per profile (event-driven via D-Bus signals — captures
  every connect/disconnect regardless of source: TUI, desklet CLI,
  external `openvpn3 session-manage`), `edit: network` tab (server /
  port / proto overrides via openvpn3 SetOverride), `edit: advanced`
  tab (DCO / public_access / locked_down toggles + read-only usage
  info), Connected → Edit transition (`e` key, tunnel stays up).
