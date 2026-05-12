//go:build e2e

package e2e

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/esivres/openvpn3ui/internal/ovpn"
)

// Test_Sessions_List is read-only: it just lists whatever the user already
// has running. It does not create or tear down sessions, so it is safe to
// run repeatedly without disturbing live tunnels.
func Test_Sessions_List(t *testing.T) {
	conn := requireOpenVPN3(t)
	mgr := ovpn.NewSessionManager(conn)

	sessions, err := mgr.List()
	require.NoError(t, err, "FetchAvailableSessions must succeed")

	t.Logf("found %d active sessions", len(sessions))
	for _, s := range sessions {
		require.NotEmpty(t, s.Path, "session must have a path")
		t.Logf("  %s  config=%q device=%q status=(%d,%d)",
			s.Path, s.ConfigName, s.DeviceName, s.Status.Major, s.Status.Minor)
	}
}
