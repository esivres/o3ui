package ovpn

import (
	"fmt"
	"time"

	"github.com/godbus/dbus/v5"
)

// Status mirrors openvpn3's (uus) status tuple. Codes are documented in
// openvpn3-linux's StatusEvent enum; we keep them as raw values plus the
// human-readable message so the UI can render either.
type Status struct {
	Major   uint32
	Minor   uint32
	Message string
}

// Connection state minor codes under Major == StatusMajorConnection.
// See openvpn3-linux/src/log/log-helpers.hpp (CONN_*).
const (
	StatusMajorConnection uint32 = 2

	StatusConnConnecting uint32 = 1
	StatusConnConnected  uint32 = 7
	StatusConnDisconnect uint32 = 9
	StatusConnAuthFailed uint32 = 5
)

// IsConnected reports whether the session is currently established.
func (s Status) IsConnected() bool {
	return s.Major == StatusMajorConnection && s.Minor == StatusConnConnected
}

// IsActive reports whether the session is in any "live" connection state —
// either currently connected or actively connecting. The UI treats these
// the same: the user cannot Connect on top of one, only Disconnect (or
// cancel the in-flight attempt).
//
// Returning false here is the right answer for sessions left lingering
// after Disconnect — openvpn3 does not always remove the session object
// immediately, so a (2, StatusConnDisconnect) row would otherwise look
// "active" to the UI and freeze the buttons in the wrong state.
func (s Status) IsActive() bool {
	if s.Major != StatusMajorConnection {
		return false
	}
	return s.Minor != StatusConnDisconnect
}

// Session is a snapshot of an openvpn3 session's properties.
type Session struct {
	Path        string
	ConfigName  string
	ConfigPath  string
	DeviceName  string
	SessionName string
	Status      Status
	CreatedAt   time.Time
}

// SessionManager talks to net.openvpn.v3.sessions.
type SessionManager struct {
	conn Conn
}

func NewSessionManager(c Conn) *SessionManager {
	return &SessionManager{conn: c}
}

// NewTunnel creates a session tied to the given configuration path and
// returns the resulting session D-Bus path. The session is *not* connected
// yet — call SessionController.Ready/Connect afterwards.
func (m *SessionManager) NewTunnel(configPath string) (string, error) {
	if err := EnsureService(m.conn, BusSessions, PathSessions); err != nil {
		return "", err
	}
	mgr := m.conn.Object(BusSessions, PathSessions)
	var path dbus.ObjectPath
	if err := mgr.Call(IfaceSessionsMgr+".NewTunnel", 0, dbus.ObjectPath(configPath)).Store(&path); err != nil {
		return "", fmt.Errorf("NewTunnel: %w", err)
	}
	return string(path), nil
}

// List returns every session the caller can see.
func (m *SessionManager) List() ([]Session, error) {
	if err := EnsureService(m.conn, BusSessions, PathSessions); err != nil {
		return nil, err
	}
	mgr := m.conn.Object(BusSessions, PathSessions)
	var paths []dbus.ObjectPath
	if err := mgr.Call(IfaceSessionsMgr+".FetchAvailableSessions", 0).Store(&paths); err != nil {
		return nil, fmt.Errorf("FetchAvailableSessions: %w", err)
	}

	out := make([]Session, 0, len(paths))
	for _, p := range paths {
		s, err := m.Get(string(p))
		if err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, nil
}

// Get reads a session's current property snapshot.
func (m *SessionManager) Get(path string) (Session, error) {
	obj := m.conn.Object(BusSessions, dbus.ObjectPath(path))
	s := Session{Path: path}

	getString := func(prop string) (string, error) {
		v, err := obj.GetProperty(IfaceSession + "." + prop)
		if err != nil {
			return "", fmt.Errorf("get %s: %w", prop, err)
		}
		if x, ok := v.Value().(string); ok {
			return x, nil
		}
		return "", nil
	}

	var err error
	if s.ConfigName, err = getString("config_name"); err != nil {
		return s, err
	}
	if cp, err := obj.GetProperty(IfaceSession + ".config_path"); err == nil {
		if op, ok := cp.Value().(dbus.ObjectPath); ok {
			s.ConfigPath = string(op)
		}
	}
	// device_name exists only after the tunnel device is created;
	// session_name is set by the daemon at session create. Both are
	// best-effort — empty values are fine and missing properties are
	// the common case for sessions still in handshake.
	s.DeviceName, _ = getString("device_name")
	s.SessionName, _ = getString("session_name")
	if cs, err := obj.GetProperty(IfaceSession + ".session_created"); err == nil {
		if t, ok := cs.Value().(uint64); ok {
			s.CreatedAt = time.Unix(int64(t), 0)
		}
	}
	if st, err := obj.GetProperty(IfaceSession + ".status"); err == nil {
		s.Status = unmarshalStatus(st)
	}
	return s, nil
}

func unmarshalStatus(v dbus.Variant) Status {
	// (uus) — variant of struct. Decode straight into a Status so the
	// struct-conversion linter doesn't need a tuple+copy.
	var s Status
	if err := dbus.Store([]interface{}{v.Value()}, &s); err == nil {
		return s
	}
	return Status{}
}

// SessionController exposes per-session methods on a known D-Bus path.
type SessionController struct {
	conn Conn
	path string
}

// Control returns a controller for the session at the given path.
func (m *SessionManager) Control(path string) *SessionController {
	return &SessionController{conn: m.conn, path: path}
}

func (sc *SessionController) call(method string, args ...interface{}) error {
	obj := sc.conn.Object(BusSessions, dbus.ObjectPath(sc.path))
	if err := obj.Call(IfaceSession+"."+method, 0, args...).Store(); err != nil {
		return fmt.Errorf("%s on %s: %w", method, sc.path, err)
	}
	return nil
}

// InputPrompt describes a single pending UserInput slot on a session: the
// (type, group, id) tuple identifies it for UserInputProvide; name and
// description are human-readable hints (e.g. name="username",
// "static_challenge"). Hidden marks values that must be masked in UI.
type InputPrompt struct {
	SessionPath string
	Type        uint32
	Group       uint32
	ID          uint32
	Name        string
	Description string
	Hidden      bool
}

// Ready signals that all required input (creds, OTP) has been provided and
// the session may proceed. openvpn3 expects this before Connect when there
// are pending UserInput queues.
func (sc *SessionController) Ready() error      { return sc.call("Ready") }
func (sc *SessionController) Connect() error    { return sc.call("Connect") }
func (sc *SessionController) Disconnect() error { return sc.call("Disconnect") }
func (sc *SessionController) Restart() error    { return sc.call("Restart") }
func (sc *SessionController) Resume() error     { return sc.call("Resume") }
func (sc *SessionController) Pause(reason string) error {
	return sc.call("Pause", reason)
}

// LogVerbosity reads the session's current log verbosity (0..6 in
// openvpn3 v27 terms). Returns 0 with an error if the property is missing.
func (sc *SessionController) LogVerbosity() (uint32, error) {
	obj := sc.conn.Object(BusSessions, dbus.ObjectPath(sc.path))
	v, err := obj.GetProperty(IfaceSession + ".log_verbosity")
	if err != nil {
		return 0, fmt.Errorf("get log_verbosity on %s: %w", sc.path, err)
	}
	if x, ok := v.Value().(uint32); ok {
		return x, nil
	}
	return 0, fmt.Errorf("unexpected log_verbosity type %T", v.Value())
}

// SetLogVerbosity changes the per-session log verbosity. openvpn3 enforces
// the range; we pass through whatever the caller gave us.
func (sc *SessionController) SetLogVerbosity(level uint32) error {
	obj := sc.conn.Object(BusSessions, dbus.ObjectPath(sc.path))
	if err := obj.SetProperty(IfaceSession+".log_verbosity", level); err != nil {
		return fmt.Errorf("set log_verbosity on %s: %w", sc.path, err)
	}
	return nil
}

// Statistics reads the openvpn3 `statistics` property — a dict-like
// snapshot of byte and packet counters maintained by the backend. Keys
// observed in v27 include BYTES_IN/OUT, PACKETS_IN/OUT, TUN_BYTES_IN/OUT,
// TUN_PACKETS_IN/OUT. Missing keys silently map to zero in callers; we
// never invent values.
func (sc *SessionController) Statistics() (map[string]int64, error) {
	obj := sc.conn.Object(BusSessions, dbus.ObjectPath(sc.path))
	v, err := obj.GetProperty(IfaceSession + ".statistics")
	if err != nil {
		return nil, fmt.Errorf("get statistics on %s: %w", sc.path, err)
	}
	raw, ok := v.Value().(map[string]int64)
	if !ok {
		// godbus may unmarshal a{sx} as map[string]dbus.Variant in some
		// versions; handle both.
		if vm, ok2 := v.Value().(map[string]dbus.Variant); ok2 {
			out := make(map[string]int64, len(vm))
			for k, vv := range vm {
				if x, ok3 := vv.Value().(int64); ok3 {
					out[k] = x
				}
			}
			return out, nil
		}
		return nil, fmt.Errorf("unexpected statistics type %T", v.Value())
	}
	return raw, nil
}

// PendingInputs returns every queued UserInput slot the session still needs.
// Empty slice means Ready() should now succeed.
func (sc *SessionController) PendingInputs() ([]InputPrompt, error) {
	obj := sc.conn.Object(BusSessions, dbus.ObjectPath(sc.path))

	// Signature is a(uu) — array of (type, group) structs.
	var groups []struct {
		Type, Group uint32
	}
	if err := obj.Call(IfaceSession+".UserInputQueueGetTypeGroup", 0).Store(&groups); err != nil {
		return nil, fmt.Errorf("UserInputQueueGetTypeGroup: %w", err)
	}

	var prompts []InputPrompt
	for _, tg := range groups {
		var ids []uint32
		if err := obj.Call(IfaceSession+".UserInputQueueCheck", 0, tg.Type, tg.Group).Store(&ids); err != nil {
			return nil, fmt.Errorf("UserInputQueueCheck(%d,%d): %w", tg.Type, tg.Group, err)
		}
		for _, id := range ids {
			var (
				typ, grp, gotID   uint32
				name, description string
				hidden            bool
			)
			err := obj.Call(IfaceSession+".UserInputQueueFetch", 0, tg.Type, tg.Group, id).
				Store(&typ, &grp, &gotID, &name, &description, &hidden)
			if err != nil {
				return nil, fmt.Errorf("UserInputQueueFetch(%d,%d,%d): %w", tg.Type, tg.Group, id, err)
			}
			prompts = append(prompts, InputPrompt{
				SessionPath: sc.path,
				Type:        typ,
				Group:       grp,
				ID:          gotID,
				Name:        name,
				Description: description,
				Hidden:      hidden,
			})
		}
	}
	return prompts, nil
}

// ProvideInput answers a single InputPrompt with the given value.
func (sc *SessionController) ProvideInput(p InputPrompt, value string) error {
	obj := sc.conn.Object(BusSessions, dbus.ObjectPath(sc.path))
	err := obj.Call(IfaceSession+".UserInputProvide", 0, p.Type, p.Group, p.ID, value).Store()
	if err != nil {
		return fmt.Errorf("UserInputProvide(%s/%d): %w", p.Name, p.ID, err)
	}
	return nil
}
