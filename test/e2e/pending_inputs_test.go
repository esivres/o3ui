//go:build e2e

package e2e

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/esivres/openvpn3ui/internal/ovpn"
)

// Test_PendingInputs_ReadOnly walks every existing session and reads its
// queued UserInputs. We don't care what the contents are — just that the
// D-Bus marshalling of UserInputQueueGetTypeGroup / Check / Fetch works
// against a real openvpn3 service for whatever shape it returns today.
func Test_PendingInputs_ReadOnly(t *testing.T) {
	conn := requireOpenVPN3(t)
	mgr := ovpn.NewSessionManager(conn)

	sessions, err := mgr.List()
	require.NoError(t, err)
	if len(sessions) == 0 {
		t.Skip("no live sessions to query")
	}

	for _, s := range sessions {
		ctl := mgr.Control(s.Path)
		prompts, err := ctl.PendingInputs()
		require.NoError(t, err, "PendingInputs on %s must not error", s.Path)
		t.Logf("%s: %d pending input(s)", s.Path, len(prompts))
		for _, p := range prompts {
			t.Logf("    type=%d group=%d id=%d name=%q hidden=%v",
				p.Type, p.Group, p.ID, p.Name, p.Hidden)
		}
	}
}
