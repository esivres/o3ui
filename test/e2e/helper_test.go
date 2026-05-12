//go:build e2e

package e2e

import (
	"testing"

	"github.com/godbus/dbus/v5"

	"github.com/esivres/openvpn3ui/internal/ovpn"
)

// requireOpenVPN3 connects to the system bus and verifies that the openvpn3
// configuration manager is reachable. Skips otherwise so the test is portable.
func requireOpenVPN3(t *testing.T) ovpn.Conn {
	t.Helper()

	conn, err := ovpn.ConnectSystemBus()
	if err != nil {
		t.Skipf("system bus not available: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })

	// Touch the configuration manager. Activation-on-demand: a successful
	// call to a no-op method (Introspect) tells us the service is installed.
	obj := conn.Object(ovpn.BusConfiguration, ovpn.PathConfiguration)
	var xml string
	if err := obj.Call("org.freedesktop.DBus.Introspectable.Introspect", 0).Store(&xml); err != nil {
		t.Skipf("openvpn3 configuration service not available: %v", err)
	}
	return conn
}

// sampleProfile is a minimal valid .ovpn body used for import round-trips.
// It is *not* connectable — the host/cert are dummies — but openvpn3 accepts
// it as a parseable configuration.
const sampleProfile = `client
dev tun
proto udp
remote vpn.example.invalid 1194
resolv-retry infinite
nobind
persist-key
persist-tun
remote-cert-tls server
cipher AES-256-GCM
verb 3
<ca>
-----BEGIN CERTIFICATE-----
MIIBkTCB+wIJAKHHIgIIp7C+MA0GCSqGSIb3DQEBCwUAMA0xCzAJBgNVBAYTAlVT
MB4XDTI0MDEwMTAwMDAwMFoXDTM0MDEwMTAwMDAwMFowDTELMAkGA1UEBhMCVVMw
gZ8wDQYJKoZIhvcNAQEBBQADgY0AMIGJAoGBALxxxxxxxxxxxxxxxxxxxxxxxxxx
xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx
xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx
xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx
AgMBAAEwDQYJKoZIhvcNAQELBQADAQA=
-----END CERTIFICATE-----
</ca>
`

// findConfigByPath is a tiny helper for assertions.
func findConfigByPath(cfgs []ovpn.Config, path string) (ovpn.Config, bool) {
	for _, c := range cfgs {
		if c.Path == path {
			return c, true
		}
	}
	return ovpn.Config{}, false
}

// silence "imported but not used" if the file ends up only providing helpers.
var _ = dbus.ObjectPath("")
