package secrets_test

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/esivres/openvpn3ui/internal/secrets"
)

// runStoreContract exercises the Store interface against any implementation.
// Used both for the in-memory fake and (when enabled) the live keyring.
func runStoreContract(t *testing.T, s secrets.Store) {
	t.Helper()

	const id = "test/openvpn3ui/contract"
	t.Cleanup(func() { _ = s.Delete(id) })

	// Get on missing key returns ErrNotFound.
	_, err := s.Get(id)
	require.True(t, errors.Is(err, secrets.ErrNotFound), "missing key must yield ErrNotFound, got %v", err)

	// Set + Get round-trip.
	require.NoError(t, s.Set(id, "JBSWY3DPEHPK3PXP"))
	v, err := s.Get(id)
	require.NoError(t, err)
	require.Equal(t, "JBSWY3DPEHPK3PXP", v)

	// Set overwrites.
	require.NoError(t, s.Set(id, "rotated-secret"))
	v, _ = s.Get(id)
	require.Equal(t, "rotated-secret", v)

	// Delete + Get returns ErrNotFound.
	require.NoError(t, s.Delete(id))
	_, err = s.Get(id)
	require.True(t, errors.Is(err, secrets.ErrNotFound))

	// Double-delete returns ErrNotFound (idempotent semantics).
	err = s.Delete(id)
	require.True(t, errors.Is(err, secrets.ErrNotFound))
}

func TestMemoryStore_Contract(t *testing.T) {
	runStoreContract(t, secrets.NewMemory())
}
