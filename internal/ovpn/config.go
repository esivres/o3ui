package ovpn

import (
	"fmt"

	"github.com/godbus/dbus/v5"
)

// Config is a snapshot of an openvpn3 configuration profile.
type Config struct {
	Path  string
	Name  string
	Valid bool
}

// ConfigManager talks to net.openvpn.v3.configuration.
type ConfigManager struct {
	conn Conn
}

func NewConfigManager(c Conn) *ConfigManager {
	return &ConfigManager{conn: c}
}

// List returns all configurations the current user can see.
func (m *ConfigManager) List() ([]Config, error) {
	if err := EnsureService(m.conn, BusConfiguration, PathConfiguration); err != nil {
		return nil, err
	}
	mgr := m.conn.Object(BusConfiguration, PathConfiguration)
	var paths []dbus.ObjectPath
	if err := mgr.Call(IfaceConfigurationMgr+".FetchAvailableConfigs", 0).Store(&paths); err != nil {
		return nil, fmt.Errorf("FetchAvailableConfigs: %w", err)
	}

	out := make([]Config, 0, len(paths))
	for _, p := range paths {
		cfg, err := m.fetch(p)
		if err != nil {
			return nil, err
		}
		out = append(out, cfg)
	}
	return out, nil
}

func (m *ConfigManager) fetch(p dbus.ObjectPath) (Config, error) {
	obj := m.conn.Object(BusConfiguration, p)
	c := Config{Path: string(p)}

	name, err := obj.GetProperty(IfaceConfiguration + ".name")
	if err != nil {
		return c, fmt.Errorf("get name for %s: %w", p, err)
	}
	if s, ok := name.Value().(string); ok {
		c.Name = s
	}

	valid, err := obj.GetProperty(IfaceConfiguration + ".valid")
	if err != nil {
		return c, fmt.Errorf("get valid for %s: %w", p, err)
	}
	if b, ok := valid.Value().(bool); ok {
		c.Valid = b
	}
	return c, nil
}

// Fetch returns the original .ovpn body the configuration was imported
// with. openvpn3's configuration manager preserves the source text on
// disk so the user can round-trip a profile through their backups.
func (m *ConfigManager) Fetch(path string) (string, error) {
	obj := m.conn.Object(BusConfiguration, dbus.ObjectPath(path))
	var body string
	if err := obj.Call(IfaceConfiguration+".Fetch", 0).Store(&body); err != nil {
		return "", fmt.Errorf("Fetch %s: %w", path, err)
	}
	return body, nil
}

// Rename changes the display name of a configuration. openvpn3 exposes
// the name as a writable property on each config object, so this is a
// single SetProperty call — the new value persists across runs because
// our profiles are imported with persistent=true.
func (m *ConfigManager) Rename(path, newName string) error {
	if newName == "" {
		return fmt.Errorf("rename: empty name")
	}
	obj := m.conn.Object(BusConfiguration, dbus.ObjectPath(path))
	if err := obj.SetProperty(IfaceConfiguration+".name", newName); err != nil {
		return fmt.Errorf("Rename %s: %w", path, err)
	}
	return nil
}

// ConfigProperties is a read-only snapshot of the per-configuration
// flags openvpn3 exposes on each profile object. Field names mirror
// the D-Bus property names so the mapping stays obvious. ImportTs /
// LastUsedTs are unix seconds; zero values mean "never" / "unknown".
type ConfigProperties struct {
	Persistent          bool
	PublicAccess        bool
	LockedDown          bool
	DCO                 bool
	TransferOwnerSession bool
	UsedCount           uint32
	ImportTs            int64
	LastUsedTs          int64
}

// FetchProperties reads the well-known property bag from a config
// object. Missing/unsupported properties (older openvpn3 versions)
// are silently zeroed — we'd rather render "—" than crash the edit
// screen when the daemon is a release behind.
func (m *ConfigManager) FetchProperties(path string) (ConfigProperties, error) {
	obj := m.conn.Object(BusConfiguration, dbus.ObjectPath(path))
	out := ConfigProperties{}
	getBool := func(name string) bool {
		v, err := obj.GetProperty(IfaceConfiguration + "." + name)
		if err != nil {
			return false
		}
		b, _ := v.Value().(bool)
		return b
	}
	getU32 := func(name string) uint32 {
		v, err := obj.GetProperty(IfaceConfiguration + "." + name)
		if err != nil {
			return 0
		}
		x, _ := v.Value().(uint32)
		return x
	}
	getU64 := func(name string) int64 {
		v, err := obj.GetProperty(IfaceConfiguration + "." + name)
		if err != nil {
			return 0
		}
		if u, ok := v.Value().(uint64); ok {
			return int64(u)
		}
		return 0
	}
	out.Persistent = getBool("persistent")
	out.PublicAccess = getBool("public_access")
	out.LockedDown = getBool("locked_down")
	out.DCO = getBool("dco")
	out.TransferOwnerSession = getBool("transfer_owner_session")
	out.UsedCount = getU32("used_count")
	out.ImportTs = getU64("import_timestamp")
	out.LastUsedTs = getU64("last_used_timestamp")
	return out, nil
}

// SetBoolProperty writes one of the writable boolean flags on a
// configuration object. openvpn3 enforces "is this writable in your
// session" itself — we surface the D-Bus error as-is so the UI can
// explain why (e.g. "locked_down can only be set by the owner").
func (m *ConfigManager) SetBoolProperty(path, name string, value bool) error {
	obj := m.conn.Object(BusConfiguration, dbus.ObjectPath(path))
	if err := obj.SetProperty(IfaceConfiguration+"."+name, value); err != nil {
		return fmt.Errorf("set %s on %s: %w", name, path, err)
	}
	return nil
}

// Override is one entry in openvpn3's override list. Value is kept as
// a string for the UI surface — the daemon side stores it as a
// dbus.Variant; the wire conversion lives in SetOverride.
type Override struct {
	Name  string
	Value string
}

// Overrides returns the active overrides set on a config. openvpn3's
// `overrides` property is a{sv}; we flatten it to strings here so the
// UI doesn't have to learn about variants.
func (m *ConfigManager) Overrides(path string) ([]Override, error) {
	obj := m.conn.Object(BusConfiguration, dbus.ObjectPath(path))
	v, err := obj.GetProperty(IfaceConfiguration + ".overrides")
	if err != nil {
		return nil, fmt.Errorf("get overrides on %s: %w", path, err)
	}
	raw, ok := v.Value().(map[string]dbus.Variant)
	if !ok {
		// Some openvpn3 builds expose overrides as map[string]any of
		// already-unwrapped values. Tolerate both.
		if m2, ok2 := v.Value().(map[string]interface{}); ok2 {
			out := make([]Override, 0, len(m2))
			for k, vv := range m2 {
				out = append(out, Override{Name: k, Value: fmt.Sprintf("%v", vv)})
			}
			return out, nil
		}
		return nil, fmt.Errorf("unexpected overrides type %T", v.Value())
	}
	out := make([]Override, 0, len(raw))
	for k, vv := range raw {
		out = append(out, Override{Name: k, Value: fmt.Sprintf("%v", vv.Value())})
	}
	return out, nil
}

// SetOverride installs or replaces an override. openvpn3 SetOverride
// accepts (s name, v value); the variant type drives how the daemon
// stores it. For string-typed overrides (server-override, port-override
// as string, proto-override) we always wrap in a string variant —
// numeric ports are accepted as strings, openvpn3 parses them.
func (m *ConfigManager) SetOverride(path, name, value string) error {
	obj := m.conn.Object(BusConfiguration, dbus.ObjectPath(path))
	if err := obj.Call(IfaceConfiguration+".SetOverride", 0,
		name, dbus.MakeVariant(value)).Store(); err != nil {
		return fmt.Errorf("SetOverride %s on %s: %w", name, path, err)
	}
	return nil
}

// UnsetOverride removes one override. Idempotent on the daemon side;
// the error surfaces verbatim if the name was never set.
func (m *ConfigManager) UnsetOverride(path, name string) error {
	obj := m.conn.Object(BusConfiguration, dbus.ObjectPath(path))
	if err := obj.Call(IfaceConfiguration+".UnsetOverride", 0, name).Store(); err != nil {
		return fmt.Errorf("UnsetOverride %s on %s: %w", name, path, err)
	}
	return nil
}

// Remove deletes a configuration by its D-Bus path.
func (m *ConfigManager) Remove(path string) error {
	obj := m.conn.Object(BusConfiguration, dbus.ObjectPath(path))
	if err := obj.Call(IfaceConfiguration+".Remove", 0).Store(); err != nil {
		return fmt.Errorf("Remove %s: %w", path, err)
	}
	return nil
}

// Import sends a profile body to the configuration manager and returns the
// new D-Bus object path. `single_use=false`, `persistent=true` by default —
// imported configs survive across sessions like with `openvpn3 config-import`.
func (m *ConfigManager) Import(name, profile string, persistent bool) (string, error) {
	if err := EnsureService(m.conn, BusConfiguration, PathConfiguration); err != nil {
		return "", err
	}
	mgr := m.conn.Object(BusConfiguration, PathConfiguration)
	var path dbus.ObjectPath
	err := mgr.Call(
		IfaceConfigurationMgr+".Import",
		0, name, profile, false /*single_use*/, persistent,
	).Store(&path)
	if err != nil {
		return "", fmt.Errorf("Import: %w", err)
	}
	return string(path), nil
}
