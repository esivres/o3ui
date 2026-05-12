//go:build e2e

package e2e

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/esivres/openvpn3ui/internal/ovpn"
)

// Test_ListBackendServices_Live verifies the helper against the real bus.
// Cheap and read-only: just lists names. We expect at least the
// configuration + sessions services to be visible (since requireOpenVPN3
// activated them).
func Test_ListBackendServices_Live(t *testing.T) {
	conn := requireOpenVPN3(t)

	svcs, err := ovpn.ListBackendServices(conn)
	require.NoError(t, err)
	require.NotEmpty(t, svcs)

	have := map[string]ovpn.BackendService{}
	for _, s := range svcs {
		require.True(t, strings.HasPrefix(s.Name, "net.openvpn.v3."),
			"helper must not surface non-openvpn services; got %q", s.Name)
		require.NotContains(t, strings.TrimPrefix(s.Name, "net.openvpn.v3."), ".",
			"helper must hide per-instance suffixes; got %q", s.Name)
		have[s.Name] = s
		t.Logf("  %-36s state=%s pid=%d uptime=%s", s.Name, s.State, s.PID, s.Uptime())
	}
	require.Contains(t, have, "net.openvpn.v3.configuration")
}
