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

// HistoryEntry is one row in the per-profile session history. EndedAt
// is zero while a session is still live, so the list pane can render
// it as "ongoing" rather than computing a misleading duration. Status
// is a short label: "closed" for clean user-initiated disconnect,
// "lost" when the daemon reported the session destroyed without us
// asking, "failed" for a connect that never reached ready. Bytes are
// best-effort — openvpn3 sometimes evicts the session before we get
// a final read.
type HistoryEntry struct {
	ID          int64
	ConfigPath  string
	SessionPath string // D-Bus session object path; empty for legacy rows
	StartedAt   time.Time
	EndedAt     time.Time
	Status      string
	BytesIn     int64
	BytesOut    int64
}

// HistoryCap is how many entries we keep per profile. Older ones drop
// off automatically when a new one lands.
const HistoryCap = 10

// Store is the persistence interface — small enough to mock without a fake
// SQLite. Production code uses *SQLite.
type Store interface {
	Get(configPath string) (Overlay, error)
	Upsert(o Overlay) error
	Delete(configPath string) error
	List() ([]Overlay, error)
	Close() error

	// RecordHistoryStart inserts an open-ended history entry (EndedAt
	// zero) tagged with both the profile config_path and the live
	// session_path. The session_path is the canonical "which row to
	// close on disconnect" key — making it part of the schema lets
	// any process (TUI, desklet CLI, future runners) finalise the
	// same row by its session path regardless of who opened it. Old
	// entries past HistoryCap for the profile are pruned in the same
	// call so the ring buffer stays bounded.
	RecordHistoryStart(configPath, sessionPath string, startedAt time.Time) (int64, error)
	// CloseHistoryBySession finalises the still-open row for a given
	// session_path. Returns whether a row was actually updated — the
	// caller can use that to decide whether to log a follow-up event.
	// Safe to call multiple times: only the row with ended_at = 0 is
	// touched, so a second call (e.g. SessionDestroyedEvent arriving
	// after our own Disconnect already closed the row) is a no-op.
	CloseHistoryBySession(sessionPath string, endedAt time.Time, status string, bytesIn, bytesOut int64) (bool, error)
	// History returns the most recent HistoryCap entries for a profile,
	// newest first. Empty slice (not an error) when there are none.
	History(configPath string) ([]HistoryEntry, error)
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
	const historySchema = `
		CREATE TABLE IF NOT EXISTS session_history (
			id           INTEGER PRIMARY KEY AUTOINCREMENT,
			config_path  TEXT NOT NULL,
			started_at   INTEGER NOT NULL,
			ended_at     INTEGER NOT NULL DEFAULT 0,
			status       TEXT NOT NULL DEFAULT '',
			bytes_in     INTEGER NOT NULL DEFAULT 0,
			bytes_out    INTEGER NOT NULL DEFAULT 0
		);
		CREATE INDEX IF NOT EXISTS idx_history_path
		    ON session_history(config_path, id DESC);
	`
	if _, err := db.Exec(historySchema); err != nil {
		return fmt.Errorf("migrate history: %w", err)
	}
	// Additive column migrations for session_history — same idempotent
	// pattern as the overlays table above. session_path arrived in
	// the multi-process refactor; before it, history rows were keyed
	// by an in-memory map and couldn't be closed across process
	// boundaries (TUI vs desklet CLI vs external tooling).
	histCols, err := existingColumns(db, "session_history")
	if err != nil {
		return err
	}
	histAdditions := []struct {
		name string
		ddl  string
	}{
		{"session_path", `ALTER TABLE session_history ADD COLUMN session_path TEXT NOT NULL DEFAULT ''`},
	}
	for _, m := range histAdditions {
		if histCols[m.name] {
			continue
		}
		if _, err := db.Exec(m.ddl); err != nil {
			return fmt.Errorf("migrate add history.%s: %w", m.name, err)
		}
	}
	if _, err := db.Exec(
		`CREATE INDEX IF NOT EXISTS idx_history_session
		     ON session_history(session_path, ended_at)`,
	); err != nil {
		return fmt.Errorf("migrate history session index: %w", err)
	}
	// Sweep used to live here but had to move out: a short-lived
	// CLI process (e.g. `o3ui disconnect` invoked from the desklet)
	// opens the same DB and would otherwise mark the live session's
	// open row as "lost" before the disconnect itself ran. The sweep
	// is now an explicit method (SweepDanglingHistory) callable only
	// from contexts that know which sessions are *really* live.
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
	// Cascade history too — keeping orphan rows after a profile is
	// gone is just dead weight on disk and would surface in /history
	// queries if the same path were ever re-used.
	if _, err := s.db.Exec(`DELETE FROM session_history WHERE config_path = ?`, configPath); err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

// RecordHistoryStart inserts a new history row with ended_at == 0 and
// trims older rows so the per-profile cap is enforced eagerly. The
// session_path is the canonical close-key; storing it in the same
// table means any process can later finalise the row without needing
// to share in-memory state with the writer.
func (s *SQLite) RecordHistoryStart(configPath, sessionPath string, startedAt time.Time) (int64, error) {
	if configPath == "" {
		return 0, errors.New("overlay: empty config_path")
	}
	res, err := s.db.Exec(
		`INSERT INTO session_history (config_path, session_path, started_at) VALUES (?, ?, ?)`,
		configPath, sessionPath, startedAt.Unix(),
	)
	if err != nil {
		return 0, err
	}
	id, _ := res.LastInsertId()
	// Prune: keep only the HistoryCap newest rows for this profile.
	// Using id DESC because rows are inserted monotonically, so the
	// newest id == the newest entry without needing a started_at sort.
	if _, err := s.db.Exec(
		`DELETE FROM session_history
		 WHERE config_path = ?
		   AND id NOT IN (
		       SELECT id FROM session_history
		       WHERE config_path = ?
		       ORDER BY id DESC LIMIT ?
		   )`,
		configPath, configPath, HistoryCap,
	); err != nil {
		return id, err
	}
	return id, nil
}

// SweepDanglingHistory closes every still-open history row whose
// session_path is not in the supplied "live" set. The caller is
// expected to have just fetched the live session list from the bus —
// rows pointing at vanished sessions are genuinely orphaned (process
// crash, missed destroy event before this build). Rows whose session
// is still live are left alone so the desklet's "live" pulse keeps
// rendering.
//
// Endtime is set to started_at so the timeline shows "—" rather than
// inventing a duration we don't have. Idempotent — repeated calls
// find nothing to sweep on subsequent invocations.
func (s *SQLite) SweepDanglingHistory(liveSessionPaths []string) (int, error) {
	live := make(map[string]struct{}, len(liveSessionPaths))
	for _, p := range liveSessionPaths {
		live[p] = struct{}{}
	}
	rows, err := s.db.Query(
		`SELECT id, session_path FROM session_history WHERE ended_at = 0`,
	)
	if err != nil {
		return 0, err
	}
	type stale struct {
		id   int64
		path string
	}
	var staleRows []stale
	for rows.Next() {
		var (
			id   int64
			path string
		)
		if err := rows.Scan(&id, &path); err != nil {
			rows.Close()
			return 0, err
		}
		if _, ok := live[path]; ok {
			continue
		}
		staleRows = append(staleRows, stale{id: id, path: path})
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, err
	}
	for _, st := range staleRows {
		if _, err := s.db.Exec(
			`UPDATE session_history
			 SET ended_at = started_at, status = 'lost'
			 WHERE id = ?`,
			st.id,
		); err != nil {
			return 0, err
		}
	}
	return len(staleRows), nil
}

// CloseHistoryBySession patches the still-open row for a session path
// with the final timestamp, status, and counters. The `ended_at = 0`
// guard makes this idempotent — concurrent finalisers (e.g. our own
// Disconnect plus the watcher SessionDestroyedEvent) race safely:
// whoever updates first wins, subsequent calls update zero rows.
func (s *SQLite) CloseHistoryBySession(sessionPath string, endedAt time.Time, status string, bytesIn, bytesOut int64) (bool, error) {
	if sessionPath == "" {
		return false, nil
	}
	res, err := s.db.Exec(
		`UPDATE session_history
		 SET ended_at = ?, status = ?, bytes_in = ?, bytes_out = ?
		 WHERE session_path = ? AND ended_at = 0`,
		endedAt.Unix(), status, bytesIn, bytesOut, sessionPath,
	)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

// History reads the newest HistoryCap entries for a profile. Ordered
// newest-first so callers can render the timeline top-down without a
// reverse step.
func (s *SQLite) History(configPath string) ([]HistoryEntry, error) {
	rows, err := s.db.Query(
		`SELECT id, config_path, session_path, started_at, ended_at, status, bytes_in, bytes_out
		 FROM session_history
		 WHERE config_path = ?
		 ORDER BY id DESC
		 LIMIT ?`,
		configPath, HistoryCap,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []HistoryEntry
	for rows.Next() {
		var (
			e          HistoryEntry
			start, end int64
		)
		if err := rows.Scan(&e.ID, &e.ConfigPath, &e.SessionPath, &start, &end,
			&e.Status, &e.BytesIn, &e.BytesOut); err != nil {
			return nil, err
		}
		if start > 0 {
			e.StartedAt = time.Unix(start, 0)
		}
		if end > 0 {
			e.EndedAt = time.Unix(end, 0)
		}
		out = append(out, e)
	}
	return out, rows.Err()
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
