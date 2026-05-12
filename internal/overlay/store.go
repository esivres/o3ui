// Package overlay stores per-config metadata that lives outside openvpn3
// itself: a friendly alias and a key into the OS keyring for the config's
// TOTP secret. The openvpn3 D-Bus service remains the source of truth for
// the profile body; this package only carries our annotations.
package overlay

import (
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	_ "modernc.org/sqlite" // pure-Go SQLite driver, no cgo
)

// Overlay is the metadata we keep for a single openvpn3 configuration. The
// natural key is its D-Bus path; everything else is optional.
type Overlay struct {
	ConfigPath       string
	Alias            string
	OTPSecretID      string // key into secrets.Store; empty == no OTP attached
	Favorite         bool
	AutoConnect      bool
	LastConnectedAt  time.Time // zero when never connected
	CountryCode      string    // short label like "DE" / "SE", optional
	Username         string    // remembered VPN username; plaintext is fine, identity is not a secret
	PasswordSecretID string    // key into secrets.Store; empty == no password stored
}

// ErrNotFound is returned when no overlay exists for a given config path.
var ErrNotFound = errors.New("overlay not found")

// Store is the persistence interface — small enough to mock without a fake
// SQLite. Production code uses *SQLite.
type Store interface {
	Get(configPath string) (Overlay, error)
	Upsert(o Overlay) error
	Delete(configPath string) error
	List() ([]Overlay, error)
	Close() error
}

// SQLite is the file-backed implementation.
type SQLite struct {
	db *sql.DB
}

// DefaultPath returns the canonical location for the overlay database under
// XDG_DATA_HOME (or ~/.local/share fallback). The directory is created if
// missing.
func DefaultPath() (string, error) {
	dir := os.Getenv("XDG_DATA_HOME")
	if dir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		dir = filepath.Join(home, ".local", "share")
	}
	dir = filepath.Join(dir, "openvpn3ui")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	return filepath.Join(dir, "overlay.db"), nil
}

// Open initialises a database at the given path, creating tables on first
// use. Pass ":memory:" for tests that don't want a file on disk.
func Open(path string) (*SQLite, error) {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, err
	}
	// Modest pragma — we're not high-throughput.
	if _, err := db.Exec(`PRAGMA journal_mode = WAL; PRAGMA foreign_keys = ON;`); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("pragma: %w", err)
	}
	if err := migrate(db); err != nil {
		_ = db.Close()
		return nil, err
	}
	return &SQLite{db: db}, nil
}

func migrate(db *sql.DB) error {
	const schema = `
		CREATE TABLE IF NOT EXISTS overlays (
			config_path  TEXT PRIMARY KEY,
			alias        TEXT NOT NULL DEFAULT '',
			otp_secret_id TEXT NOT NULL DEFAULT ''
		);
	`
	if _, err := db.Exec(schema); err != nil {
		return err
	}
	// Additive column migrations. Each is idempotent: we look at
	// pragma_table_info first, so re-runs on an already-current DB are
	// safe and silent. Numbered migrations are overkill until we need to
	// drop or rename columns.
	cols, err := existingColumns(db, "overlays")
	if err != nil {
		return err
	}
	additions := []struct {
		name string
		ddl  string
	}{
		{"favorite", `ALTER TABLE overlays ADD COLUMN favorite INTEGER NOT NULL DEFAULT 0`},
		{"auto_connect", `ALTER TABLE overlays ADD COLUMN auto_connect INTEGER NOT NULL DEFAULT 0`},
		{"last_connected_at", `ALTER TABLE overlays ADD COLUMN last_connected_at INTEGER NOT NULL DEFAULT 0`},
		{"country_code", `ALTER TABLE overlays ADD COLUMN country_code TEXT NOT NULL DEFAULT ''`},
		{"username", `ALTER TABLE overlays ADD COLUMN username TEXT NOT NULL DEFAULT ''`},
		{"password_secret_id", `ALTER TABLE overlays ADD COLUMN password_secret_id TEXT NOT NULL DEFAULT ''`},
	}
	for _, m := range additions {
		if cols[m.name] {
			continue
		}
		if _, err := db.Exec(m.ddl); err != nil {
			return fmt.Errorf("migrate add %s: %w", m.name, err)
		}
	}
	return nil
}

func existingColumns(db *sql.DB, table string) (map[string]bool, error) {
	rows, err := db.Query(`SELECT name FROM pragma_table_info(?)`, table)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]bool{}
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			return nil, err
		}
		out[n] = true
	}
	return out, rows.Err()
}

func (s *SQLite) Close() error { return s.db.Close() }

func (s *SQLite) Get(configPath string) (Overlay, error) {
	o, err := scanOverlay(s.db.QueryRow(selectColumns+` WHERE config_path = ?`, configPath))
	if errors.Is(err, sql.ErrNoRows) {
		return Overlay{}, ErrNotFound
	}
	return o, err
}

// Upsert takes Overlay by value because the OverlayStore interface in
// internal/app does too — switching one side to a pointer forks every
// caller and fake. The struct is ~128B and Upsert runs at most once
// per user action; the copy is irrelevant.
//
//nolint:gocritic // hugeParam: kept value-typed for API symmetry
func (s *SQLite) Upsert(o Overlay) error {
	if o.ConfigPath == "" {
		return errors.New("overlay: empty config_path")
	}
	_, err := s.db.Exec(
		`INSERT INTO overlays
		   (config_path, alias, otp_secret_id, favorite, auto_connect, last_connected_at,
		    country_code, username, password_secret_id)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
		 ON CONFLICT(config_path) DO UPDATE SET
		   alias = excluded.alias,
		   otp_secret_id = excluded.otp_secret_id,
		   favorite = excluded.favorite,
		   auto_connect = excluded.auto_connect,
		   last_connected_at = excluded.last_connected_at,
		   country_code = excluded.country_code,
		   username = excluded.username,
		   password_secret_id = excluded.password_secret_id`,
		o.ConfigPath, o.Alias, o.OTPSecretID,
		boolToInt(o.Favorite), boolToInt(o.AutoConnect),
		unixOrZero(o.LastConnectedAt), o.CountryCode,
		o.Username, o.PasswordSecretID,
	)
	return err
}

// scanOverlay reads one row off either *sql.Row or *sql.Rows. Centralised
// so the column order stays in sync between Get/List/etc.
type scannable interface {
	Scan(dest ...interface{}) error
}

const selectColumns = `SELECT config_path, alias, otp_secret_id,
		favorite, auto_connect, last_connected_at, country_code,
		username, password_secret_id
	FROM overlays`

func scanOverlay(s scannable) (Overlay, error) {
	var (
		o           Overlay
		fav, auto   int
		connectedAt int64
	)
	err := s.Scan(&o.ConfigPath, &o.Alias, &o.OTPSecretID,
		&fav, &auto, &connectedAt, &o.CountryCode,
		&o.Username, &o.PasswordSecretID)
	if err != nil {
		return Overlay{}, err
	}
	o.Favorite = fav != 0
	o.AutoConnect = auto != 0
	if connectedAt > 0 {
		o.LastConnectedAt = time.Unix(connectedAt, 0)
	}
	return o, nil
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}
func unixOrZero(t time.Time) int64 {
	if t.IsZero() {
		return 0
	}
	return t.Unix()
}

func (s *SQLite) Delete(configPath string) error {
	res, err := s.db.Exec(`DELETE FROM overlays WHERE config_path = ?`, configPath)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *SQLite) List() ([]Overlay, error) {
	rows, err := s.db.Query(selectColumns + ` ORDER BY config_path`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []Overlay
	for rows.Next() {
		o, err := scanOverlay(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, o)
	}
	return out, rows.Err()
}
