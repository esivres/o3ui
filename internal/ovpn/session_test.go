package ovpn_test

import (
	"testing"

	"github.com/godbus/dbus/v5"
	"github.com/stretchr/testify/require"

	"github.com/esivres/openvpn3ui/internal/ovpn"
	"github.com/esivres/openvpn3ui/test/fakes"
)

func TestSessionManager_NewTunnel(t *testing.T) {
	conn := fakes.NewFakeConn()

	var capturedConfig dbus.ObjectPath
	conn.Add(&fakes.FakeObject{
		ObjPath: ovpn.PathSessions,
		Calls: map[string]fakes.CallHandler{
			ovpn.IfaceSessionsMgr + ".NewTunnel": func(args ...interface{}) ([]interface{}, error) {
				capturedConfig = args[0].(dbus.ObjectPath)
				return []interface{}{dbus.ObjectPath("/net/openvpn/v3/sessions/abc")}, nil
			},
		},
	})

	mgr := ovpn.NewSessionManager(conn)
	path, err := mgr.NewTunnel("/net/openvpn/v3/configuration/cfg1")
	require.NoError(t, err)
	require.Equal(t, "/net/openvpn/v3/sessions/abc", path)
	require.EqualValues(t, "/net/openvpn/v3/configuration/cfg1", capturedConfig)
}

func TestSessionManager_GetAndList(t *testing.T) {
	conn := fakes.NewFakeConn()
	sessionPath := dbus.ObjectPath("/net/openvpn/v3/sessions/s1")

	conn.Add(&fakes.FakeObject{
		ObjPath: ovpn.PathSessions,
		Calls: map[string]fakes.CallHandler{
			ovpn.IfaceSessionsMgr + ".FetchAvailableSessions": func(_ ...interface{}) ([]interface{}, error) {
				return []interface{}{[]dbus.ObjectPath{sessionPath}}, nil
			},
		},
	})
	conn.Add(&fakes.FakeObject{
		ObjPath: sessionPath,
		Props: map[string]dbus.Variant{
			ovpn.IfaceSession + ".config_name":     dbus.MakeVariant("home"),
			ovpn.IfaceSession + ".config_path":     dbus.MakeVariant(dbus.ObjectPath("/net/openvpn/v3/configuration/cfg1")),
			ovpn.IfaceSession + ".device_name":     dbus.MakeVariant("tun0"),
			ovpn.IfaceSession + ".session_name":    dbus.MakeVariant("vpn.example"),
			ovpn.IfaceSession + ".session_created": dbus.MakeVariant(uint64(1700000000)),
			ovpn.IfaceSession + ".status": dbus.MakeVariant(struct {
				Major, Minor uint32
				Message      string
			}{Major: 2, Minor: 7, Message: ""}),
		},
	})

	mgr := ovpn.NewSessionManager(conn)
	sessions, err := mgr.List()
	require.NoError(t, err)
	require.Len(t, sessions, 1)

	s := sessions[0]
	require.Equal(t, "home", s.ConfigName)
	require.Equal(t, "tun0", s.DeviceName)
	require.Equal(t, "vpn.example", s.SessionName)
	require.True(t, s.Status.IsConnected(), "(2,7) must be considered connected")
	require.Equal(t, "/net/openvpn/v3/configuration/cfg1", s.ConfigPath)
}

func TestSessionController_Methods(t *testing.T) {
	conn := fakes.NewFakeConn()
	sessionPath := dbus.ObjectPath("/net/openvpn/v3/sessions/s1")

	called := map[string]int{}
	calls := map[string]fakes.CallHandler{}
	for _, m := range []string{"Ready", "Connect", "Disconnect", "Restart", "Resume", "Pause"} {
		method := m
		calls[ovpn.IfaceSession+"."+method] = func(_ ...interface{}) ([]interface{}, error) {
			called[method]++
			return nil, nil
		}
	}
	conn.Add(&fakes.FakeObject{ObjPath: sessionPath, Calls: calls})

	sc := ovpn.NewSessionManager(conn).Control(string(sessionPath))
	require.NoError(t, sc.Ready())
	require.NoError(t, sc.Connect())
	require.NoError(t, sc.Pause("user"))
	require.NoError(t, sc.Resume())
	require.NoError(t, sc.Restart())
	require.NoError(t, sc.Disconnect())

	require.Equal(t, 1, called["Ready"])
	require.Equal(t, 1, called["Connect"])
	require.Equal(t, 1, called["Pause"])
	require.Equal(t, 1, called["Resume"])
	require.Equal(t, 1, called["Restart"])
	require.Equal(t, 1, called["Disconnect"])
}
