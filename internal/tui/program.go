// Package tui hosts the Bubble Tea program that implements the design.
// The Root model owns the screen-routing FSM: it instantiates each
// sub-screen on demand and forwards Update/View to the current one.
package tui

import (
	"errors"
	"os"
	"path/filepath"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/esivres/openvpn3ui/internal/app"
	"github.com/esivres/openvpn3ui/internal/tui/components"
	"github.com/esivres/openvpn3ui/internal/tui/screens/auth"
	"github.com/esivres/openvpn3ui/internal/tui/screens/connected"
	"github.com/esivres/openvpn3ui/internal/tui/screens/connecting"
	"github.com/esivres/openvpn3ui/internal/tui/screens/edit"
	importportable "github.com/esivres/openvpn3ui/internal/tui/screens/importprofile"
	"github.com/esivres/openvpn3ui/internal/tui/screens/list"
	"github.com/esivres/openvpn3ui/internal/tui/screens/otpimport"
	"github.com/esivres/openvpn3ui/internal/tui/screens/settings"
	"github.com/esivres/openvpn3ui/internal/tui/theme"
)

// Root is the top-level tea.Model. Sub-models emit transition messages
// (list.ActionMsg, connecting.DoneMsg/FailedMsg/CancelMsg,
// connected.DisconnectedMsg/BackMsg, auth.SubmitMsg/CancelMsg, plus the
// cross-goroutine promptRequest); Root catches them in Update() and
// swaps the current screen.
type Root struct {
	svc    *app.Service
	width  int
	height int

	current tea.Model

	// suspended is the previous screen kept around while an FSM-level
	// detour is on-screen (currently only otpimport — edit→i→otpimport
	// returns to edit, not list). Stays nil otherwise.
	suspended tea.Model

	// When an Auth modal is active it lives *alongside* current rather
	// than swapping it out — the screen behind keeps running and gets
	// rendered underneath, so the user retains the connecting context
	// (progress, status, log) while answering the prompt. Used to be
	// a full screen swap; that lost the user's place every time
	// openvpn3 asked for a TOTP.
	authActive   *auth.Model
	pendingReply chan promptReply
	pendingCfg   string
	pendingName  string

	// helpOverlay is toggled by `?`. While true, View overlays a key
	// reference card on top of the current screen — no FSM transition,
	// the underlying screen keeps running underneath.
	helpOverlay bool

	// pendingConfirm is the y/N modal state for destructive actions.
	// Until the user picks, all key input (except y/n/esc) is
	// swallowed; on `y` the saved Cmd fires.
	pendingConfirm *confirmRequest
}

// confirmRequest captures the text shown in the modal plus the Cmd to
// run on `y`. Cancellation just clears the request.
type confirmRequest struct {
	modal components.ConfirmModal
	onYes tea.Cmd
}

func NewRoot(svc *app.Service) *Root {
	return &Root{svc: svc, current: list.New(svc)}
}

func (m *Root) Init() tea.Cmd { return m.current.Init() }

func (m *Root) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	// Always handle quit + window resize at the root so every sub-screen
	// gets a consistent size and Ctrl+C works regardless of state.
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
	case tea.KeyMsg:
		if msg.String() == "ctrl+c" {
			return m, tea.Quit
		}
		// `?` is a global toggle for the help overlay. We swallow it
		// from the underlying screen so a profile name containing `?`
		// in the filter input still works — the auth modal owns the
		// `?` key in that mode (via filtMode in list.go etc.).
		if msg.String() == "?" && !m.isModalActive() {
			m.helpOverlay = !m.helpOverlay
			return m, nil
		}
		if m.helpOverlay {
			// Any key while overlay is shown closes it. Cheap and
			// discoverable — no need to remember the toggle key.
			m.helpOverlay = false
			return m, nil
		}
		if m.pendingConfirm != nil {
			switch msg.String() {
			case "y", "Y", "enter":
				cmd := m.pendingConfirm.onYes
				m.pendingConfirm = nil
				return m, cmd
			case "n", "N", "esc", "q":
				m.pendingConfirm = nil
				return m, nil
			}
			return m, nil
		}
	}

	// Transition messages — checked before forwarding so the originating
	// screen doesn't see its own goodbye signal.
	switch msg := msg.(type) {
	case list.ActionMsg:
		return m.handleListAction(msg)
	case connecting.DoneMsg:
		next := connected.New(m.svc, msg.SessionPath)
		next.SetSize(m.width, m.height)
		m.current = next
		return m, next.Init()
	case connecting.FailedMsg, connecting.CancelMsg:
		return m.gotoList()
	case connected.DisconnectedMsg, connected.BackMsg:
		return m.gotoList()
	case edit.BackMsg:
		return m.gotoList()
	case edit.OpenOTPImportMsg:
		next := otpimport.New(m.svc, msg.ConfigPath, msg.ConfigName)
		next.SetSize(m.width, m.height)
		m.suspended = m.current
		m.current = next
		return m, next.Init()
	case otpimport.BackMsg:
		// Restore the Edit screen if we came from it; otherwise list.
		if m.suspended != nil {
			m.current = m.suspended
			m.suspended = nil
			return m, m.current.Init()
		}
		return m.gotoList()
	case settings.BackMsg:
		return m.gotoList()
	case importportable.BackMsg:
		return m.gotoList()

	case promptRequest:
		return m.openAuthModal(msg)
	case auth.SubmitMsg:
		return m.resolveAuth(promptReply{Value: msg.Value}, msg.Remember)
	case auth.CancelMsg:
		return m.resolveAuth(promptReply{Err: errors.New("user cancelled auth")}, false)
	}

	// While the auth modal is up it owns the key stream — but other
	// messages (timers, tea.WindowSizeMsg, signal events) keep flowing
	// to the screen behind so it stays alive.
	if m.authActive != nil {
		if _, isKey := msg.(tea.KeyMsg); isKey {
			updated, cmd := m.authActive.Update(msg)
			if am, ok := updated.(*auth.Model); ok {
				m.authActive = am
			}
			return m, cmd
		}
	}

	updated, cmd := m.current.Update(msg)
	m.current = updated
	return m, cmd
}

// handleListAction takes the message by value — bubbletea dispatches
// every Msg through `Update(msg tea.Msg)` where the boxing already
// copies it, so the second copy into our handler is unavoidable
// without redesigning the interface above us.
//
//nolint:gocritic // hugeParam: bubbletea Msg dispatch is by-value by design
func (m *Root) handleListAction(a list.ActionMsg) (tea.Model, tea.Cmd) {
	switch a.Kind {
	case "connect":
		next := connecting.New(m.svc, a.Item.ConfigPath, a.Item.Name)
		next.SetSize(m.width, m.height)
		m.current = next
		return m, next.Init()
	case "disconnect":
		// Gate behind a y/N — `d` mashed by mistake on the active row
		// should not tear the tunnel down. The actual disconnect lives
		// in the onYes Cmd so it only runs after explicit confirmation.
		cfgPath := a.Item.ConfigPath
		name := a.Item.Name
		m.pendingConfirm = &confirmRequest{
			modal: components.ConfirmModal{
				Title:    "Disconnect " + name + "?",
				Body:     "The VPN tunnel will be torn down immediately.",
				YesLabel: "Disconnect",
				Danger:   true,
			},
			onYes: func() tea.Msg {
				if sessions, err := m.svc.ActiveSessions(); err == nil {
					for i := range sessions {
						if sessions[i].ConfigPath == cfgPath {
							_ = m.svc.Disconnect(sessions[i].Path)
							break
						}
					}
				}
				return list.FlashMsg{Text: "● disconnected · " + name}
			},
		}
		return m, nil
	case "view":
		// Re-enter the Connected screen for an existing live session
		// — useful after the user dismissed it with q/esc but the
		// tunnel is still up.
		if sessions, err := m.svc.ActiveSessions(); err == nil {
			for _, s := range sessions {
				if s.ConfigPath == a.Item.ConfigPath {
					next := connected.New(m.svc, s.Path)
					next.SetSize(m.width, m.height)
					m.current = next
					return m, next.Init()
				}
			}
		}
		// No active session found (raced with disconnect) — fall through
		// to a fresh list rather than freezing.
		return m.gotoList()
	case "edit":
		next := edit.New(m.svc, a.Item.ConfigPath, a.Item.Name)
		next.SetSize(m.width, m.height)
		m.current = next
		return m, next.Init()
	case "settings":
		next := settings.New(m.svc)
		next.SetSize(m.width, m.height)
		m.current = next
		return m, next.Init()
	case "delete":
		cfgPath := a.Item.ConfigPath
		name := a.Item.Name
		m.pendingConfirm = &confirmRequest{
			modal: components.ConfirmModal{
				Title:    "Delete profile " + name + "?",
				Body:     "Removes the profile from openvpn3 and clears saved username / password / TOTP secret. The .ovpn file you imported from is not touched.",
				YesLabel: "Delete",
				Danger:   true,
			},
			onYes: func() tea.Msg {
				if err := m.svc.RemoveConfig(cfgPath); err != nil {
					return list.FlashMsg{Text: "delete failed: " + err.Error(), IsError: true}
				}
				return list.FlashMsg{Text: "✗ deleted · " + name}
			},
		}
		return m, nil
	case "export":
		// Export writes the user's TOTP secret and password in
		// plaintext to a file. The y/N gate makes that consequence
		// visible at least once per session instead of dropping the
		// file silently on a single keystroke.
		cfgPath := a.Item.ConfigPath
		name := a.Item.Name
		m.pendingConfirm = &confirmRequest{
			modal: components.ConfirmModal{
				Title:    "Export " + name + "?",
				Body:     "Writes ~/" + name + ".o3ui.json (mode 0600) including TOTP secret and saved password in plaintext. Suitable for moving the profile to another machine; not for sharing.",
				YesLabel: "Export",
				Danger:   true,
			},
			onYes: m.exportProfile(cfgPath, name),
		}
		return m, nil
	case "import":
		// One filepicker screen, two formats — sniffs by content
		// (.ovpn vs .o3ui.json), dispatches accordingly.
		next := importportable.New(m.svc)
		next.SetSize(m.width, m.height)
		m.current = next
		return m, next.Init()
	case "quit-confirm":
		// `q` in list when there's an active tunnel. Ask first; a
		// silent process exit while VPN stays up is the classic
		// "wait, did it disconnect?" footgun.
		m.pendingConfirm = &confirmRequest{
			modal: components.ConfirmModal{
				Title:    "Quit while a tunnel is up?",
				Body:     "The VPN session keeps running in openvpn3 after o3ui exits. Press y to quit, n to stay.",
				YesLabel: "Quit",
				Danger:   false,
			},
			onYes: tea.Quit,
		}
		return m, nil
	}
	// Other actions (stats) — not yet routed.
	return m, nil
}

// exportProfile dumps the selected profile as a v1 portable bundle to
// $HOME and flashes the result back to the list screen. Synchronous —
// the work is one D-Bus Fetch plus a single write, fast enough that
// blocking the event loop here doesn't show.
func (m *Root) exportProfile(configPath, displayName string) tea.Cmd {
	return func() tea.Msg {
		p, err := m.svc.ExportProfile(configPath)
		if err != nil {
			return list.FlashMsg{Text: "export failed: " + err.Error(), IsError: true}
		}
		data, err := app.MarshalPortable(p)
		if err != nil {
			return list.FlashMsg{Text: "marshal failed: " + err.Error(), IsError: true}
		}
		dir, err := os.UserHomeDir()
		if err != nil {
			dir = os.TempDir()
		}
		fname := filepath.Join(dir, sanitiseFilename(displayName)+".o3ui.json")
		// 0600 so the file with credentials/TOTP secret is at least
		// not world-readable from the moment it lands.
		if err := os.WriteFile(fname, data, 0o600); err != nil {
			return list.FlashMsg{Text: "write failed: " + err.Error(), IsError: true}
		}
		return list.FlashMsg{Text: "✓ exported to " + fname}
	}
}

// sanitiseFilename strips characters that make a name awkward on disk.
// Replaces path separators and trims whitespace; otherwise the original
// is preserved so the file name still mirrors the profile name.
func sanitiseFilename(s string) string {
	r := strings.NewReplacer("/", "_", "\\", "_", ":", "_", "\x00", "")
	out := strings.TrimSpace(r.Replace(s))
	if out == "" {
		return "profile"
	}
	return out
}

func (m *Root) gotoList() (tea.Model, tea.Cmd) {
	l := list.New(m.svc)
	l.SetSize(m.width, m.height)
	m.current = l
	return m, l.Init()
}

// openAuthModal puts an auth.Model into authActive (layered overlay)
// without disturbing current. The previous screen behind keeps
// rendering, so the user sees the connecting context (progress bar,
// status, log) while typing their TOTP/password.
//
//nolint:gocritic // hugeParam: see handleListAction — bubbletea Msg dispatch is by-value
func (m *Root) openAuthModal(req promptRequest) (tea.Model, tea.Cmd) {
	m.pendingReply = req.Reply
	m.pendingCfg = req.ConfigPath
	m.pendingName = req.Prompt.Name

	// Pull a friendlier display name from the saved overlay if we have one.
	display := req.ConfigPath
	if cfgs, err := m.svc.ListConfigs(); err == nil {
		for _, c := range cfgs {
			if c.Path == req.ConfigPath {
				display = c.Name
				break
			}
		}
	}

	modal := auth.New(display, req.Prompt)
	modal.SetSize(m.width, m.height)
	m.authActive = modal
	return m, modal.Init()
}

// resolveAuth fires the reply back to the waiting goroutine, dismisses
// the modal, and lets the screen behind keep running uninterrupted.
// If "remember" was ticked, persist the value into overlay/keyring
// keyed by the prompt name.
func (m *Root) resolveAuth(rep promptReply, remember bool) (tea.Model, tea.Cmd) {
	if m.pendingReply != nil {
		m.pendingReply <- rep
		m.pendingReply = nil
	}
	if remember && rep.Err == nil {
		switch promptKind(m.pendingName) {
		case "user":
			_ = m.svc.RememberUsername(m.pendingCfg, rep.Value)
		case "pass":
			_ = m.svc.RememberPassword(m.pendingCfg, rep.Value)
		}
	}
	m.authActive = nil
	// No screen transition: current already holds the connecting (or
	// other) screen and it has kept ticking the whole time. Returning
	// nil from here lets the next event reach it naturally.
	return m, nil
}

func promptKind(name string) string {
	lower := lowerASCII(name)
	switch {
	case contains(lower, "user"):
		return "user"
	case contains(lower, "pass"):
		return "pass"
	default:
		return ""
	}
}

func lowerASCII(s string) string {
	b := make([]byte, len(s))
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c >= 'A' && c <= 'Z' {
			c += 'a' - 'A'
		}
		b[i] = c
	}
	return string(b)
}

func contains(s, sub string) bool {
	if len(sub) > len(s) {
		return false
	}
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}

func (m *Root) View() string {
	base := m.current.View()
	// Layered overlays in order of priority: confirm > auth > help.
	// The base view always renders so the screen behind shows through
	// — including realtime ticks while the user is staring at a modal.
	if m.pendingConfirm != nil {
		card := m.pendingConfirm.modal.Render(m.width)
		if m.width == 0 || m.height == 0 {
			return card
		}
		return lipgloss.Place(m.width, m.height, lipgloss.Center, lipgloss.Center, card)
	}
	if m.authActive != nil {
		card := m.authActive.View()
		if m.width == 0 || m.height == 0 {
			return card
		}
		// Keep the base screen's header strip visible at the top so
		// the user retains context (e.g. "ovpn3 / connecting · ⟳
		// session 3a8d…"). Centre the modal in the remaining height.
		// lipgloss has no proper layer compositor in v1, but this
		// header-plus-centre pattern is enough to stop the modal
		// from erasing the user's place.
		lines := strings.Split(base, "\n")
		topStrip := ""
		topH := 2
		if len(lines) > topH {
			topStrip = strings.Join(lines[:topH], "\n")
		}
		remaining := m.height - topH
		if remaining < lipgloss.Height(card) {
			remaining = lipgloss.Height(card)
		}
		centered := lipgloss.Place(m.width, remaining,
			lipgloss.Center, lipgloss.Center, card)
		if topStrip == "" {
			return centered
		}
		return topStrip + "\n" + centered
	}
	if !m.helpOverlay {
		return base
	}
	return m.renderHelpOverlay(base)
}

// isModalActive reports whether something is overlaid on top of the
// current screen that should own the `?` key (e.g. the auth prompt's
// free-text field must accept `?` as input rather than triggering
// the global help, and otpimport has its own filter input).
func (m *Root) isModalActive() bool {
	if m.authActive != nil || m.pendingConfirm != nil {
		return true
	}
	if _, ok := m.current.(*otpimport.Model); ok {
		return true
	}
	return false
}

// HelpKeyProvider lets a screen contribute its own keymap to the `?`
// overlay. Optional — Root falls back to a global-only list when the
// current screen doesn't implement it.
type HelpKeyProvider interface {
	HelpKeys() []components.KeyHelp
}

// renderHelpOverlay paints a centred key-reference card on top of the
// current screen. Always shows the global keys; appends per-screen
// keys when the current model implements HelpKeyProvider.
func (m *Root) renderHelpOverlay(base string) string {
	rows := []components.KeyHelp{
		{Key: "↑↓ / jk", Label: "navigate"},
		{Key: "⏎", Label: "open / activate"},
		{Key: "esc", Label: "back"},
		{Key: "?", Label: "toggle this help"},
		{Key: "q / ctrl+c", Label: "quit"},
	}
	if hp, ok := m.current.(HelpKeyProvider); ok {
		rows = append(rows, hp.HelpKeys()...)
	}

	var b strings.Builder
	b.WriteString(theme.AccentPurple.Render("◆ ") + theme.Bright.Render("Keys"))
	b.WriteString("\n\n")
	keyStyle := lipgloss.NewStyle().Foreground(theme.Purple).Bold(true).Width(12)
	labelStyle := lipgloss.NewStyle().Foreground(theme.Fg)
	for _, r := range rows {
		b.WriteString(keyStyle.Render(r.Key))
		b.WriteString(labelStyle.Render(r.Label))
		b.WriteString("\n")
	}
	b.WriteString("\n")
	b.WriteString(theme.Subtle.Render("press any key to dismiss"))

	card := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(theme.BorderLt).
		Padding(1, 3).
		Render(b.String())

	w, h := m.width, m.height
	if w == 0 || h == 0 {
		return card
	}
	return lipgloss.Place(w, h, lipgloss.Center, lipgloss.Center, card)
}

// Run starts the bubbletea program. If `prompter` is non-nil, its Send
// is bound to the program so Service.Connect can raise UserInput
// challenges into the modal screen. If `events` is non-nil, every event
// read from it is forwarded into the program as a tea.Msg, enabling
// real-time UI updates from D-Bus signals.
func Run(svc *app.Service, prompter *Prompter, events <-chan interface{}) error {
	p := tea.NewProgram(NewRoot(svc), tea.WithAltScreen())
	if prompter != nil {
		prompter.BindSend(p.Send)
	}
	if events != nil {
		go func() {
			for ev := range events {
				p.Send(ev)
			}
		}()
	}
	_, err := p.Run()
	return err
}
