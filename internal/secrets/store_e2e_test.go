//go:build e2e

package secrets_test

import (
	"testing"

	"github.com/esivres/openvpn3ui/internal/secrets"
)

// TestSystemStore_Contract runs the same contract against the real Secret
// Service. Build-tagged so it only runs with `-tags=e2e` and on a host with
// an unlocked keyring (DBUS_SESSION_BUS_ADDRESS + org.freedesktop.secrets).
func TestSystemStore_Contract(t *testing.T) {
	runStoreContract(t, secrets.New())
}
