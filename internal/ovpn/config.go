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
