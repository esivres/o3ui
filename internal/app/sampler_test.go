package app

// Internal test: we exercise the sampler directly to drive it tick-by-tick
// without spinning a real ticker. Lives in `package app` so tick() is
// accessible without exporting it.

import (
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/esivres/openvpn3ui/internal/ovpn"
)

// fakeBackend bundles SessionBackend + a controller scripted with a series
// of statistics snapshots. Each call to Statistics pops the next entry.
type fakeBackend struct {
	sessions []ovpn.Session
	scripts  map[string][]map[string]int64 // path → series
}

func (f *fakeBackend) List() ([]ovpn.Session, error) { return f.sessions, nil }
func (f *fakeBackend) NewTunnel(string) (string, error) {
	return "", errors.New("unused")
}
func (f *fakeBackend) Control(path string) SessionControl {
	return &fakeBackendCtl{path: path, parent: f}
}

type fakeBackendCtl struct {
	path   string
	parent *fakeBackend
}

func (c *fakeBackendCtl) Ready() error      { return nil }
func (c *fakeBackendCtl) Connect() error    { return nil }
func (c *fakeBackendCtl) Disconnect() error { return nil }
func (c *fakeBackendCtl) PendingInputs() ([]ovpn.InputPrompt, error) {
	return nil, nil
}
func (c *fakeBackendCtl) ProvideInput(ovpn.InputPrompt, string) error { return nil }
func (c *fakeBackendCtl) LogVerbosity() (uint32, error)               { return 0, nil }
func (c *fakeBackendCtl) SetLogVerbosity(uint32) error                { return nil }
func (c *fakeBackendCtl) Statistics() (map[string]int64, error) {
	series := c.parent.scripts[c.path]
	if len(series) == 0 {
		return map[string]int64{}, nil
	}
	head := series[0]
	c.parent.scripts[c.path] = series[1:]
	return head, nil
}

func TestSampler_BuildsRingBufferAndDeltas(t *testing.T) {
	be := &fakeBackend{
		sessions: []ovpn.Session{{
			Path:   "/s/1",
			Status: ovpn.Status{Major: ovpn.StatusMajorConnection, Minor: ovpn.StatusConnConnected},
		}},
		scripts: map[string][]map[string]int64{
			"/s/1": {
				{"BYTES_IN": 1000, "BYTES_OUT": 200},
				{"BYTES_IN": 3000, "BYTES_OUT": 700},
				{"BYTES_IN": 8000, "BYTES_OUT": 1700},
			},
		},
	}
	svc := New(&fakeConfigs{}, be)
	sm := NewSampler(svc, time.Second, 60)
	svc.AttachSampler(sm)

	// Drive three synthetic ticks 1s apart.
	t0 := time.Unix(1_000_000, 0)
	sm.tick(t0)
	sm.tick(t0.Add(time.Second))
	sm.tick(t0.Add(2 * time.Second))

	hist := sm.History("/s/1")
	require.Len(t, hist, 3)

	require.Equal(t, int64(1000), hist[0].BytesIn)
	require.Equal(t, int64(0), hist[0].DeltaIn, "first sample has no previous, delta must be 0")

	require.Equal(t, int64(3000), hist[1].BytesIn)
	require.Equal(t, int64(2000), hist[1].DeltaIn, "delta = (3000-1000)/1s = 2000 B/s")
	require.Equal(t, int64(500), hist[1].DeltaOut)

	require.Equal(t, int64(8000), hist[2].BytesIn)
	require.Equal(t, int64(5000), hist[2].DeltaIn)
	require.Equal(t, int64(1000), hist[2].DeltaOut)
}

func TestSampler_RingBufferEvictsOldest(t *testing.T) {
	scripts := []map[string]int64{}
	for i := int64(0); i < 5; i++ {
		scripts = append(scripts, map[string]int64{"BYTES_IN": i * 100})
	}
	be := &fakeBackend{
		sessions: []ovpn.Session{{
			Path:   "/s/1",
			Status: ovpn.Status{Major: ovpn.StatusMajorConnection, Minor: ovpn.StatusConnConnected},
		}},
		scripts: map[string][]map[string]int64{"/s/1": scripts},
	}
	svc := New(&fakeConfigs{}, be)
	sm := NewSampler(svc, time.Second, 3) // cap = 3
	svc.AttachSampler(sm)

	t0 := time.Unix(1_000_000, 0)
	for i := 0; i < 5; i++ {
		sm.tick(t0.Add(time.Duration(i) * time.Second))
	}

	hist := sm.History("/s/1")
	require.Len(t, hist, 3, "ring buffer cannot exceed capacity")
	// The two oldest must be gone — surviving samples are the last 3
	// counter values (200, 300, 400).
	require.Equal(t, int64(200), hist[0].BytesIn)
	require.Equal(t, int64(300), hist[1].BytesIn)
	require.Equal(t, int64(400), hist[2].BytesIn)
}

func TestSampler_EvictsDisappearedSessions(t *testing.T) {
	be := &fakeBackend{
		sessions: []ovpn.Session{{
			Path:   "/s/1",
			Status: ovpn.Status{Major: ovpn.StatusMajorConnection, Minor: ovpn.StatusConnConnected},
		}},
		scripts: map[string][]map[string]int64{
			"/s/1": {{"BYTES_IN": 100}, {"BYTES_IN": 200}},
		},
	}
	svc := New(&fakeConfigs{}, be)
	sm := NewSampler(svc, time.Second, 60)
	svc.AttachSampler(sm)

	sm.tick(time.Unix(1_000_000, 0))
	require.Len(t, sm.History("/s/1"), 1)

	// Session goes away (Disconnect); next tick should evict its history.
	be.sessions = nil
	sm.tick(time.Unix(1_000_001, 0))
	require.Empty(t, sm.History("/s/1"), "history of disappeared sessions must be released")
}

func TestSampler_NegativeDeltaClampedToZero(t *testing.T) {
	// Backend restart can reset counters; reading a smaller value than
	// the previous one must not produce a negative throughput sample.
	be := &fakeBackend{
		sessions: []ovpn.Session{{
			Path:   "/s/1",
			Status: ovpn.Status{Major: ovpn.StatusMajorConnection, Minor: ovpn.StatusConnConnected},
		}},
		scripts: map[string][]map[string]int64{
			"/s/1": {{"BYTES_IN": 5000}, {"BYTES_IN": 10}},
		},
	}
	svc := New(&fakeConfigs{}, be)
	sm := NewSampler(svc, time.Second, 60)
	svc.AttachSampler(sm)

	sm.tick(time.Unix(1_000_000, 0))
	sm.tick(time.Unix(1_000_001, 0))

	hist := sm.History("/s/1")
	require.Equal(t, int64(0), hist[1].DeltaIn,
		"counter regression must clamp delta to zero, not produce a negative")
}

func TestService_ThroughputHistory_ReturnsNilWithoutSampler(t *testing.T) {
	svc := New(&fakeConfigs{}, &fakeSessions{ctl: &fakeCtl{}})
	require.Nil(t, svc.ThroughputHistory("/s/1"))
}

// minimal fakes mirrored from service_test.go for the in-package test;
// service_test.go lives in `package app_test` so we can't reuse them.
type fakeConfigs struct{}

func (f *fakeConfigs) List() ([]ovpn.Config, error) { return nil, nil }
func (f *fakeConfigs) Import(string, string, bool) (string, error) {
	return "", errors.New("unused")
}
func (f *fakeConfigs) Remove(string) error          { return errors.New("unused") }
func (f *fakeConfigs) Fetch(string) (string, error) { return "", errors.New("unused") }

type fakeSessions struct{ ctl *fakeCtl }

func (f *fakeSessions) List() ([]ovpn.Session, error)    { return nil, nil }
func (f *fakeSessions) NewTunnel(string) (string, error) { return "", errors.New("unused") }
func (f *fakeSessions) Control(string) SessionControl    { return f.ctl }

type fakeCtl struct{}

func (c *fakeCtl) Ready() error      { return nil }
func (c *fakeCtl) Connect() error    { return nil }
func (c *fakeCtl) Disconnect() error { return nil }
func (c *fakeCtl) PendingInputs() ([]ovpn.InputPrompt, error) {
	return nil, nil
}
func (c *fakeCtl) ProvideInput(ovpn.InputPrompt, string) error { return nil }
func (c *fakeCtl) Statistics() (map[string]int64, error)       { return nil, nil }
func (c *fakeCtl) LogVerbosity() (uint32, error)               { return 0, nil }
func (c *fakeCtl) SetLogVerbosity(uint32) error                { return nil }
