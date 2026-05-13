// Package cli implements the non-TUI subcommands of the o3ui binary —
// status, list, connect, disconnect, and the desklet install helpers.
// The TUI process is the primary user surface; these subcommands exist
// so external clients (the Cinnamon desklet, shell scripts) can drive
// openvpn3 through the same Service that the TUI uses.
package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	openapp "github.com/esivres/openvpn3ui/internal/app"
	"github.com/esivres/openvpn3ui/internal/overlay"
	"github.com/esivres/openvpn3ui/internal/ovpn"
	"github.com/esivres/openvpn3ui/internal/secrets"
)

// Dispatch routes argv[1:] to the right subcommand handler. Returns an
// exit code so main can os.Exit cleanly. If args is empty or the first
// token is not a known subcommand, returns -1 so the caller falls back
// to the TUI.
func Dispatch(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		return -1
	}
	switch args[0] {
	case "status":
		return runStatus(args[1:], stdout, stderr)
	case "list":
		return runList(args[1:], stdout, stderr)
	case "connect":
		return runConnect(args[1:], stdout, stderr)
	case "disconnect":
		return runDisconnect(args[1:], stdout, stderr)
	case "desklet":
		return runDesklet(args[1:], stdout, stderr)
	case "pipe-api":
		return runPipeAPI(args[1:], stdout, stderr)
	case "help", "-h", "--help":
		printUsage(stdout)
		return 0
	}
	return -1
}

func printUsage(w io.Writer) {
	fmt.Fprint(w, `o3ui — openvpn3 controller

Usage:
  o3ui                          launch the interactive TUI (default)
  o3ui status [--json]          print the active session, machine-readable
  o3ui list   [--json]          list imported profiles
  o3ui connect <name|path>      start the VPN for the given profile
  o3ui disconnect [<name>]      tear down the active session
  o3ui desklet install          install the Cinnamon desklet
  o3ui desklet uninstall        remove the Cinnamon desklet
`)
}

// buildService constructs a Service exactly the way the TUI does, so
// CLI commands hit the same code paths (auth chain, overlay, secrets).
// Returns the service plus a cleanup func to defer.
func buildService() (*openapp.Service, func(), error) {
	conn, err := ovpn.ConnectSystemBus()
	if err != nil {
		return nil, nil, fmt.Errorf("connect system bus: %w", err)
	}
	overlayPath, err := overlay.DefaultPath()
	if err != nil {
		conn.Close()
		return nil, nil, fmt.Errorf("overlay path: %w", err)
	}
	ov, err := overlay.Open(overlayPath)
	if err != nil {
		conn.Close()
		return nil, nil, fmt.Errorf("open overlay: %w", err)
	}
	secs := secrets.New()

	svc := openapp.New(
		ovpn.NewConfigManager(conn),
		sessionMgrAdapter{ovpn.NewSessionManager(conn)},
	)
	svc.SetStorage(ov, secs)
	svc.AttachBus(func() ovpn.Conn { return conn })
	// Auth chain: stored creds + auto-TOTP. No interactive prompt — the
	// CLI is non-interactive; if a credential is missing the connect
	// call fails and the user must use the TUI to enter it once.
	svc.SetAuth(openapp.ChainAuth{Layers: []openapp.Auth{
		openapp.NewStoredCredentialsAuth(ov, secs),
		openapp.NewAutoTOTPAuth(ov, secs),
	}})
	cleanup := func() {
		ov.Close()
		conn.Close()
	}
	return svc, cleanup, nil
}

type sessionMgrAdapter struct{ *ovpn.SessionManager }

func (a sessionMgrAdapter) Control(path string) openapp.SessionControl {
	return a.SessionManager.Control(path)
}

// resolveProfile takes a user-supplied "name or path" and returns the
// matching config path. Match by exact path first, then by case-fold
// substring on Name.
func resolveProfile(svc *openapp.Service, ref string) (ovpn.Config, error) {
	cfgs, err := svc.ListConfigs()
	if err != nil {
		return ovpn.Config{}, err
	}
	for _, c := range cfgs {
		if c.Path == ref {
			return c, nil
		}
	}
	low := strings.ToLower(ref)
	var hits []ovpn.Config
	for _, c := range cfgs {
		if strings.Contains(strings.ToLower(c.Name), low) {
			hits = append(hits, c)
		}
	}
	switch len(hits) {
	case 0:
		return ovpn.Config{}, fmt.Errorf("no profile matches %q", ref)
	case 1:
		return hits[0], nil
	default:
		names := make([]string, len(hits))
		for i, c := range hits {
			names[i] = c.Name
		}
		return ovpn.Config{}, fmt.Errorf("ambiguous %q matches: %s", ref, strings.Join(names, ", "))
	}
}

// ── status ───────────────────────────────────────────────────────────

// statusReport is the shape consumed by the desklet. JSON keys are
// snake_case; absent fields are omitted.
//
// `default_*` is what the desklet should show / drive when nothing is
// currently active: the user's favorite, or fallback the most recently
// connected, or the first profile in the list. This way a freshly
// added desklet — with no settings filled in — still has a sensible
// "Connect" target to point at.
type statusReport struct {
	Connected      bool    `json:"connected"`
	Connecting     bool    `json:"connecting,omitempty"`
	Profile        string  `json:"profile,omitempty"`
	ConfigPath     string  `json:"config_path,omitempty"`
	SessionPath    string  `json:"session_path,omitempty"`
	State          string  `json:"state"` // disconnected|connecting|connected|failed|unknown
	Message        string  `json:"message,omitempty"`
	Country        string  `json:"country,omitempty"`
	BytesIn        int64   `json:"bytes_in,omitempty"`
	BytesOut       int64   `json:"bytes_out,omitempty"`
	RateIn         int64   `json:"rate_in,omitempty"`  // bytes/sec, computed against cache
	RateOut        int64   `json:"rate_out,omitempty"` // bytes/sec
	UptimeSec      int64   `json:"uptime_sec,omitempty"`
	SparkIn        []int64 `json:"spark_in,omitempty"` // last 60 deltas
	SparkOut       []int64 `json:"spark_out,omitempty"`
	StartedAt      string  `json:"started_at,omitempty"`
	DefaultProfile string  `json:"default_profile,omitempty"`
	DefaultPath    string  `json:"default_path,omitempty"`
	DefaultCountry string  `json:"default_country,omitempty"`
	DefaultReason  string  `json:"default_reason,omitempty"` // favorite|last|first
}

func runStatus(args []string, stdout, stderr io.Writer) int {
	asJSON := false
	for _, a := range args {
		if a == "--json" || a == "-j" {
			asJSON = true
		}
	}
	svc, cleanup, err := buildService()
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	defer cleanup()

	rep, err := buildStatusReport(svc)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	if asJSON {
		enc := json.NewEncoder(stdout)
		enc.SetIndent("", "  ")
		_ = enc.Encode(rep)
		return 0
	}
	// Human-readable
	if !rep.Connected && !rep.Connecting {
		fmt.Fprintln(stdout, "● disconnected")
		return 0
	}
	if rep.Connecting {
		fmt.Fprintf(stdout, "● connecting · %s · %s\n", rep.Profile, rep.Message)
		return 0
	}
	fmt.Fprintf(stdout, "● connected · %s · ↓ %s/s ↑ %s/s · uptime %s\n",
		rep.Profile, humanBytes(rep.RateIn), humanBytes(rep.RateOut), humanDur(rep.UptimeSec))
	return 0
}

func buildStatusReport(svc *openapp.Service) (statusReport, error) {
	sessions, err := svc.ListSessions()
	if err != nil {
		return statusReport{}, err
	}
	// Pick the most "active" session — connected wins over connecting,
	// connecting wins over anything else. Disconnect ghosts are skipped.
	var pick *ovpn.Session
	for i := range sessions {
		s := sessions[i]
		if !s.Status.IsActive() {
			continue
		}
		if pick == nil || (s.Status.IsConnected() && !pick.Status.IsConnected()) {
			pick = &sessions[i]
		}
	}
	if pick == nil {
		rep := statusReport{State: "disconnected"}
		fillDefault(svc, &rep)
		return rep, nil
	}
	rep := statusReport{
		Profile:     pick.ConfigName,
		ConfigPath:  pick.ConfigPath,
		SessionPath: pick.Path,
		Message:     pick.Status.Message,
	}
	if !pick.CreatedAt.IsZero() {
		rep.StartedAt = pick.CreatedAt.UTC().Format(time.RFC3339)
		rep.UptimeSec = int64(time.Since(pick.CreatedAt).Seconds())
	}
	// openvpn3 keeps the session around in "active" major after auth
	// fails (Minor 5 = AuthFailed). Without this check the desklet
	// would render an infinite Connecting… spinner with the failure
	// message as a subtitle — confusing, and the user has no Retry.
	switch {
	case pick.Status.IsConnected():
		rep.Connected = true
		rep.State = "connected"
	case pick.Status.Minor == ovpn.StatusConnAuthFailed ||
		strings.Contains(strings.ToLower(pick.Status.Message), "fail"):
		rep.State = "failed"
		if rep.Message == "" {
			rep.Message = "openvpn3 reported a failure"
		}
	default:
		rep.Connecting = true
		rep.State = "connecting"
	}
	// Pull stats; rate computed against cached prev sample.
	stats, _ := svc.SessionStatistics(pick.Path)
	rep.BytesIn = stats["BYTES_IN"]
	rep.BytesOut = stats["BYTES_OUT"]
	prev, _ := loadCache()
	now := time.Now()
	if prev != nil && prev.SessionPath == pick.Path && rep.Connected {
		dt := now.Sub(prev.At).Seconds()
		if dt > 0.05 {
			if d := rep.BytesIn - prev.BytesIn; d >= 0 {
				rep.RateIn = int64(float64(d) / dt)
			}
			if d := rep.BytesOut - prev.BytesOut; d >= 0 {
				rep.RateOut = int64(float64(d) / dt)
			}
		}
		// Sparkline: ring buffer of recent deltas. Copy the previous
		// slice into a fresh backing array — directly appending to
		// `prev.SparkIn` aliases the cache value, so a later append in
		// the same process would mutate already-serialised data.
		rep.SparkIn = append(append([]int64(nil), prev.SparkIn...), rep.RateIn)
		rep.SparkOut = append(append([]int64(nil), prev.SparkOut...), rep.RateOut)
		if len(rep.SparkIn) > 60 {
			rep.SparkIn = rep.SparkIn[len(rep.SparkIn)-60:]
		}
		if len(rep.SparkOut) > 60 {
			rep.SparkOut = rep.SparkOut[len(rep.SparkOut)-60:]
		}
	}
	// Even when connected, surface the default profile too — the
	// desklet may want to fall back to it after disconnect without an
	// extra `o3ui list` round-trip.
	fillDefault(svc, &rep)
	_ = saveCache(&statusCache{
		At:          now,
		SessionPath: pick.Path,
		BytesIn:     rep.BytesIn,
		BytesOut:    rep.BytesOut,
		SparkIn:     rep.SparkIn,
		SparkOut:    rep.SparkOut,
	})
	return rep, nil
}

// fillDefault picks the profile the desklet should show when nothing
// is active: favorite first, then most-recently-connected, then the
// first imported profile. Errors are swallowed — a missing default is
// strictly cosmetic.
func fillDefault(svc *openapp.Service, rep *statusReport) {
	cfgs, err := svc.ListConfigs()
	if err != nil || len(cfgs) == 0 {
		return
	}
	type cand struct {
		name, path, country string
		fav                 bool
		last                time.Time
	}
	cs := make([]cand, 0, len(cfgs))
	for _, c := range cfgs {
		cc := cand{name: c.Name, path: c.Path}
		if o, ok := svc.GetOverlay(c.Path); ok {
			cc.fav = o.Favorite
			cc.last = o.LastConnectedAt
			cc.country = o.CountryCode
		}
		cs = append(cs, cc)
	}
	// 1) any favorite — most-recently-used among them
	var pick *cand
	for i := range cs {
		if !cs[i].fav {
			continue
		}
		if pick == nil || cs[i].last.After(pick.last) {
			pick = &cs[i]
		}
	}
	if pick != nil {
		rep.DefaultProfile = pick.name
		rep.DefaultPath = pick.path
		rep.DefaultCountry = pick.country
		rep.DefaultReason = "favorite"
		return
	}
	// 2) most-recently-connected overall
	for i := range cs {
		if cs[i].last.IsZero() {
			continue
		}
		if pick == nil || cs[i].last.After(pick.last) {
			pick = &cs[i]
		}
	}
	if pick != nil {
		rep.DefaultProfile = pick.name
		rep.DefaultPath = pick.path
		rep.DefaultCountry = pick.country
		rep.DefaultReason = "last"
		return
	}
	// 3) first imported
	rep.DefaultProfile = cs[0].name
	rep.DefaultPath = cs[0].path
	rep.DefaultCountry = cs[0].country
	rep.DefaultReason = "first"
}

// statusCache persists the last sample to compute byte-rate deltas
// across separate `o3ui status` invocations (the desklet polls every
// second; the CLI is otherwise stateless).
type statusCache struct {
	At          time.Time `json:"at"`
	SessionPath string    `json:"session_path"`
	BytesIn     int64     `json:"bytes_in"`
	BytesOut    int64     `json:"bytes_out"`
	SparkIn     []int64   `json:"spark_in,omitempty"`
	SparkOut    []int64   `json:"spark_out,omitempty"`
}

func cachePath() string {
	dir := os.Getenv("XDG_RUNTIME_DIR")
	if dir == "" {
		dir = filepath.Join(os.TempDir(), fmt.Sprintf("o3ui-%d", os.Getuid()))
	} else {
		dir = filepath.Join(dir, "o3ui")
	}
	_ = os.MkdirAll(dir, 0o700)
	return filepath.Join(dir, "status.json")
}

func loadCache() (*statusCache, error) {
	b, err := os.ReadFile(cachePath())
	if err != nil {
		return nil, err
	}
	var c statusCache
	if err := json.Unmarshal(b, &c); err != nil {
		return nil, err
	}
	// Stale samples (>5s old) are not useful — better to start fresh.
	if time.Since(c.At) > 5*time.Second {
		return nil, nil
	}
	return &c, nil
}

func saveCache(c *statusCache) error {
	b, err := json.Marshal(c)
	if err != nil {
		return err
	}
	// Atomic rename — the desklet polls every 2s; without this, a
	// concurrent `o3ui status` run (e.g. user typing the command in a
	// terminal while the desklet ticks) can race and produce a half-
	// written cache that fails the next loadCache JSON parse.
	return writeFileAtomic(cachePath(), b, 0o600)
}

// ── list ─────────────────────────────────────────────────────────────

func runList(args []string, stdout, stderr io.Writer) int {
	asJSON := false
	for _, a := range args {
		if a == "--json" || a == "-j" {
			asJSON = true
		}
	}
	svc, cleanup, err := buildService()
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	defer cleanup()
	cfgs, err := svc.ListConfigs()
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	if asJSON {
		type entry struct {
			Name        string `json:"name"`
			Path        string `json:"path"`
			Country     string `json:"country,omitempty"`
			Favorite    bool   `json:"favorite"`
			AutoConnect bool   `json:"auto_connect"`
		}
		out := make([]entry, 0, len(cfgs))
		for _, c := range cfgs {
			e := entry{Name: c.Name, Path: c.Path}
			if o, ok := svc.GetOverlay(c.Path); ok {
				e.Country = o.CountryCode
				e.Favorite = o.Favorite
				e.AutoConnect = o.AutoConnect
			}
			out = append(out, e)
		}
		enc := json.NewEncoder(stdout)
		enc.SetIndent("", "  ")
		_ = enc.Encode(out)
		return 0
	}
	for i, c := range cfgs {
		fmt.Fprintf(stdout, "%2d  %s\n", i, c.Name)
	}
	return 0
}

// ── connect / disconnect ─────────────────────────────────────────────

func runConnect(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprintln(stderr, "usage: o3ui connect <name|path>")
		return 2
	}
	svc, cleanup, err := buildService()
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	defer cleanup()
	cfg, err := resolveProfile(svc, args[0])
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	// Thread the timeout through to Service.Connect — previously we
	// raced a select{} against a goroutine writing sessionPath, which
	// was a data race and left the goroutine stuck holding the D-Bus
	// connection while cleanup() closed it. With context-aware Connect
	// the timeout itself cancels Auth.Provide and unwinds cleanly.
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	sessionPath, err := svc.Connect(ctx, cfg.Path)
	if err != nil {
		if ctx.Err() != nil {
			fmt.Fprintln(stderr, "connect timed out after 60s")
		} else {
			fmt.Fprintf(stderr, "connect failed: %v\n", err)
		}
		return 1
	}
	fmt.Fprintf(stdout, "connected · %s · %s\n", cfg.Name, sessionPath)
	return 0
}

func runDisconnect(args []string, stdout, stderr io.Writer) int {
	svc, cleanup, err := buildService()
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	defer cleanup()
	sessions, err := svc.ActiveSessions()
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	if len(sessions) == 0 {
		fmt.Fprintln(stdout, "no active session")
		return 0
	}
	var target *ovpn.Session
	if len(args) > 0 {
		ref := strings.ToLower(args[0])
		for i := range sessions {
			if sessions[i].Path == args[0] ||
				strings.Contains(strings.ToLower(sessions[i].ConfigName), ref) {
				target = &sessions[i]
				break
			}
		}
		if target == nil {
			fmt.Fprintf(stderr, "no active session matches %q\n", args[0])
			return 1
		}
	} else {
		target = &sessions[0]
	}
	if err := svc.Disconnect(target.Path); err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	fmt.Fprintf(stdout, "disconnected · %s\n", target.ConfigName)
	return 0
}

// ── helpers ──────────────────────────────────────────────────────────

func humanBytes(n int64) string {
	switch {
	case n < 1024:
		return fmt.Sprintf("%d B", n)
	case n < 1024*1024:
		return fmt.Sprintf("%.1f KB", float64(n)/1024)
	case n < 1024*1024*1024:
		return fmt.Sprintf("%.1f MB", float64(n)/(1024*1024))
	default:
		return fmt.Sprintf("%.2f GB", float64(n)/(1024*1024*1024))
	}
}

func humanDur(sec int64) string {
	d := time.Duration(sec) * time.Second
	h := int(d.Hours())
	m := int(d.Minutes()) % 60
	s := int(d.Seconds()) % 60
	if h > 0 {
		return fmt.Sprintf("%02d:%02d:%02d", h, m, s)
	}
	return fmt.Sprintf("%02d:%02d", m, s)
}
