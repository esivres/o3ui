// Package app is the orchestration layer between UI and domain packages.
// UI talks only to *Service; domain packages know nothing about Fyne.
package app

import (
	"context"
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/esivres/openvpn3ui/internal/otp"
	"github.com/esivres/openvpn3ui/internal/overlay"
	"github.com/esivres/openvpn3ui/internal/ovpn"
	"github.com/esivres/openvpn3ui/internal/secrets"
)

// ConfigBackend is the slice of ovpn.ConfigManager the service uses.
type ConfigBackend interface {
	List() ([]ovpn.Config, error)
	Import(name, profile string, persistent bool) (string, error)
	Remove(path string) error
	// Fetch returns the original .ovpn body for an existing config path.
	// Used by the portable-profile exporter so a backup carries the
	// real config text alongside our overlay metadata.
	Fetch(path string) (string, error)
}

// SessionBackend is the slice of ovpn.SessionManager the service uses.
type SessionBackend interface {
	List() ([]ovpn.Session, error)
	NewTunnel(configPath string) (string, error)
	Control(path string) SessionControl
}

// SessionControl mirrors *ovpn.SessionController, kept narrow for fakes.
type SessionControl interface {
	Ready() error
	Connect() error
	Disconnect() error
	PendingInputs() ([]ovpn.InputPrompt, error)
	ProvideInput(p ovpn.InputPrompt, value string) error
	Statistics() (map[string]int64, error)
	LogVerbosity() (uint32, error)
	SetLogVerbosity(uint32) error
}

// LogLevel is the validated 1..6 range the UI presents (1=fatal, 6=verbose).
// Stored as a uint8 internally; converted to/from the uint32 the openvpn3
// API uses at the boundary.
type LogLevel uint8

const (
	LogFatal   LogLevel = 1
	LogError   LogLevel = 2
	LogWarning LogLevel = 3
	LogInfo    LogLevel = 4
	LogDebug   LogLevel = 5
	LogVerbose LogLevel = 6

	defaultLogLevel = LogInfo
)

// SessionStatistics returns the openvpn3 byte/packet counters for a
// live session. Mirrors the SessionControl method one level up so CLI
// callers (the desklet) don't have to thread the backend themselves.
func (s *Service) SessionStatistics(sessionPath string) (map[string]int64, error) {
	return s.sessions.Control(sessionPath).Statistics()
}

// SessionLogLevel returns the current verbosity of an existing session.
// Service-side wrapper so the TUI doesn't see the (uint32, error) shape.
func (s *Service) SessionLogLevel(sessionPath string) (LogLevel, error) {
	v, err := s.sessions.Control(sessionPath).LogVerbosity()
	if err != nil {
		return 0, err
	}
	return LogLevel(v), nil
}

// SetSessionLogLevel changes verbosity on a live session.
func (s *Service) SetSessionLogLevel(sessionPath string, l LogLevel) error {
	if l < LogFatal || l > LogVerbose {
		return fmt.Errorf("log level out of range: %d", l)
	}
	return s.sessions.Control(sessionPath).SetLogVerbosity(uint32(l))
}

// BackendServices wraps ovpn.ListBackendServices so the TUI doesn't
// need to know about ovpn types directly. Requires AttachBus to have
// been called first.
func (s *Service) BackendServices() ([]ovpn.BackendService, error) {
	if s.connFn == nil {
		return nil, errors.New("backend services lookup not configured")
	}
	return ovpn.ListBackendServices(s.connFn())
}

// AttachBus wires the system-bus connection accessor into the service so
// helpers like BackendServices can introspect it. Optional — without it,
// BackendServices returns an error.
func (s *Service) AttachBus(connFn func() ovpn.Conn) { s.connFn = connFn }

// PreferredLogLevel returns the level to apply to *future* sessions on
// Connect. Stored as a service-wide preference; for v1 we keep this in
// memory only — a settings-file backing store comes later.
func (s *Service) PreferredLogLevel() LogLevel {
	if s.prefLog == 0 {
		return defaultLogLevel
	}
	return s.prefLog
}

// SetPreferredLogLevel updates the preference used by future Connect calls.
func (s *Service) SetPreferredLogLevel(l LogLevel) error {
	if l < LogFatal || l > LogVerbose {
		return fmt.Errorf("log level out of range: %d", l)
	}
	s.prefLog = l
	return nil
}

// Auth fills UserInput challenges raised by openvpn3 during Connect. A
// concrete Auth typically composes layers: an automatic TOTP source first,
// a UI prompt as a fallback. ErrAuthCancelled lets a layer say "I don't
// know how to answer this — pass it on / give up".
type Auth interface {
	Provide(ctx context.Context, configPath string, prompt ovpn.InputPrompt) (string, error)
}

// ErrAuthCancelled means the auth chain could not — or would not — answer
// a prompt. Service.Connect propagates it so the caller can show a clean
// "cancelled" message rather than a generic D-Bus error.
var ErrAuthCancelled = errors.New("auth cancelled")

// maxAuthIterations bounds the Ready/PendingInputs loop. openvpn3 normally
// asks for everything in one batch, but a server can re-prompt; cap so a
// misbehaving config can't spin us forever.
const maxAuthIterations = 5

// OverlayStore is the subset of overlay.Store used by Service. Kept narrow
// so the UI tests don't need a real SQLite.
type OverlayStore interface {
	Get(configPath string) (overlay.Overlay, error)
	Upsert(o overlay.Overlay) error
	Delete(configPath string) error
}

type Service struct {
	configs  ConfigBackend
	sessions SessionBackend
	overlay  OverlayStore
	secrets  secrets.Store
	auth     Auth             // optional; nil means "no auto-fill, fail on missing input"
	sampler  *Sampler         // optional; populated via AttachSampler
	prefLog  LogLevel         // 0 == use defaultLogLevel
	connFn   func() ovpn.Conn // optional; populated via AttachBus
}

func New(configs ConfigBackend, sessions SessionBackend) *Service {
	return &Service{configs: configs, sessions: sessions}
}

// SetAuth wires in a UserInput resolver. Call once at startup.
func (s *Service) SetAuth(a Auth) { s.auth = a }

// SetStorage wires the overlay DB and credential keyring used by the OTP
// management methods. Optional — without it, the OTP setters return errors.
func (s *Service) SetStorage(o OverlayStore, sec secrets.Store) {
	s.overlay = o
	s.secrets = sec
}

// otpSecretID encodes the keyring key for a config's TOTP secret. Stable
// across runs so a re-attached overlay finds its secret.
func otpSecretID(configPath string) string { return "totp:" + configPath }

// HasOTP reports whether a TOTP secret is currently attached to the config.
// Used by the UI to render "Setup OTP" vs "Remove OTP".
func (s *Service) HasOTP(configPath string) bool {
	if s.overlay == nil {
		return false
	}
	o, err := s.overlay.Get(configPath)
	if err != nil {
		return false
	}
	return o.OTPSecretID != ""
}

// SetOTP validates the base32 secret, stores it in the keyring, and links
// it to the config in the overlay. Returns an error if the secret can't be
// decoded — better to refuse upfront than to fail at connect time.
func (s *Service) SetOTP(configPath, base32Secret string) error {
	if s.overlay == nil || s.secrets == nil {
		return errors.New("OTP storage not configured")
	}
	if _, err := otp.DecodeBase32Secret(base32Secret); err != nil {
		return fmt.Errorf("invalid base32 secret: %w", err)
	}
	id := otpSecretID(configPath)
	if err := s.secrets.Set(id, base32Secret); err != nil {
		return err
	}
	o, err := s.overlay.Get(configPath)
	if err != nil {
		o = overlay.Overlay{ConfigPath: configPath}
	}
	o.OTPSecretID = id
	return s.overlay.Upsert(o)
}

// RemoveOTP deletes the OTP secret from the keyring and clears the link in
// the overlay. Idempotent: deleting an already-absent secret is not an error.
func (s *Service) RemoveOTP(configPath string) error {
	if s.overlay == nil || s.secrets == nil {
		return errors.New("OTP storage not configured")
	}
	id := otpSecretID(configPath)
	if err := s.secrets.Delete(id); err != nil && !errors.Is(err, secrets.ErrNotFound) {
		return err
	}
	o, err := s.overlay.Get(configPath)
	if errors.Is(err, overlay.ErrNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	o.OTPSecretID = ""
	return s.overlay.Upsert(o)
}

// GetOverlay returns the metadata blob stored alongside an openvpn3 config.
// Missing entries surface as (zero, false) — never an error — so call sites
// don't need to disambiguate "no overlay yet" from "DB failure".
func (s *Service) GetOverlay(configPath string) (overlay.Overlay, bool) {
	if s.overlay == nil {
		return overlay.Overlay{}, false
	}
	o, err := s.overlay.Get(configPath)
	if err != nil {
		return overlay.Overlay{}, false
	}
	return o, true
}

// SetFavorite toggles the favorite flag for a config. Used by the TUI's
// star action on the profile list.
func (s *Service) SetFavorite(configPath string, on bool) error {
	return s.mutateOverlay(configPath, func(o *overlay.Overlay) { o.Favorite = on })
}

// SetAutoConnect marks a profile to auto-connect on login. The actual
// auto-connect runner is out of scope for v1 — we just persist the flag
// so a future systemd-user-service can act on it.
func (s *Service) SetAutoConnect(configPath string, on bool) error {
	return s.mutateOverlay(configPath, func(o *overlay.Overlay) { o.AutoConnect = on })
}

// SetCountryCode stores an optional short label (e.g. "DE"/"SE") shown next
// to the profile name in the list. Empty string clears it.
func (s *Service) SetCountryCode(configPath, code string) error {
	return s.mutateOverlay(configPath, func(o *overlay.Overlay) { o.CountryCode = code })
}

// markConnected stamps LastConnectedAt = now after a successful Connect.
// Failures are swallowed deliberately: a flaky overlay write must not turn
// a working tunnel into a reported error.
func (s *Service) markConnected(configPath string) {
	_ = s.mutateOverlay(configPath, func(o *overlay.Overlay) { o.LastConnectedAt = time.Now() })
}

func (s *Service) mutateOverlay(configPath string, fn func(*overlay.Overlay)) error {
	if s.overlay == nil {
		return errors.New("overlay storage not configured")
	}
	o, err := s.overlay.Get(configPath)
	if errors.Is(err, overlay.ErrNotFound) {
		o = overlay.Overlay{ConfigPath: configPath}
	} else if err != nil {
		return err
	}
	fn(&o)
	return s.overlay.Upsert(o)
}

// passwordSecretID is the keyring key under which we store the VPN
// password for a given config. Stable across restarts.
func passwordSecretID(configPath string) string { return "pwd:" + configPath }

// SetCredentials remembers a username/password pair for a config. The
// username goes into the overlay (plaintext — it is not a secret), the
// password into the keyring. Either may be empty to clear that side; if
// both are empty the stored secret is removed entirely.
func (s *Service) SetCredentials(configPath, username, password string) error {
	if s.overlay == nil || s.secrets == nil {
		return errors.New("credentials storage not configured")
	}
	if password == "" {
		// Remove any prior secret so we don't leak a stale one.
		_ = s.secrets.Delete(passwordSecretID(configPath)) // ignore ErrNotFound
		return s.mutateOverlay(configPath, func(o *overlay.Overlay) {
			o.Username = username
			o.PasswordSecretID = ""
		})
	}
	id := passwordSecretID(configPath)
	if err := s.secrets.Set(id, password); err != nil {
		return err
	}
	return s.mutateOverlay(configPath, func(o *overlay.Overlay) {
		o.Username = username
		o.PasswordSecretID = id
	})
}

// GetCredentials returns the stored username and password for a config.
// (username, password, hasPassword) — the boolean is true only when a
// password is on file (a stored username alone is not enough to skip the
// auth prompt). Missing data is not an error.
func (s *Service) GetCredentials(configPath string) (string, string, bool) {
	if s.overlay == nil || s.secrets == nil {
		return "", "", false
	}
	o, err := s.overlay.Get(configPath)
	if err != nil {
		return "", "", false
	}
	if o.PasswordSecretID == "" {
		return o.Username, "", false
	}
	pwd, err := s.secrets.Get(o.PasswordSecretID)
	if err != nil {
		return o.Username, "", false
	}
	return o.Username, pwd, true
}

// ClearCredentials forgets both the username and the stored password.
func (s *Service) ClearCredentials(configPath string) error {
	return s.SetCredentials(configPath, "", "")
}

// RememberUsername stores only the username, leaving any saved password
// alone. Used by the auth-prompt modal when the user ticks "remember"
// on the username field.
func (s *Service) RememberUsername(configPath, username string) error {
	if s.overlay == nil {
		return errors.New("credentials storage not configured")
	}
	return s.mutateOverlay(configPath, func(o *overlay.Overlay) { o.Username = username })
}

// RememberPassword stores only the password, leaving any saved username
// alone. Empty value clears the stored password.
func (s *Service) RememberPassword(configPath, password string) error {
	if s.overlay == nil || s.secrets == nil {
		return errors.New("credentials storage not configured")
	}
	id := passwordSecretID(configPath)
	if password == "" {
		_ = s.secrets.Delete(id)
		return s.mutateOverlay(configPath, func(o *overlay.Overlay) { o.PasswordSecretID = "" })
	}
	if err := s.secrets.Set(id, password); err != nil {
		return err
	}
	return s.mutateOverlay(configPath, func(o *overlay.Overlay) { o.PasswordSecretID = id })
}

// PreviewOTP generates the current TOTP code for a config, for use in the
// setup dialog's live preview. Returns ("", false) if no secret is attached.
func (s *Service) PreviewOTP(configPath string) (string, bool) {
	if s.overlay == nil || s.secrets == nil {
		return "", false
	}
	o, err := s.overlay.Get(configPath)
	if err != nil || o.OTPSecretID == "" {
		return "", false
	}
	raw, err := s.secrets.Get(o.OTPSecretID)
	if err != nil {
		return "", false
	}
	secret, err := otp.DecodeBase32Secret(raw)
	if err != nil {
		return "", false
	}
	return otp.Now(otp.Config{Secret: secret}), true
}

func (s *Service) ListConfigs() ([]ovpn.Config, error)   { return s.configs.List() }
func (s *Service) ListSessions() ([]ovpn.Session, error) { return s.sessions.List() }

// ActiveSessions returns only sessions that are connecting or connected.
// openvpn3 may keep a session object listed after Disconnect, so callers
// driving UI state ("show Disconnect button?", "is this profile in use?")
// must filter — using ListSessions directly causes the UI to keep showing
// a tunnel as active long after it was torn down.
func (s *Service) ActiveSessions() ([]ovpn.Session, error) {
	all, err := s.sessions.List()
	if err != nil {
		return nil, err
	}
	out := all[:0]
	// Index-based — Session ~128B; in-place filter, no extra copy.
	for i := range all {
		if all[i].Status.IsActive() {
			out = append(out, all[i])
		}
	}
	return out, nil
}

// ImportFromFile reads a .ovpn file and pushes its contents to openvpn3.
// `name` is the human-readable label shown in the UI / openvpn3 list.
func (s *Service) ImportFromFile(name, path string) (string, error) {
	body, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	return s.configs.Import(name, string(body), true)
}

// Connect creates a tunnel for the given config and drives it through
// auth challenges until it is either Ready (and connected) or the auth
// chain refuses. The session path is returned even on failure so the
// caller can clean up or retry.
//
// ctx is checked at each Auth.Provide boundary so the caller (TUI Esc,
// CLI timeout) can cancel a stuck prompt without leaking the goroutine.
// Without context, a Prompter.Ask blocked on user input would keep its
// channel alive forever when the user dismissed the connecting screen.
func (s *Service) Connect(ctx context.Context, configPath string) (string, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	sessionPath, err := s.sessions.NewTunnel(configPath)
	if err != nil {
		return "", err
	}
	ctl := s.sessions.Control(sessionPath)

	for i := 0; i < maxAuthIterations; i++ {
		if err := ctx.Err(); err != nil {
			return sessionPath, err
		}
		err := ctl.Ready()
		if err == nil {
			break // all inputs provided
		}
		// Ready failed — typical reason: pending UserInput queues. Fetch
		// them and try to fill via Auth.
		prompts, perr := ctl.PendingInputs()
		if perr != nil {
			return sessionPath, fmt.Errorf("ready=%w; queue=%w", err, perr)
		}
		if len(prompts) == 0 {
			// Ready failed for a non-input reason. Surface it.
			return sessionPath, err
		}
		if s.auth == nil {
			return sessionPath, fmt.Errorf("%d pending inputs, no auth handler: %w", len(prompts), err)
		}
		for _, p := range prompts {
			value, perr := s.auth.Provide(ctx, configPath, p)
			if perr != nil {
				return sessionPath, perr
			}
			if err := ctl.ProvideInput(p, value); err != nil {
				return sessionPath, err
			}
		}
	}
	if err := ctl.Connect(); err != nil {
		return sessionPath, err
	}
	// Apply user's preferred log verbosity to the fresh session. Failures
	// are non-fatal — verbosity is a debugging aid, not a connectivity
	// requirement.
	_ = ctl.SetLogVerbosity(uint32(s.PreferredLogLevel()))
	s.markConnected(configPath)
	return sessionPath, nil
}

// Disconnect tears down a session by its D-Bus path.
func (s *Service) Disconnect(sessionPath string) error {
	return s.sessions.Control(sessionPath).Disconnect()
}
