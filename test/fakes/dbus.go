// Package fakes provides in-memory test doubles for the D-Bus surface used by
// internal/ovpn. Tests never touch a real bus.
package fakes

import (
	"fmt"

	"github.com/godbus/dbus/v5"

	"github.com/esivres/openvpn3ui/internal/ovpn"
)

// CallHandler answers a single method call.
type CallHandler func(args ...interface{}) (body []interface{}, err error)

// FakeObject implements ovpn.BusObject in memory.
type FakeObject struct {
	ObjPath dbus.ObjectPath
	Props   map[string]dbus.Variant
	Calls   map[string]CallHandler
}

func (f *FakeObject) Path() dbus.ObjectPath { return f.ObjPath }

func (f *FakeObject) GetProperty(p string) (dbus.Variant, error) {
	v, ok := f.Props[p]
	if !ok {
		return dbus.Variant{}, fmt.Errorf("fake: no property %q on %s", p, f.ObjPath)
	}
	return v, nil
}

func (f *FakeObject) SetProperty(p string, v interface{}) error {
	if f.Props == nil {
		f.Props = map[string]dbus.Variant{}
	}
	f.Props[p] = dbus.MakeVariant(v)
	return nil
}

func (f *FakeObject) Call(method string, _ dbus.Flags, args ...interface{}) *dbus.Call {
	call := &dbus.Call{Method: method, Args: args}
	if h, ok := f.Calls[method]; ok {
		body, err := h(args...)
		call.Body = body
		call.Err = err
		return call
	}
	// Standard org.freedesktop.DBus interfaces: provide minimal defaults so
	// tests don't have to wire them up on every object.
	if method == "org.freedesktop.DBus.Introspectable.Introspect" {
		call.Body = []interface{}{`<node/>`}
		return call
	}
	call.Err = fmt.Errorf("fake: no handler for %s on %s", method, f.ObjPath)
	return call
}

// FakeConn implements ovpn.Conn over a path → FakeObject map.
type FakeConn struct {
	Objects map[string]*FakeObject
}

func NewFakeConn() *FakeConn {
	return &FakeConn{Objects: map[string]*FakeObject{}}
}

func (f *FakeConn) Add(obj *FakeObject) {
	f.Objects[string(obj.ObjPath)] = obj
}

func (f *FakeConn) Object(_ string, path dbus.ObjectPath) ovpn.BusObject {
	if obj, ok := f.Objects[string(path)]; ok {
		return obj
	}
	// The bus driver itself: answer StartServiceByName as "already running"
	// so tests don't need to register it explicitly.
	if path == "/org/freedesktop/DBus" {
		return &FakeObject{
			ObjPath: path,
			Calls: map[string]CallHandler{
				"org.freedesktop.DBus.StartServiceByName": func(_ ...interface{}) ([]interface{}, error) {
					return []interface{}{uint32(2)}, nil // REPLY_ALREADY_RUNNING-ish
				},
			},
		}
	}
	// Return a stub that errors on every interaction — closer to real D-Bus.
	return &FakeObject{ObjPath: path}
}

// BareObject mirrors Object — fakes have no retry decorator anyway,
// so the two return the same thing. Required by the Conn interface.
func (f *FakeConn) BareObject(dest string, path dbus.ObjectPath) ovpn.BusObject {
	return f.Object(dest, path)
}

func (f *FakeConn) AddMatchSignal(_ ...dbus.MatchOption) error    { return nil }
func (f *FakeConn) RemoveMatchSignal(_ ...dbus.MatchOption) error { return nil }
func (f *FakeConn) Signal(_ chan<- *dbus.Signal)                  {}
func (f *FakeConn) RemoveSignal(_ chan<- *dbus.Signal)            {}
func (f *FakeConn) Close() error                                  { return nil }
