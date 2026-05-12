# TODO

Active issues and follow-ups, by priority. Items move out of here into
git history when fixed.

## Sprint 2 — следующий рывок (по UX-ревью)

- [ ] **`.ovpn` parsing → list detail pane.** Parser живёт в
      `internal/ovpnconf`, `Service.ImportFromFile` уже знает тело.
      Распарсить на import, сложить host/proto/cipher/auth в overlay
      (новые колонки), surface через `Service.GetOverlay`. После этого
      правая панель в list перестаёт быть прочерками.
- [ ] **Edit: вернуть tab-bar с реально работающими вкладками.** Не
      возвращать пустые табы — только то, что несёт контент.
      Минимум:
      - **general** — имя (rename), country, favorite/auto-connect
        тоггл-чекбоксы, parsed `.ovpn`-поля (host/proto/cipher) из
        overlay. Консолидирует то что сейчас раскидано по
        хоткеям `R / f / `,` `.
      - **authentication** — текущий контент (creds + OTP).
      - **raw .ovpn** — read-only viewport, `Service.configs.Fetch`
        через bubbles/viewport. Полезно для debug-а конфига.
      `network / advanced` — отложены до спринта 4+, пока не выяснили
      какие per-config properties openvpn3 D-Bus реально отдаёт.
- [ ] **Settings: about-таб.** Version (мы прокидываем `-X
      main.version`/commit/date в ldflags), license, ссылка на
      github. ~10 строк, мгновенный win. `connection/general/network`
      пока пустые — не возвращать.
- [ ] **Убрать прогресс-бар на connecting.** Сейчас `elapsedPct`
      врёт юзеру: бар доезжает до 95% за 12с независимо от того что
      делает openvpn3. Оставить только spinner + step-индикаторы
      session/auth/tunnel — они честные.
- [ ] **Удалить дублирующие inline OTP-modes из edit** (`m` manual,
      `g` qr-path, modeEnterURI). Оставить только `i` → otpimport
      screen. Удаляет ~80 строк фрагментированной логики.
- [ ] **Modal auth поверх suspended screen**, а не fullscreen
      replace. Сейчас фон auth-modal затирает connecting-экран
      целиком — пользователь теряет контекст, кажется будто
      приложение перезапустилось.
- [ ] **Q vs q в list при активной сессии.** `q` мгновенно выходит
      даже если есть active tunnel. Использовать новый
      `components.ConfirmModal` для предупреждения, либо `Q` (Shift)
      для quit, `q` — no-op в list.

## Sprint 3 — wow-фичи

- [ ] **Live D-Bus log на Connecting screen.** Subscribe per-session
      `Log` signal (после `LogForward(true)`). Ring buffer на 10
      последних строк, авто-скролл, цветовые tag'и. Закрывает
      последнюю причину держать `openvpn3` CLI открытым параллельно.
- [ ] **Command palette `:` / `Ctrl+P`** с fuzzy-поиском по
      командам текущего экрана и всем профилям. `:conn farzoom<⏎>`
      коннектит. Игра-чейнджер для discoverability — gh dash /
      lazygit / vscode идиома.
- [ ] **Big-font ASCII TOTP preview** в edit-screen (figlet-style
      6-digit code занимает половину высоты box-а вместо мелких
      цифр в углу). ~50 строк rune-art.
- [ ] **Diagnostic next-steps на FailedMsg.** Парсить openvpn3
      error и предлагать действие: `AUTH_FAILED → press u/p to fix
      credentials`, `TLS handshake → check time skew / cert`,
      `Network unreachable → check internet`. Каждый mapping ≈ 2-3
      строки — приложение начинает «понимать».
- [ ] **Settings: connection-таб.** Дефолты подключения
      (auto-reconnect, дефолт log verbosity), когда auto-connect
      runner подключим.

## Sprint 4 — уют (можно отложить)

- [ ] **Public IP / latency** на Connected screen — currently «—».
      Probe через HTTPS к известному хосту через tun.
- [ ] **Auto-connect on login** — overlay-флаг есть, нет runner-а.
      Нужен systemd-user-service template + bin entrypoint.
- [ ] **Session history per profile** — ring buffer в overlay
      (последние 10 attempts: timestamp, duration, status, bytes).
      Показать в right pane как timeline.
- [ ] **Delete profile из TUI** (`D` с confirm). Сейчас только
      через `openvpn3 config-remove`.
- [ ] **`0..9` quick-jump на list** — выпрыгнуть на строку N.
      `[N]` индексы уже отрисованы — намёк, который надо закрыть.
- [ ] **Mouse-support** (`tea.WithMouseAllMotion`) — scroll,
      click на табы/строки. Одна строка, ощутимо для десктопа.
- [ ] **`edit:network / advanced`-табы** — нужна разведка
      openvpn3 D-Bus per-config properties API.
- [ ] **`settings:network`** — DNS/routing глобалы, если openvpn3
      такое экспортирует.

## TUI — visual / layout (отложенные дизайнерские)

- [ ] **Profile list box border barely visible.** Пробовали
      `theme.BorderLt`, всё ещё washed out на некоторых терминалах.
      Нужен brighter colour или другой border style — выбирать на
      Mint defaults.
- [ ] **Header row clipped на узких терминалах.** Reproduce +
      измерить `lipgloss.Height` каждой части.
- [ ] **Footer wraps на ~80 cols.** Частично закрыто `?`-overlay
      + ужатым footer'ом, но wide-glyph `↑↓ ⏎` всё ещё риск.

## Backend

- [ ] **TUI gracefully degrade on missing keyring.** Сейчас
      `secrets.New()` тихо Open-ится, но Set/Get падает per call;
      surface friendly "keyring locked" в Auth modal.
- [ ] **GORM + gorm-gen migration** для `internal/overlay` —
      отложено, пока схема ещё двигается.
- [ ] **Webcam QR import** — был skipped при OTP screen build.
      Не в v1, revisit когда будет запрос.
- [ ] **Settings: backend service `restart` action.** Кнопка
      показывается, не подключена. Нужна policy decision —
      требует polkit; либо реализовать, либо убрать колонку.

## Done — moved into git history

Items finalised (and removed from this list) live in git history.
