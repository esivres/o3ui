package list

import (
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/stretchr/testify/require"

	"github.com/esivres/openvpn3ui/internal/app"
	"github.com/esivres/openvpn3ui/internal/ovpn"
)

// stubBackend lets us drive the list screen against a known set of
// configs/sessions without spinning up D-Bus or SQLite.
type stubBackend struct {
	configs []ovpn.Config
}

func (s *stubBackend) List() ([]ovpn.Config, error) { return s.configs, nil }
func (s *stubBackend) Import(string, string, bool) (string, error) {
	return "", nil
}
func (s *stubBackend) Remove(string) error          { return nil }
func (s *stubBackend) Fetch(string) (string, error) { return "", nil }

type stubSessions struct{ list []ovpn.Session }

func (s *stubSessions) List() ([]ovpn.Session, error) { return s.list, nil }
func (s *stubSessions) NewTunnel(string) (string, error) {
	return "", nil
}
func (s *stubSessions) Control(string) app.SessionControl { return stubCtl{} }

type stubCtl struct{}

func (stubCtl) Ready() error                                { return nil }
func (stubCtl) Connect() error                              { return nil }
func (stubCtl) Disconnect() error                           { return nil }
func (stubCtl) PendingInputs() ([]ovpn.InputPrompt, error)  { return nil, nil }
func (stubCtl) ProvideInput(ovpn.InputPrompt, string) error { return nil }
func (stubCtl) Statistics() (map[string]int64, error)       { return nil, nil }
func (stubCtl) LogVerbosity() (uint32, error)               { return 0, nil }
func (stubCtl) SetLogVerbosity(uint32) error                { return nil }

func newModel(t *testing.T, configs []ovpn.Config, active []ovpn.Session) *Model {
	t.Helper()
	cfg := &stubBackend{configs: configs}
	sess := &stubSessions{list: active}
	svc := app.New(cfg, sess)
	m := New(svc)
	m.SetSize(120, 40)
	// Init returns a Cmd that fetches; run it synchronously and apply.
	msg := m.Init()()
	m.Update(msg)
	return m
}

func TestList_LoadsConfigsAndMarksActiveSession(t *testing.T) {
	m := newModel(t,
		[]ovpn.Config{
			{Path: "/c/a", Name: "Frankfurt", Valid: true},
			{Path: "/c/b", Name: "Stockholm", Valid: true},
		},
		[]ovpn.Session{{
			ConfigPath: "/c/a",
			Status:     ovpn.Status{Major: ovpn.StatusMajorConnection, Minor: ovpn.StatusConnConnected},
		}},
	)
	require.Len(t, m.items, 2)
	require.True(t, m.items[0].HasSession, "session on /c/a must be reflected")
	require.False(t, m.items[1].HasSession)
}

func TestList_FilterFiltersByName(t *testing.T) {
	m := newModel(t,
		[]ovpn.Config{
			{Path: "/c/a", Name: "Frankfurt"},
			{Path: "/c/b", Name: "Stockholm"},
			{Path: "/c/c", Name: "Tokyo"},
		},
		nil,
	)
	// Enter filter mode and type "stock".
	m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("/")})
	for _, r := range "stock" {
		m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
	}
	require.Equal(t, "stock", m.filter)
	visible := m.visible()
	require.Len(t, visible, 1)
	require.Equal(t, "Stockholm", m.items[visible[0]].Name)
}

// runCmd evaluates a tea.Cmd and returns the message it produces, or nil.
// Hides the func()-msg unwrap inside so tests stay readable.
func runCmd(c tea.Cmd) tea.Msg {
	if c == nil {
		return nil
	}
	return c()
}

func TestList_EnterEmitsConnectOrDisconnect(t *testing.T) {
	m := newModel(t,
		[]ovpn.Config{
			{Path: "/c/a", Name: "A"},
			{Path: "/c/b", Name: "B"},
		},
		[]ovpn.Session{{
			ConfigPath: "/c/b",
			Status:     ovpn.Status{Major: ovpn.StatusMajorConnection, Minor: ovpn.StatusConnConnected},
		}},
	)

	// Cursor on A → Enter must emit ActionMsg{Kind: connect}.
	_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	msg := runCmd(cmd)
	act, ok := msg.(ActionMsg)
	require.True(t, ok, "expected ActionMsg, got %T", msg)
	require.Equal(t, "connect", act.Kind)
	require.Equal(t, "A", act.Item.Name)

	// Move down to B (active session) → Enter opens the live view; the
	// destructive disconnect is bound to the dedicated 'd' key so users
	// can't tear a tunnel down by accident.
	m.Update(tea.KeyMsg{Type: tea.KeyDown})
	_, cmd = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	act = runCmd(cmd).(ActionMsg)
	require.Equal(t, "view", act.Kind)
	require.Equal(t, "B", act.Item.Name)

	// 'd' on B emits disconnect; 'd' on a disconnected row is a no-op.
	_, cmd = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'d'}})
	act = runCmd(cmd).(ActionMsg)
	require.Equal(t, "disconnect", act.Kind)
	require.Equal(t, "B", act.Item.Name)

	m.Update(tea.KeyMsg{Type: tea.KeyUp}) // back to A (idle)
	_, cmd = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'d'}})
	require.Nil(t, runCmd(cmd), "'d' on idle profile must be a no-op")
}
