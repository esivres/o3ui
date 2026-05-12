package ovpn_test

import (
	"testing"

	"github.com/godbus/dbus/v5"
	"github.com/stretchr/testify/require"

	"github.com/esivres/openvpn3ui/internal/ovpn"
	"github.com/esivres/openvpn3ui/test/fakes"
)

func TestListBackendServices_FiltersAndStates(t *testing.T) {
	conn := fakes.NewFakeConn()
	conn.Add(&fakes.FakeObject{
		ObjPath: "/org/freedesktop/DBus",
		Calls: map[string]fakes.CallHandler{
			"org.freedesktop.DBus.ListNames": func(_ ...interface{}) ([]interface{}, error) {
				return []interface{}{[]string{
					"org.freedesktop.DBus",
					"net.openvpn.v3.configuration",
					"net.openvpn.v3.sessions",
					"net.openvpn.v3.backends.be4839", // instance name, must be skipped
					"some.other.service",
				}}, nil
			},
			"org.freedesktop.DBus.ListActivatableNames": func(_ ...interface{}) ([]interface{}, error) {
				return []interface{}{[]string{
					"net.openvpn.v3.configuration", // dup of running, must not double
					"net.openvpn.v3.netcfg",
					"net.openvpn.v3.log",
				}}, nil
			},
			"org.freedesktop.DBus.GetConnectionUnixProcessID": func(args ...interface{}) ([]interface{}, error) {
				// Return a fake PID per name. Process won't exist on the
				// host, so procStarted should silently fail and Started
				// will stay zero — we accept that in this test.
				name := args[0].(string)
				switch name {
				case "net.openvpn.v3.configuration":
					return []interface{}{uint32(11111)}, nil
				case "net.openvpn.v3.sessions":
					return []interface{}{uint32(22222)}, nil
				}
				return nil, nil
			},
		},
	})

	got, err := ovpn.ListBackendServices(conn)
	require.NoError(t, err)

	names := map[string]ovpn.BackendService{}
	for _, s := range got {
		names[s.Name] = s
	}

	// Filter: only net.openvpn.v3.* parents, no instances, no others.
	require.Contains(t, names, "net.openvpn.v3.configuration")
	require.Contains(t, names, "net.openvpn.v3.sessions")
	require.Contains(t, names, "net.openvpn.v3.netcfg")
	require.Contains(t, names, "net.openvpn.v3.log")
	require.NotContains(t, names, "net.openvpn.v3.backends.be4839", "per-instance bus names must not be surfaced")
	require.NotContains(t, names, "some.other.service")
	require.NotContains(t, names, "org.freedesktop.DBus")

	// State derivation from which list the name appeared on.
	require.Equal(t, "running", names["net.openvpn.v3.configuration"].State)
	require.Equal(t, "running", names["net.openvpn.v3.sessions"].State)
	require.Equal(t, "activatable", names["net.openvpn.v3.netcfg"].State)
	require.Equal(t, "activatable", names["net.openvpn.v3.log"].State)

	// Dup handling: configuration appears in both lists; "running" wins.
	require.Equal(t, "running", names["net.openvpn.v3.configuration"].State)

	// PID lookups for running services succeed; activatable ones don't try.
	require.Equal(t, uint32(11111), names["net.openvpn.v3.configuration"].PID)
	require.Equal(t, uint32(22222), names["net.openvpn.v3.sessions"].PID)
	require.Equal(t, uint32(0), names["net.openvpn.v3.netcfg"].PID)
}

// silence linter when test file has no other dbus usage.
var _ = dbus.ObjectPath("")
