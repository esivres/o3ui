package app_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/esivres/openvpn3ui/internal/app"
	"github.com/esivres/openvpn3ui/internal/overlay"
	"github.com/esivres/openvpn3ui/internal/ovpn"
	"github.com/esivres/openvpn3ui/internal/secrets"
)

// fakeConfigs implements app.ConfigBackend.
type fakeConfigs struct {
	list      []ovpn.Config
	importErr error
	importedN string
	importedP string
	imported  bool
	removed   string
}

func (f *fakeConfigs) List() ([]ovpn.Config, error) { return f.list, nil }
func (f *fakeConfigs) Import(name, profile string, _ bool) (string, error) {
	if f.importErr != nil {
		return "", f.importErr
	}
	f.imported = true
	f.importedN = name
	f.importedP = profile
	return "/net/openvpn/v3/configuration/x", nil
}
func (f *fakeConfigs) Remove(path string) error { f.removed = path; return nil }
func (f *fakeConfigs) Fetch(string) (string, error) {
	return "client\ndev tun\nremote vpn.test 1194\n", nil
}
func (f *fakeConfigs) Rename(string, string) error { return nil }
func (f *fakeConfigs) FetchProperties(string) (ovpn.ConfigProperties, error) {
	return ovpn.ConfigProperties{}, nil
}
func (f *fakeConfigs) SetBoolProperty(string, string, bool) error { return nil }
func (f *fakeConfigs) Overrides(string) ([]ovpn.Override, error)  { return nil, nil }
func (f *fakeConfigs) SetOverride(string, string, string) error   { return nil }
func (f *fakeConfigs) UnsetOverride(string, string) error         { return nil }

// fakeSessions implements app.SessionBackend.
type fakeSessions struct {
	list      []ovpn.Session
	tunnelErr error
	newTunnel string
	ctl       *fakeCtl
}

func (f *fakeSessions) List() ([]ovpn.Session, error) { return f.list, nil }
func (f *fakeSessions) Get(path string) (ovpn.Session, error) {
	for i := range f.list {
		if f.list[i].Path == path {
			return f.list[i], nil
		}
	}
	return ovpn.Session{}, errors.New("session not found")
}
func (f *fakeSessions) NewTunnel(_ string) (string, error) {
	if f.tunnelErr != nil {
		return "", f.tunnelErr
	}
	return f.newTunnel, nil
}
func (f *fakeSessions) Control(_ string) app.SessionControl { return f.ctl }

type fakeCtl struct {
	readyErr      error
	readyErrs     []error // pop one per Ready() call; empty = use readyErr
	connectErr    error
	disconnectErr error
	calls         []string

	pending    []ovpn.InputPrompt
	provided   map[uint32]string // id → value
	provideErr error
	logLevel   uint32
}

func (c *fakeCtl) Ready() error {
	c.calls = append(c.calls, "Ready")
	if len(c.readyErrs) > 0 {
		err := c.readyErrs[0]
		c.readyErrs = c.readyErrs[1:]
		return err
	}
	return c.readyErr
}
func (c *fakeCtl) Connect() error    { c.calls = append(c.calls, "Connect"); return c.connectErr }
func (c *fakeCtl) Disconnect() error { c.calls = append(c.calls, "Disconnect"); return c.disconnectErr }
func (c *fakeCtl) PendingInputs() ([]ovpn.InputPrompt, error) {
	c.calls = append(c.calls, "PendingInputs")
	out := c.pending
	c.pending = nil // empty after the first fetch — emulate openvpn3 dequeue
	return out, nil
}
func (c *fakeCtl) ProvideInput(p ovpn.InputPrompt, value string) error {
	c.calls = append(c.calls, "ProvideInput:"+p.Name)
	if c.provideErr != nil {
		return c.provideErr
	}
	if c.provided == nil {
		c.provided = map[uint32]string{}
	}
	c.provided[p.ID] = value
	return nil
}
func (c *fakeCtl) Statistics() (map[string]int64, error) { return map[string]int64{}, nil }
func (c *fakeCtl) LogVerbosity() (uint32, error)         { return c.logLevel, nil }
func (c *fakeCtl) SetLogVerbosity(l uint32) error {
	c.logLevel = l
	c.calls = append(c.calls, "SetLogVerbosity")
	return nil
}
func (c *fakeCtl) LogForward(bool) error { return nil }

func TestService_ListConfigsAndSessions(t *testing.T) {
	cfg := &fakeConfigs{list: []ovpn.Config{{Path: "/p", Name: "home", Valid: true}}}
	sess := &fakeSessions{list: []ovpn.Session{{Path: "/s", ConfigName: "home"}}}
	svc := app.New(cfg, sess)

	configs, err := svc.ListConfigs()
	require.NoError(t, err)
	require.Equal(t, "home", configs[0].Name)

	sessions, err := svc.ListSessions()
	require.NoError(t, err)
	require.Equal(t, "/s", sessions[0].Path)
}

func TestService_ImportFromFile(t *testing.T) {
	tmp := t.TempDir()
	p := filepath.Join(tmp, "client.ovpn")
	require.NoError(t, os.WriteFile(p, []byte("client\nremote vpn.example\n"), 0o600))

	cfg := &fakeConfigs{}
	svc := app.New(cfg, &fakeSessions{ctl: &fakeCtl{}})

	path, err := svc.ImportFromFile("home", p)
	require.NoError(t, err)
	require.Equal(t, "/net/openvpn/v3/configuration/x", path)
	require.True(t, cfg.imported)
	require.Equal(t, "home", cfg.importedN)
	require.Contains(t, cfg.importedP, "remote vpn.example")
}

func TestService_ImportFromFile_MissingFile(t *testing.T) {
	cfg := &fakeConfigs{}
	svc := app.New(cfg, &fakeSessions{ctl: &fakeCtl{}})

	_, err := svc.ImportFromFile("x", "/no/such/file.ovpn")
	require.Error(t, err)
	require.False(t, cfg.imported, "Import must not be attempted when the file is unreadable")
}

func TestService_Connect_HappyPath(t *testing.T) {
	ctl := &fakeCtl{}
	svc := app.New(&fakeConfigs{}, &fakeSessions{newTunnel: "/s/abc", ctl: ctl})

	path, err := svc.Connect(context.Background(), "/cfg/1")
	require.NoError(t, err)
	require.Equal(t, "/s/abc", path)
	// Ready precedes Connect; SetLogVerbosity may follow as a non-fatal
	// best-effort call after the tunnel is up.
	require.Equal(t, "Ready", ctl.calls[0])
	require.Equal(t, "Connect", ctl.calls[1])
}

func TestService_Connect_ReadyFailureSurfaces_NoAuth(t *testing.T) {
	// Without an auth handler, a pending-input Ready failure must propagate.
	ctl := &fakeCtl{
		readyErr: errors.New("user input required"),
		pending:  []ovpn.InputPrompt{{ID: 1, Name: "username"}},
	}
	svc := app.New(&fakeConfigs{}, &fakeSessions{newTunnel: "/s/abc", ctl: ctl})

	path, err := svc.Connect(context.Background(), "/cfg/1")
	require.Error(t, err)
	require.Equal(t, "/s/abc", path)
	require.NotContains(t, ctl.calls, "Connect", "Connect must not be reached without auth")
}

func TestService_Connect_AutoTOTPFulfilsChallenge(t *testing.T) {
	// Two prompts: username (handled by fallback closure) and
	// static_challenge (handled automatically by AutoTOTPAuth).
	// Ready fails the first time, succeeds after both inputs are provided.
	ctl := &fakeCtl{
		readyErrs: []error{errors.New("input required"), nil},
		pending: []ovpn.InputPrompt{
			{ID: 1, Name: "username"},
			{ID: 2, Name: "static_challenge", Description: "Enter your OTP"},
		},
	}
	sess := &fakeSessions{newTunnel: "/s/abc", ctl: ctl}

	// Wire up AutoTOTPAuth with a known secret. RFC 6238 SHA-1 vector at
	// t=59s yields 94287082 (8 digits). With default 6 digits we expect
	// the last 6 of that — but easier to assert "non-empty + 6 digits".
	ov := &fakeOverlay{m: map[string]overlay.Overlay{
		"/cfg/1": {ConfigPath: "/cfg/1", OTPSecretID: "id-1"},
	}}
	secs := secrets.NewMemory()
	require.NoError(t, secs.Set("id-1", "JBSWY3DPEHPK3PXP")) // base32-encoded test secret

	auth := app.ChainAuth{Layers: []app.Auth{
		app.NewAutoTOTPAuth(ov, secs),
		app.AuthFunc(func(_ context.Context, _ string, p ovpn.InputPrompt) (string, error) {
			if p.Name == "username" {
				return "alice", nil
			}
			return "", errors.New("unexpected prompt: " + p.Name)
		}),
	}}

	svc := app.New(&fakeConfigs{}, sess)
	svc.SetAuth(auth)

	path, err := svc.Connect(context.Background(), "/cfg/1")
	require.NoError(t, err)
	require.Equal(t, "/s/abc", path)

	require.Equal(t, "alice", ctl.provided[1], "username prompt must be answered by fallback")
	require.Len(t, ctl.provided[2], 6, "OTP prompt must be answered with a 6-digit code")
	require.Regexp(t, `^\d{6}$`, ctl.provided[2])

	// Sanity check on call ordering: Ready, PendingInputs, ProvideInput x2,
	// Ready (success), Connect.
	require.Contains(t, ctl.calls, "Connect")
}

type fakeOverlay struct{ m map[string]overlay.Overlay }

func (f *fakeOverlay) Get(p string) (overlay.Overlay, error) {
	o, ok := f.m[p]
	if !ok {
		return overlay.Overlay{}, overlay.ErrNotFound
	}
	return o, nil
}

func TestService_Connect_NewTunnelFailureBubblesUp(t *testing.T) {
	svc := app.New(&fakeConfigs{}, &fakeSessions{tunnelErr: errors.New("boom"), ctl: &fakeCtl{}})
	path, err := svc.Connect(context.Background(), "/cfg/1")
	require.Error(t, err)
	require.Empty(t, path)
}

func TestService_OTP_SetGetRemove(t *testing.T) {
	ov := newFakeOverlayStore()
	secs := secrets.NewMemory()
	svc := app.New(&fakeConfigs{}, &fakeSessions{ctl: &fakeCtl{}})
	svc.SetStorage(ov, secs)

	const cp = "/cfg/1"

	require.False(t, svc.HasOTP(cp), "no OTP attached initially")
	_, ok := svc.PreviewOTP(cp)
	require.False(t, ok)

	require.NoError(t, svc.SetOTP(cp, "JBSWY3DPEHPK3PXP"))
	require.True(t, svc.HasOTP(cp))

	code, ok := svc.PreviewOTP(cp)
	require.True(t, ok)
	require.Regexp(t, `^\d{6}$`, code)

	require.NoError(t, svc.RemoveOTP(cp))
	require.False(t, svc.HasOTP(cp))

	// Idempotent: removing again is fine.
	require.NoError(t, svc.RemoveOTP(cp))
}

func TestService_OTP_RejectsInvalidSecret(t *testing.T) {
	svc := app.New(&fakeConfigs{}, &fakeSessions{ctl: &fakeCtl{}})
	svc.SetStorage(newFakeOverlayStore(), secrets.NewMemory())
	require.Error(t, svc.SetOTP("/cfg/1", "not-base32-!!!"))
}

func TestService_OverlayFlags(t *testing.T) {
	ov := newFakeOverlayStore()
	svc := app.New(&fakeConfigs{}, &fakeSessions{ctl: &fakeCtl{}})
	svc.SetStorage(ov, secrets.NewMemory())

	const cp = "/cfg/1"
	require.NoError(t, svc.SetFavorite(cp, true))
	require.NoError(t, svc.SetAutoConnect(cp, true))
	require.NoError(t, svc.SetCountryCode(cp, "DE"))

	o, ok := svc.GetOverlay(cp)
	require.True(t, ok)
	require.True(t, o.Favorite)
	require.True(t, o.AutoConnect)
	require.Equal(t, "DE", o.CountryCode)

	require.NoError(t, svc.SetFavorite(cp, false))
	o, _ = svc.GetOverlay(cp)
	require.False(t, o.Favorite, "previous fields must be preserved across single-field updates")
	require.True(t, o.AutoConnect)
	require.Equal(t, "DE", o.CountryCode)
}

func TestService_Connect_StampsLastConnected(t *testing.T) {
	ov := newFakeOverlayStore()
	svc := app.New(&fakeConfigs{}, &fakeSessions{newTunnel: "/s/1", ctl: &fakeCtl{}})
	svc.SetStorage(ov, secrets.NewMemory())

	before := time.Now().Add(-time.Second)
	_, err := svc.Connect(context.Background(), "/cfg/1")
	require.NoError(t, err)

	o, ok := svc.GetOverlay("/cfg/1")
	require.True(t, ok)
	require.True(t, o.LastConnectedAt.After(before),
		"successful Connect must update LastConnectedAt; got %v", o.LastConnectedAt)
}

func TestService_Credentials_RoundTrip(t *testing.T) {
	ov := newFakeOverlayStore()
	secs := secrets.NewMemory()
	svc := app.New(&fakeConfigs{}, &fakeSessions{ctl: &fakeCtl{}})
	svc.SetStorage(ov, secs)

	const cp = "/cfg/1"
	require.NoError(t, svc.SetCredentials(cp, "alice", "s3cret"))

	user, pwd, ok := svc.GetCredentials(cp)
	require.True(t, ok)
	require.Equal(t, "alice", user)
	require.Equal(t, "s3cret", pwd)

	// Username-only update keeps the password.
	require.NoError(t, svc.SetCredentials(cp, "alice2", "s3cret"))
	user, pwd, ok = svc.GetCredentials(cp)
	require.True(t, ok)
	require.Equal(t, "alice2", user)
	require.Equal(t, "s3cret", pwd)

	// Empty password clears the secret but keeps the username.
	require.NoError(t, svc.SetCredentials(cp, "alice2", ""))
	user, pwd, ok = svc.GetCredentials(cp)
	require.False(t, ok, "stored password is gone, ok must be false")
	require.Equal(t, "alice2", user)
	require.Equal(t, "", pwd)

	// ClearCredentials wipes both.
	require.NoError(t, svc.ClearCredentials(cp))
	user, _, ok = svc.GetCredentials(cp)
	require.False(t, ok)
	require.Equal(t, "", user)
}

func TestService_Connect_AutoFillsStoredCredentials(t *testing.T) {
	// End-to-end: prompts for username + password get answered from
	// overlay/keyring without bothering a Prompter, then the (already
	// covered) AutoTOTPAuth handles the static_challenge.
	ctl := &fakeCtl{
		readyErrs: []error{errors.New("input required"), nil},
		pending: []ovpn.InputPrompt{
			{ID: 1, Name: "username"},
			{ID: 2, Name: "password", Hidden: true},
		},
	}
	sess := &fakeSessions{newTunnel: "/s/abc", ctl: ctl}

	ov := newFakeOverlayStore()
	secs := secrets.NewMemory()
	svc := app.New(&fakeConfigs{}, sess)
	svc.SetStorage(ov, secs)
	require.NoError(t, svc.SetCredentials("/cfg/1", "alice", "s3cret"))

	// Compose the realistic chain: stored creds first, then a Prompter
	// that would explode if reached (proving stored values were used).
	svc.SetAuth(app.ChainAuth{Layers: []app.Auth{
		app.NewStoredCredentialsAuth(ov, secs),
		app.NewPromptAuth(app.PrompterFunc(func(_ context.Context, _ string, p ovpn.InputPrompt) (string, error) {
			t.Fatalf("Prompter must not be invoked when credentials are stored; got %q", p.Name)
			return "", nil
		})),
	}})

	_, err := svc.Connect(context.Background(), "/cfg/1")
	require.NoError(t, err)
	require.Equal(t, "alice", ctl.provided[1])
	require.Equal(t, "s3cret", ctl.provided[2])
}

func TestService_OverlayFlags_RequireStorage(t *testing.T) {
	svc := app.New(&fakeConfigs{}, &fakeSessions{ctl: &fakeCtl{}})
	require.Error(t, svc.SetFavorite("/x", true), "must fail when SetStorage was not called")
}

func TestService_OTP_RequiresStorage(t *testing.T) {
	svc := app.New(&fakeConfigs{}, &fakeSessions{ctl: &fakeCtl{}})
	require.Error(t, svc.SetOTP("/cfg/1", "JBSWY3DPEHPK3PXP"),
		"SetOTP without SetStorage must fail clearly")
}

// fakeOverlayStore implements app.OverlayStore.
type fakeOverlayStore struct {
	m map[string]overlay.Overlay
}

func newFakeOverlayStore() *fakeOverlayStore {
	return &fakeOverlayStore{m: map[string]overlay.Overlay{}}
}

func (f *fakeOverlayStore) Get(p string) (overlay.Overlay, error) {
	o, ok := f.m[p]
	if !ok {
		return overlay.Overlay{}, overlay.ErrNotFound
	}
	return o, nil
}
func (f *fakeOverlayStore) Upsert(o overlay.Overlay) error { f.m[o.ConfigPath] = o; return nil }
func (f *fakeOverlayStore) Delete(p string) error {
	if _, ok := f.m[p]; !ok {
		return overlay.ErrNotFound
	}
	delete(f.m, p)
	return nil
}
func (f *fakeOverlayStore) RecordHistoryStart(string, string, time.Time) (int64, error) {
	return 0, nil
}
func (f *fakeOverlayStore) CloseHistoryBySession(string, time.Time, string, int64, int64) (bool, error) {
	return false, nil
}
func (f *fakeOverlayStore) History(string) ([]overlay.HistoryEntry, error) { return nil, nil }

func TestService_ActiveSessions_FiltersDisconnected(t *testing.T) {
	// Backend reports three sessions: one connected, one connecting,
	// one in StatusConnDisconnect (the lingering ghost openvpn3 leaves
	// behind after Disconnect). ActiveSessions must hide the ghost.
	all := []ovpn.Session{
		{Path: "/s/up", Status: ovpn.Status{Major: ovpn.StatusMajorConnection, Minor: ovpn.StatusConnConnected}},
		{Path: "/s/connecting", Status: ovpn.Status{Major: ovpn.StatusMajorConnection, Minor: ovpn.StatusConnConnecting}},
		{Path: "/s/ghost", Status: ovpn.Status{Major: ovpn.StatusMajorConnection, Minor: ovpn.StatusConnDisconnect}},
	}
	svc := app.New(&fakeConfigs{}, &fakeSessions{list: all, ctl: &fakeCtl{}})

	listed, err := svc.ListSessions()
	require.NoError(t, err)
	require.Len(t, listed, 3, "ListSessions surfaces every session, including ghosts (admin view)")

	active, err := svc.ActiveSessions()
	require.NoError(t, err)
	require.Len(t, active, 2)
	paths := []string{active[0].Path, active[1].Path}
	require.NotContains(t, paths, "/s/ghost")
	require.Contains(t, paths, "/s/up")
	require.Contains(t, paths, "/s/connecting")
}

func TestStatus_IsActive(t *testing.T) {
	cases := []struct {
		name   string
		status ovpn.Status
		want   bool
	}{
		{"connected", ovpn.Status{Major: ovpn.StatusMajorConnection, Minor: ovpn.StatusConnConnected}, true},
		{"connecting", ovpn.Status{Major: ovpn.StatusMajorConnection, Minor: ovpn.StatusConnConnecting}, true},
		{"disconnect-ghost", ovpn.Status{Major: ovpn.StatusMajorConnection, Minor: ovpn.StatusConnDisconnect}, false},
		{"unrelated-major", ovpn.Status{Major: 0}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, tc.want, tc.status.IsActive())
		})
	}
}

func TestService_Disconnect(t *testing.T) {
	ctl := &fakeCtl{}
	svc := app.New(&fakeConfigs{}, &fakeSessions{ctl: ctl})

	require.NoError(t, svc.Disconnect("/s/abc"))
	require.Equal(t, []string{"Disconnect"}, ctl.calls)
}
