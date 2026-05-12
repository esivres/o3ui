package ovpn_test

import (
	"errors"
	"testing"

	"github.com/godbus/dbus/v5"
	"github.com/stretchr/testify/require"

	"github.com/esivres/openvpn3ui/internal/ovpn"
	"github.com/esivres/openvpn3ui/test/fakes"
)

func TestEnsureService_StartsByName(t *testing.T) {
	conn := fakes.NewFakeConn()

	var calledWithName string
	conn.Add(&fakes.FakeObject{
		ObjPath: "/org/freedesktop/DBus",
		Calls: map[string]fakes.CallHandler{
			"org.freedesktop.DBus.StartServiceByName": func(args ...interface{}) ([]interface{}, error) {
				calledWithName = args[0].(string)
				return []interface{}{uint32(1)}, nil // REPLY_SUCCESS
			},
		},
	})

	require.NoError(t, ovpn.EnsureService(conn, ovpn.BusConfiguration, ovpn.PathConfiguration))
	require.Equal(t, ovpn.BusConfiguration, calledWithName)
}

func TestEnsureService_PropagatesError(t *testing.T) {
	conn := fakes.NewFakeConn()
	conn.Add(&fakes.FakeObject{
		ObjPath: "/org/freedesktop/DBus",
		Calls: map[string]fakes.CallHandler{
			"org.freedesktop.DBus.StartServiceByName": func(_ ...interface{}) ([]interface{}, error) {
				return nil, errors.New("forbidden")
			},
		},
	})

	err := ovpn.EnsureService(conn, ovpn.BusSessions, ovpn.PathSessions)
	require.Error(t, err)
	require.Contains(t, err.Error(), "forbidden")
}

// silence unused import linters when this file shrinks.
var _ = dbus.ObjectPath("")
