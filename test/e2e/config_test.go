//go:build e2e

package e2e

import (
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/esivres/openvpn3ui/internal/ovpn"
)

// Test_List_Empty verifies that listing configs on a (potentially empty)
// system never errors. This was the failure mode the user observed at startup.
func Test_List_Empty(t *testing.T) {
	conn := requireOpenVPN3(t)
	mgr := ovpn.NewConfigManager(conn)

	cfgs, err := mgr.List()
	require.NoError(t, err, "List() must succeed even when there are no configs")
	t.Logf("found %d existing configs", len(cfgs))
	for _, c := range cfgs {
		require.NotEmpty(t, c.Path, "every config must have a D-Bus path")
	}
}

// Test_Import_List_Remove is the full round-trip: bring a config in, see it
// in the listing, then clean up. Uses a unique name so concurrent runs don't
// collide.
func Test_Import_List_Remove(t *testing.T) {
	conn := requireOpenVPN3(t)
	mgr := ovpn.NewConfigManager(conn)

	name := fmt.Sprintf("openvpn3ui-e2e-%d", time.Now().UnixNano())

	path, err := mgr.Import(name, sampleProfile, false /*persistent*/)
	require.NoError(t, err)
	require.NotEmpty(t, path)

	// Always clean up, even if assertions below fail.
	t.Cleanup(func() {
		if err := mgr.Remove(path); err != nil {
			t.Logf("cleanup Remove(%s) failed: %v", path, err)
		}
	})

	cfgs, err := mgr.List()
	require.NoError(t, err)

	got, ok := findConfigByPath(cfgs, path)
	require.True(t, ok, "imported config %s not present in List()", path)
	require.Equal(t, name, got.Name)
}
