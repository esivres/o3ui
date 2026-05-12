package overlay_test

import (
	"database/sql"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	_ "modernc.org/sqlite"

	"github.com/esivres/openvpn3ui/internal/overlay"
)

func newStore(t *testing.T) *overlay.SQLite {
	t.Helper()
	// File-backed (not :memory:) so we also exercise migrations on disk.
	path := filepath.Join(t.TempDir(), "overlay.db")
	s, err := overlay.Open(path)
	require.NoError(t, err)
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func TestSQLite_GetMissing(t *testing.T) {
	s := newStore(t)
	_, err := s.Get("/no/such")
	require.True(t, errors.Is(err, overlay.ErrNotFound))
}

func TestSQLite_UpsertAndGet(t *testing.T) {
	s := newStore(t)

	o := overlay.Overlay{
		ConfigPath:  "/net/openvpn/v3/configuration/abc",
		Alias:       "Home",
		OTPSecretID: "config:/net/openvpn/v3/configuration/abc:totp",
	}
	require.NoError(t, s.Upsert(o))

	got, err := s.Get(o.ConfigPath)
	require.NoError(t, err)
	require.Equal(t, o, got)
}

func TestSQLite_UpsertOverwrites(t *testing.T) {
	s := newStore(t)

	require.NoError(t, s.Upsert(overlay.Overlay{ConfigPath: "/p", Alias: "old"}))
	require.NoError(t, s.Upsert(overlay.Overlay{ConfigPath: "/p", Alias: "new", OTPSecretID: "id"}))

	got, _ := s.Get("/p")
	require.Equal(t, "new", got.Alias)
	require.Equal(t, "id", got.OTPSecretID)

	all, err := s.List()
	require.NoError(t, err)
	require.Len(t, all, 1, "upsert must not duplicate rows")
}

func TestSQLite_Delete(t *testing.T) {
	s := newStore(t)

	require.NoError(t, s.Upsert(overlay.Overlay{ConfigPath: "/p"}))
	require.NoError(t, s.Delete("/p"))

	_, err := s.Get("/p")
	require.True(t, errors.Is(err, overlay.ErrNotFound))

	// Idempotent: deleting again is ErrNotFound, not a hard error.
	require.True(t, errors.Is(s.Delete("/p"), overlay.ErrNotFound))
}

func TestSQLite_ListSorted(t *testing.T) {
	s := newStore(t)

	require.NoError(t, s.Upsert(overlay.Overlay{ConfigPath: "/c"}))
	require.NoError(t, s.Upsert(overlay.Overlay{ConfigPath: "/a"}))
	require.NoError(t, s.Upsert(overlay.Overlay{ConfigPath: "/b"}))

	all, err := s.List()
	require.NoError(t, err)
	require.Len(t, all, 3)
	require.Equal(t, "/a", all[0].ConfigPath)
	require.Equal(t, "/b", all[1].ConfigPath)
	require.Equal(t, "/c", all[2].ConfigPath)
}

func TestSQLite_RejectsEmptyConfigPath(t *testing.T) {
	s := newStore(t)
	require.Error(t, s.Upsert(overlay.Overlay{ConfigPath: ""}))
}

func TestSQLite_PersistsAcrossOpens(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "overlay.db")

	s1, err := overlay.Open(path)
	require.NoError(t, err)
	require.NoError(t, s1.Upsert(overlay.Overlay{ConfigPath: "/p", Alias: "kept"}))
	require.NoError(t, s1.Close())

	s2, err := overlay.Open(path)
	require.NoError(t, err)
	defer s2.Close()

	got, err := s2.Get("/p")
	require.NoError(t, err)
	require.Equal(t, "kept", got.Alias)
}

func TestSQLite_NewFieldsRoundTrip(t *testing.T) {
	s := newStore(t)

	when := time.Date(2026, 5, 8, 12, 34, 56, 0, time.UTC)
	require.NoError(t, s.Upsert(overlay.Overlay{
		ConfigPath:      "/p",
		Alias:           "Frankfurt",
		Favorite:        true,
		AutoConnect:     true,
		LastConnectedAt: when,
		CountryCode:     "DE",
	}))

	got, err := s.Get("/p")
	require.NoError(t, err)
	require.True(t, got.Favorite)
	require.True(t, got.AutoConnect)
	require.Equal(t, "DE", got.CountryCode)
	require.WithinDuration(t, when, got.LastConnectedAt, time.Second)
}

func TestSQLite_DefaultsForNewRow(t *testing.T) {
	s := newStore(t)
	require.NoError(t, s.Upsert(overlay.Overlay{ConfigPath: "/p"}))

	got, _ := s.Get("/p")
	require.False(t, got.Favorite)
	require.False(t, got.AutoConnect)
	require.True(t, got.LastConnectedAt.IsZero(), "never-connected must surface as zero time")
	require.Equal(t, "", got.CountryCode)
}

// TestSQLite_MigratesFromV1Schema simulates an existing user upgrading
// from the original three-column overlays table. Open() must add the new
// columns without losing rows.
func TestSQLite_MigratesFromV1Schema(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "overlay.db")

	// Pre-create the v1 schema and seed a row, mimicking a pre-migration db.
	raw, err := sql.Open("sqlite", path)
	require.NoError(t, err)
	_, err = raw.Exec(`
		CREATE TABLE overlays (
			config_path  TEXT PRIMARY KEY,
			alias        TEXT NOT NULL DEFAULT '',
			otp_secret_id TEXT NOT NULL DEFAULT ''
		);
		INSERT INTO overlays(config_path, alias, otp_secret_id) VALUES ('/p', 'legacy', 'sec1');
	`)
	require.NoError(t, err)
	require.NoError(t, raw.Close())

	// Now Open via the package — migration should bring the new columns in.
	s, err := overlay.Open(path)
	require.NoError(t, err)
	t.Cleanup(func() { _ = s.Close() })

	got, err := s.Get("/p")
	require.NoError(t, err)
	require.Equal(t, "legacy", got.Alias)
	require.Equal(t, "sec1", got.OTPSecretID)
	// Defaults applied to the legacy row.
	require.False(t, got.Favorite)
	require.True(t, got.LastConnectedAt.IsZero())
	require.Equal(t, "", got.CountryCode)

	// And we can write the new fields without touching the schema again.
	got.Favorite = true
	got.CountryCode = "DE"
	require.NoError(t, s.Upsert(got))
	again, _ := s.Get("/p")
	require.True(t, again.Favorite)
	require.Equal(t, "DE", again.CountryCode)
}

// Idempotency: Open on an already-current schema must not error.
func TestSQLite_OpenIsIdempotent(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "overlay.db")
	for i := 0; i < 3; i++ {
		s, err := overlay.Open(path)
		require.NoError(t, err)
		require.NoError(t, s.Close())
	}
}
