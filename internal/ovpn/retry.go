package ovpn

import (
	"errors"
	"time"

	"github.com/godbus/dbus/v5"
)

// Transient D-Bus error names — these come up during cold-start or
// activation races, not from coding mistakes. Retry on these only;
// genuine errors (UnknownMethod, AccessDenied, …) propagate immediately
// so we don't paper over real bugs.
var transientDBusErrors = map[string]struct{}{
	"org.freedesktop.DBus.Error.UnknownObject":  {}, // "Object does not exist at path"
	"org.freedesktop.DBus.Error.ServiceUnknown": {}, // bus name not yet owned
	"org.freedesktop.DBus.Error.NoReply":        {}, // service didn't answer in time
	"org.freedesktop.DBus.Error.NoReplyTimeout": {},
	"org.freedesktop.DBus.Error.NameHasNoOwner": {},
	"org.freedesktop.DBus.Error.Disconnected":   {},
}

// Retry tunables. Package-level vars (not consts) so tests can shrink
// the delays without exporting a Set* knob.
var (
	retryAttempts  = 5
	retryBaseDelay = 50 * time.Millisecond
	retryMaxDelay  = 800 * time.Millisecond
)

// isTransientDBusError reports whether err is one of the activation-race
// failures we should retry. Returns false for nil and for "real" errors.
func isTransientDBusError(err error) bool {
	if err == nil {
		return false
	}
	var de dbus.Error
	if !errors.As(err, &de) {
		return false
	}
	_, ok := transientDBusErrors[de.Name]
	return ok
}

// retryingObject decorates a BusObject with automatic retry on transient
// errors. Method calls + property access all go through the same
// backoff loop, so callers see "object not found" disappear without
// needing to know about D-Bus activation timing.
type retryingObject struct {
	inner BusObject
}

func (r retryingObject) Path() dbus.ObjectPath { return r.inner.Path() }

func (r retryingObject) Call(method string, flags dbus.Flags, args ...interface{}) *dbus.Call {
	var call *dbus.Call
	withRetry(func() error {
		call = r.inner.Call(method, flags, args...)
		return call.Err
	})
	return call
}

func (r retryingObject) GetProperty(p string) (dbus.Variant, error) {
	var v dbus.Variant
	err := withRetry(func() error {
		var e error
		v, e = r.inner.GetProperty(p)
		return e
	})
	return v, err
}

func (r retryingObject) SetProperty(p string, val interface{}) error {
	return withRetry(func() error { return r.inner.SetProperty(p, val) })
}

// withRetry runs op up to retryAttempts times, sleeping with exponential
// backoff (capped at retryMaxDelay) after each transient failure. Stops
// immediately on success or non-transient error. Returns the final error
// from op (nil on success).
func withRetry(op func() error) error {
	delay := retryBaseDelay
	var err error
	for attempt := 0; attempt < retryAttempts; attempt++ {
		err = op()
		if err == nil || !isTransientDBusError(err) {
			return err
		}
		time.Sleep(delay)
		delay *= 2
		if delay > retryMaxDelay {
			delay = retryMaxDelay
		}
	}
	return err
}
