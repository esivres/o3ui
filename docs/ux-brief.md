# o3ui — описание интерфейса и сценариев

## Контекст

Linux-утилита для openvpn3-linux (D-Bus daemon). Два фронта:

- **TUI** — `o3ui` без аргументов. Bubble Tea / lipgloss. Тёмная палитра в стиле Catppuccin (Pink #ff5fa2 / Purple #a78bfa брэнд, Mint/Peach/Cyan акценты, борд `#6b73a3`).
- **Cinnamon desklet** — отдельный виджет на рабочем столе, ходит к openvpn3 через CLI того же бинаря.

Дальше — только TUI.

---

## Главные экраны

### 1. List (домашний экран)

Двухпанельный layout:
- **Слева — таблица профилей** (Box с pink-pink borderLt):
  - Header строка: `# CC NAME AUTH PROTO LAST` (FgSubtle)
  - Колонки: gutter(1) + status(●/○) + idx [N] + CC код + fav-слот(* для favorite) + NAME (flex) + AUTH + PROTO + LAST (relative time)
  - Selected row: `▎` gutter + Panel2 фон
  - Connected: Mint name + ● Mint dot
  - При width < 64: AUTH/PROTO сворачиваются
- **Справа — Detail box** (purple border):
  - status pill (CONNECTED / DISCONNECTED)
  - kv-пары: host (—), cipher (—), auth (—), country, favorite, auto, last (3 поля — заглушки на сегодня)

**HeaderBar:** `[ovpn3] / profiles` + правый край два pill: `● N active` или `○ idle`, `N configs`.

**Footer (минималистичный):** `↑↓ nav · ⏎ open · / find · ? help · q quit`
Полный набор клавиш — в `?` overlay поверх экрана:
- глобальные: ↑↓/jk nav, ⏎ open/activate, esc back, ? toggle, q/ctrl+c quit
- list-специфичные: `/` filter, `d` disconnect, `e` edit, `f` favorite toggle, `i` import (.ovpn / .o3ui.json), `X` export → JSON, `R` rename, `,` settings, `r` reload

**Inline-режимы внутри list:**
- **filter (`/`)** — swallow-mode, печатаешь подстроку, фильтрует по NAME. Внизу строка `filter › <stock>_` + counter `1/3 matches`.
- **rename (`R`)** — то же, но pink, prefill текущим именем. Enter → svc.RenameConfig (openvpn3 SetProperty .name). Esc отмена.

**Flash-banner** — transient статус-строка между body и footer, auto-clear 6 сек. Используется для результатов import/export/rename/disconnect. Зелёный (Mint) на успех, красный на ошибку.

**Реалтайм:** подписка на D-Bus signals (StatusChange, SessionCreated/Destroyed, ConfigCreated/Destroyed) → автоматический reload без нажатия `r`.

---

### 2. Connecting

Запускается при Enter на idle-профиле:
- Header: `[ovpn3] / connecting` + pill `⟳ <short-session-path>` + status pill `AUTH PENDING/CONNECTED/FAILED`.
- StatusBox (Pink-Purple борд) с title `◆ <profile-name>`:
  - spinner (ASCII `|/-\`) + progress bar (косметика, считается из времени) + pill `XX%` + pill state
  - steps: `session / auth / tunnel` (визуализация фазы)
- LogBox — пока статичная заглушка, в TODO: подписаться на per-session Log signal через LogForward.
- Footer: `esc cancel · q quit`.
- Esc → cancel context, прерывает Service.Connect.

При входе auth-challenge openvpn3 (UserInput pending) — переключается на:

### 3. Auth modal

Поверх connecting:
- Header `[ovpn3] / authenticate` + pill `🔐 prompt`.
- Modal box: prompt name + description, textinput (с EchoPassword если поле password), `[✓] remember` чекбокс (Tab toggle).
- Submit → пишет в reply-канал, который связывает блокирующий Service.Connect и event loop, плюс если remember — Service.RememberUsername/Password.
- Footer: `enter authenticate · tab toggle remember · esc cancel`.

### 4. Connected (live)

Куда попадаешь после успешного коннекта (или Enter на active profile в list):
- Header: pills `● <device>` (Mint), `↓ <rate>`, `↑ <rate>`, `uptime HH:MM:SS`
- StatBox (Mint glow): title `◆ <session-name> CONNECTED`, content — 4 stat-cells (tunnel / session / down / up) с label + value + sub
- ThroughputBox (Pink ▎): легенда «━━ down / ━━ up», два sparkline-блока через `▁▂▃▄▅▆▇█`, timeline `−60s ... now`
- TunnelBox (kv: device/session/config/status/created) + DBusSessionBox (full session path) — рядом, левый/правый
- Footer: `d disconnect · q hide`

### 5. Edit (per-profile, по `e`)

Header `[ovpn3] / edit` + path pill.
Сайдбар с табами (`general / network / advanced / raw .ovpn` — декорация на сегодня, все табы кроме credentials/TOTP пустые).
Основная область — authentication tab:
- Credentials block: kv username (нажми `u` чтобы редактировать), kv password (`p` — masked textinput, скрытый ввод), `d` clear creds.
- OTP block:
  - если установлен: live 6-digit code + countdown (секунды до next), `x` remove (с Y/N подтверждением)
  - если нет: `i` open import screen, `m` manual base32

Footer: `u username · p password · d clear creds | i import OTP · m manual base32 · x remove OTP · q/esc back`.

### 6. OTP import (по `i` из edit)

Header pill `two-factor`.
3 таба:
- **URI** — paste otpauth:// или otpauth-migration:// (Google Authenticator bulk export)
- **QR** — file picker для PNG/JPEG с QR-кодом
- **Manual** — base32 input
Если URI содержит multiple accounts → переключается на picker-таб с выбором конкретного аккаунта.
Tab переключает табы, Enter commit, Esc back.

### 7. Profile import (по `i` из list)

Filepicker (`$HOME` стартует).
Sniff содержимого:
- JSON с `version` + `config` → portable bundle (наш формат `.o3ui.json`), Service.ImportPortable восстанавливает overlay + credentials + TOTP secret
- иначе — raw .ovpn → Service.ImportFromFile
Hint текст под header'ом: «pick a raw .ovpn config or a .o3ui.json portable bundle — format detected automatically».
По завершении → BackMsg + FlashMsg в list.

### 8. Settings (по `,`)

Header pill `● net.openvpn.v3`.
- Backend services table: список сервисов openvpn3 на шине, PID, uptime из `/proc/<pid>/stat`, state pill.
- Log verbosity row: 6 pills `1-6` (1=fatal..6=verbose), активный pink.
- D-Bus paths листинг.
Footer: `1-6 log level · r refresh · q/esc back`.

---

## Главные сценарии

### S1. Первое использование
1. `apt install ./o3ui_X.Y.Z_amd64.deb` → бинарь в `/usr/bin`, completions, десклет в `/usr/share/cinnamon/desklets`.
2. `o3ui` → пустой list, footer hint «(no profiles — press i to import)».
3. `i` → filepicker, выбрать `client.ovpn` → flash `✓ imported client (.ovpn)`.
4. Enter на профиле → connecting → auth modal (если нужен) → connected.

### S2. Daily VPN-toggle через десклет
- Кликаешь на десклет «Connect to FarzoomVpn» (favorite автоматически подсвечен) → CLI `o3ui connect` под капотом → auth chain (stored creds + auto-TOTP) → connected. Sparkline в десклете рисует трафик.

### S3. Перенос профиля между машинами
- Машина A: list → `X` на профиле → `~/<name>.o3ui.json` (0600, plaintext).
- Скопировал файл (scp, gpg --encrypt+send, флешка).
- Машина B: `o3ui` → `i` → filepicker → выбор `.o3ui.json` → восстановились credentials, TOTP secret, favorite-флаг.

### S4. Настройка TOTP под существующий профиль
- `e` на профиле → edit screen → `i` → OTP import → URI/QR/Manual → save → возврат в edit → live код показывается.

### S5. Troubleshooting
- В list `,` → settings → видишь, что `net.openvpn.v3.sessions` не running → можно `1-6` поднять log level → перезайти и смотреть log в connecting screen (когда live log будет реализован).

---

## Что точно есть

- Filter, rename, favorite toggle (`f`), export, dual-format import, edit screen с credentials/OTP, OTP import 3-tab, settings, connecting/connected с реалтаймом, anti-flicker re-render в десклете, `?` overlay.
- Auth chain: stored creds → auto-TOTP → interactive prompt. Reusable как UI, так и CLI.
- D-Bus signals для realtime updates.
- Sampler для throughput (1Hz, ring buffer 60 samples).
- Atomic writes для status cache и desklet settings.
- Context cancellation на Esc и timeout.
- Cinnamon desklet с 8 view kinds, Tweener pulse/progress.

## Что заведомо отсутствует / в TODO

- Edit-табы (general/network/advanced/raw .ovpn) — декорация, пустые.
- Live D-Bus log на connecting screen — пока статичный.
- Backend service `restart` action в settings — кнопка есть, не подключена.
- Public IP / latency на connected screen — `—` заглушка.
- Auto-connect on login — overlay-флаг есть, runner нет.
- `.ovpn` parsing на import — поля host/cipher/auth в Detail Box не наполняются (parser есть).
- Webcam QR (только file-based QR).
- Удаление профиля из TUI (есть только в CLI / openvpn3 config-remove).
- Подтверждение перед destructive actions.
- Help system / справочник за пределами ?-overlay.
- Сохранение window-size / cursor position между запусками.
- Tab/Enter навигация на сабтабах в edit (декорация).

## Принципы, которые соблюдаем

- ASCII-safe glyphs (без emoji в hot path), `›` 1-cell вместо wide `❯`.
- Один шрифт (терминальный), без кастомных fonts.
- Реалтайм без polling-loop (опираемся на D-Bus signals).
- Auth-цепочка проходит как для интерактивного пользователя, так и для headless (CLI/десклет).
- 0 issues в golangci-lint, race-tests зелёные.
