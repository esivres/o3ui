package ovpn

import (
	"errors"
	"testing"
	"time"

	"github.com/godbus/dbus/v5"
	"github.com/stretchr/testify/require"
)

// shortenRetry tightens the backoff so tests don't burn real time. The
// originals are restored via t.Cleanup so test files don't leak settings
// into siblings.
func shortenRetry(t *testing.T) {
	t.Helper()
	oa, obd, omd := retryAttempts, retryBaseDelay, retryMaxDelay
	retryAttempts = 4
	retryBaseDelay = time.Millisecond
	retryMaxDelay = 4 * time.Millisecond
	t.Cleanup(func() {
		retryAttempts, retryBaseDelay, retryMaxDelay = oa, obd, omd
	})
}

func TestIsTransientDBusError(t *testing.T) {
	require.False(t, isTransientDBusError(nil))
	require.False(t, isTransientDBusError(errors.New("plain error")))

	transient := dbus.Error{Name: "org.freedesktop.DBus.Error.UnknownObject"}
	require.True(t, isTransientDBusError(transient))

	permanent := dbus.Error{Name: "org.freedesktop.DBus.Error.AccessDenied"}
	require.False(t, isTransientDBusError(permanent),
		"AccessDenied is a real error — must not be retried")
}

// flakyObject is a BusObject that errors transient on the first N calls,
// then succeeds. Used to verify the retry loop converges.
type flakyObject struct {
	transient int           // remaining transient failures to inject
	calls     int           // total calls observed (success + failure)
	body      []interface{} // what to return on success
}

func (f *flakyObject) Path() dbus.ObjectPath { return "/x" }
func (f *flakyObject) Call(method string, _ dbus.Flags, args ...interface{}) *dbus.Call {
	f.calls++
	if f.transient > 0 {
		f.transient--
		return &dbus.Call{
			Method: method, Args: args,
			Err: dbus.Error{Name: "org.freedesktop.DBus.Error.UnknownObject"},
		}
	}
	return &dbus.Call{Method: method, Args: args, Body: f.body}
}
func (f *flakyObject) GetProperty(string) (dbus.Variant, error) {
	f.calls++
	if f.transient > 0 {
		f.transient--
		return dbus.Variant{}, dbus.Error{Name: "org.freedesktop.DBus.Error.UnknownObject"}
	}
	return dbus.MakeVariant("ok"), nil
}
func (f *flakyObject) SetProperty(string, interface{}) error {
	f.calls++
	if f.transient > 0 {
		f.transient--
		return dbus.Error{Name: "org.freedesktop.DBus.Error.UnknownObject"}
	}
	return nil
}

func TestRetryingObject_RetriesUntilSuccess(t *testing.T) {
	shortenRetry(t)
	inner := &flakyObject{transient: 2, body: []interface{}{"hello"}}
	wrap := retryingObject{inner: inner}

	call := wrap.Call("X", 0)
	require.NoError(t, call.Err)
	require.Equal(t, 3, inner.calls, "must retry 2 transient failures + 1 success")
}

func TestRetryingObject_GivesUpAfterMaxAttempts(t *testing.T) {
	shortenRetry(t)
	inner := &flakyObject{transient: 99} // never recovers
	wrap := retryingObject{inner: inner}

	call := wrap.Call("X", 0)
	require.Error(t, call.Err)
	require.Equal(t, retryAttempts, inner.calls, "must stop after retryAttempts")
}

func TestRetryingObject_DoesNotRetryPermanentError(t *testing.T) {
	shortenRetry(t)
	inner := &permFailObject{}
	wrap := retryingObject{inner: inner}

	call := wrap.Call("X", 0)
	require.Error(t, call.Err)
	require.Equal(t, 1, inner.calls, "non-transient error must not trigger retry")
}

type permFailObject struct{ calls int }

func (p *permFailObject) Path() dbus.ObjectPath { return "/x" }
func (p *permFailObject) Call(string, dbus.Flags, ...interface{}) *dbus.Call {
	p.calls++
	return &dbus.Call{Err: dbus.Error{Name: "org.freedesktop.DBus.Error.AccessDenied"}}
}
func (p *permFailObject) GetProperty(string) (dbus.Variant, error) {
	p.calls++
	return dbus.Variant{}, dbus.Error{Name: "org.freedesktop.DBus.Error.AccessDenied"}
}
func (p *permFailObject) SetProperty(string, interface{}) error {
	p.calls++
	return dbus.Error{Name: "org.freedesktop.DBus.Error.AccessDenied"}
}

func TestRetryingObject_GetPropertyAndSetPropertyAlsoRetry(t *testing.T) {
	shortenRetry(t)

	inner := &flakyObject{transient: 2}
	wrap := retryingObject{inner: inner}
	v, err := wrap.GetProperty("foo")
	require.NoError(t, err)
	require.Equal(t, "ok", v.Value())
	require.Equal(t, 3, inner.calls)

	inner2 := &flakyObject{transient: 2}
	wrap2 := retryingObject{inner: inner2}
	require.NoError(t, wrap2.SetProperty("foo", "bar"))
	require.Equal(t, 3, inner2.calls)
}
