package ovpn_test

import (
	"testing"

	"github.com/godbus/dbus/v5"
	"github.com/stretchr/testify/require"

	"github.com/esivres/openvpn3ui/internal/ovpn"
	"github.com/esivres/openvpn3ui/test/fakes"
)

func TestConfigManager_List(t *testing.T) {
	conn := fakes.NewFakeConn()

	conn.Add(&fakes.FakeObject{
		ObjPath: ovpn.PathConfiguration,
		Calls: map[string]fakes.CallHandler{
			ovpn.IfaceConfigurationMgr + ".FetchAvailableConfigs": func(_ ...interface{}) ([]interface{}, error) {
				return []interface{}{[]dbus.ObjectPath{"/net/openvpn/v3/configuration/abc", "/net/openvpn/v3/configuration/def"}}, nil
			},
		},
	})
	conn.Add(&fakes.FakeObject{
		ObjPath: "/net/openvpn/v3/configuration/abc",
		Props: map[string]dbus.Variant{
			ovpn.IfaceConfiguration + ".name":  dbus.MakeVariant("home"),
			ovpn.IfaceConfiguration + ".valid": dbus.MakeVariant(true),
		},
	})
	conn.Add(&fakes.FakeObject{
		ObjPath: "/net/openvpn/v3/configuration/def",
		Props: map[string]dbus.Variant{
			ovpn.IfaceConfiguration + ".name":  dbus.MakeVariant("work"),
			ovpn.IfaceConfiguration + ".valid": dbus.MakeVariant(false),
		},
	})

	cfgs, err := ovpn.NewConfigManager(conn).List()
	require.NoError(t, err)
	require.Len(t, cfgs, 2)
	require.Equal(t, "home", cfgs[0].Name)
	require.True(t, cfgs[0].Valid)
	require.Equal(t, "work", cfgs[1].Name)
	require.False(t, cfgs[1].Valid)
}

func TestConfigManager_Import(t *testing.T) {
	conn := fakes.NewFakeConn()

	var capturedName, capturedBody string
	conn.Add(&fakes.FakeObject{
		ObjPath: ovpn.PathConfiguration,
		Calls: map[string]fakes.CallHandler{
			ovpn.IfaceConfigurationMgr + ".Import": func(args ...interface{}) ([]interface{}, error) {
				capturedName = args[0].(string)
				capturedBody = args[1].(string)
				return []interface{}{dbus.ObjectPath("/net/openvpn/v3/configuration/new")}, nil
			},
		},
	})

	path, err := ovpn.NewConfigManager(conn).Import("alpha", "client\nremote vpn.example\n", true)
	require.NoError(t, err)
	require.Equal(t, "/net/openvpn/v3/configuration/new", path)
	require.Equal(t, "alpha", capturedName)
	require.Contains(t, capturedBody, "remote vpn.example")
}
