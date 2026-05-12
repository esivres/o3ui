package ovpn

import (
	"fmt"
	"time"

	"github.com/godbus/dbus/v5"
)

// BusObject is the minimal surface of dbus.BusObject we depend on.
// Keeping it narrow makes mocking trivial.
type BusObject interface {
	Call(method string, flags dbus.Flags, args ...interface{}) *dbus.Call
	GetProperty(p string) (dbus.Variant, error)
	SetProperty(p string, v interface{}) error
	Path() dbus.ObjectPath
}

// Conn abstracts the parts of *dbus.Conn we actually use.
type Conn interface {
	Object(dest string, path dbus.ObjectPath) BusObject
	// BareObject returns the bus object *without* the retry decorator —
	// EnsureService needs this so its own polling loop doesn't get an
	// extra retry layer underneath, multiplying the delay on missing
	// services.
	BareObject(dest string, path dbus.ObjectPath) BusObject
	AddMatchSignal(options ...dbus.MatchOption) error
	RemoveMatchSignal(options ...dbus.MatchOption) error
	Signal(ch chan<- *dbus.Signal)
	RemoveSignal(ch chan<- *dbus.Signal)
	Close() error
}

// systemBus wraps a real *dbus.Conn to satisfy Conn.
type systemBus struct {
	c *dbus.Conn
}

func (s *systemBus) Object(dest string, path dbus.ObjectPath) BusObject {
	// Wrap with the retry decorator so cold-start "Object does not exist"
	// races against openvpn3 activation become invisible to callers.
	return retryingObject{inner: s.c.Object(dest, path)}
}

func (s *systemBus) BareObject(dest string, path dbus.ObjectPath) BusObject {
	return s.c.Object(dest, path)
}

func (s *systemBus) AddMatchSignal(options ...dbus.MatchOption) error {
	return s.c.AddMatchSignal(options...)
}

func (s *systemBus) RemoveMatchSignal(options ...dbus.MatchOption) error {
	return s.c.RemoveMatchSignal(options...)
}

func (s *systemBus) Signal(ch chan<- *dbus.Signal)       { s.c.Signal(ch) }
func (s *systemBus) RemoveSignal(ch chan<- *dbus.Signal) { s.c.RemoveSignal(ch) }
func (s *systemBus) Close() error                        { return s.c.Close() }

// ConnectSystemBus opens a connection to the system bus where openvpn3 lives.
func ConnectSystemBus() (Conn, error) {
	c, err := dbus.ConnectSystemBus()
	if err != nil {
		return nil, err
	}
	return &systemBus{c: c}, nil
}

// EnsureService asks the bus to (re)activate a well-known name and then
// waits for the manager object to be reachable. openvpn3 services are
// activation-on-demand: StartServiceByName returns as soon as the *bus
// name* is owned, but the service can still be busy registering its
// objects, so an immediate method call may fail with
// "Object does not exist at path".
//
// We close the gap by polling Introspect on the manager path with a short
// exponential backoff. Idempotent — calling it before every operation costs
// at most one round-trip in the warm path.
func EnsureService(c Conn, name string, path dbus.ObjectPath) error {
	// Bare object on org.freedesktop.DBus — StartServiceByName itself can
	// return ServiceUnknown, and that error is in our transient list, so
	// going through the retrying decorator would add 1.5s of pointless
	// backoff on every machine that doesn't have openvpn3 installed.
	// EnsureService already runs its own polling loop below.
	bus := c.BareObject("org.freedesktop.DBus", "/org/freedesktop/DBus")
	var reply uint32
	if err := bus.Call(
		"org.freedesktop.DBus.StartServiceByName",
		0, name, uint32(0),
	).Store(&reply); err != nil {
		return fmt.Errorf("StartServiceByName %s: %w", name, err)
	}

	// Introspect-poll uses bare too — we want our own backoff schedule
	// to be authoritative, not a layered one.
	obj := c.BareObject(name, path)
	delay := 25 * time.Millisecond
	for attempt := 0; attempt < 8; attempt++ {
		var xml string
		err := obj.Call("org.freedesktop.DBus.Introspectable.Introspect", 0).Store(&xml)
		if err == nil && xml != "" {
			return nil
		}
		time.Sleep(delay)
		delay *= 2
	}
	return fmt.Errorf("EnsureService: %s did not register %s in time", name, path)
}

// D-Bus names for openvpn3 v3.
const (
	BusConfiguration = "net.openvpn.v3.configuration"
	BusSessions      = "net.openvpn.v3.sessions"
	BusLog           = "net.openvpn.v3.log"

	PathConfiguration = "/net/openvpn/v3/configuration"
	PathSessions      = "/net/openvpn/v3/sessions"

	IfaceConfigurationMgr = "net.openvpn.v3.configuration"
	IfaceConfiguration    = "net.openvpn.v3.configuration"
	IfaceSessionsMgr      = "net.openvpn.v3.sessions"
	IfaceSession          = "net.openvpn.v3.sessions"
)
