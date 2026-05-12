package app

import (
	"context"
	"sync"
	"time"
)

// Sample is one entry in a session's throughput history. DeltaIn/DeltaOut
// are bytes-per-second relative to the previous sample (zero on the first
// observation). Absolute totals are kept too because the UI shows both.
type Sample struct {
	At       time.Time
	BytesIn  int64
	BytesOut int64
	DeltaIn  int64 // bytes per second since the previous sample
	DeltaOut int64
}

// Sampler polls openvpn3 sessions on a tick and keeps a per-session ring
// buffer of recent throughput samples. The TUI's sparkline pulls from
// here without making D-Bus calls of its own.
//
// One sampler is enough for the whole app — it iterates active sessions
// each tick. Sessions that disappear (Disconnect) keep their last buffer
// for a short while so the UI can render a final state, then are evicted
// on the next tick.
type Sampler struct {
	svc      *Service
	interval time.Duration
	capacity int

	mu      sync.Mutex
	history map[string][]Sample // session path → ring (oldest at index 0)
}

// NewSampler builds a sampler bound to a Service. capacity is how many
// samples to retain (the design uses 60 = one minute at 1Hz).
func NewSampler(svc *Service, interval time.Duration, capacity int) *Sampler {
	if interval <= 0 {
		interval = time.Second
	}
	if capacity <= 0 {
		capacity = 60
	}
	return &Sampler{
		svc:      svc,
		interval: interval,
		capacity: capacity,
		history:  map[string][]Sample{},
	}
}

// Run drives the sampler until ctx is cancelled. Blocks; typically launched
// in its own goroutine at startup. Safe to run zero or one of these per
// Service.
func (s *Sampler) Run(ctx context.Context) {
	t := time.NewTicker(s.interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-t.C:
			s.tick(now)
		}
	}
}

// History returns a copy of the recent samples for one session, oldest
// first. Empty slice when the session has never been sampled or has been
// evicted. Cheap — protected by a short critical section.
func (s *Sampler) History(sessionPath string) []Sample {
	s.mu.Lock()
	defer s.mu.Unlock()
	src := s.history[sessionPath]
	if len(src) == 0 {
		return nil
	}
	out := make([]Sample, len(src))
	copy(out, src)
	return out
}

// tick is one polling pass. Exposed for tests via the Tick wrapper below.
func (s *Sampler) tick(now time.Time) {
	sessions, err := s.svc.ActiveSessions()
	if err != nil {
		return
	}
	live := map[string]struct{}{}
	// Index-based — ovpn.Session is ~128B per element.
	for i := range sessions {
		live[sessions[i].Path] = struct{}{}
		stats, err := s.svc.sessions.Control(sessions[i].Path).Statistics()
		if err != nil {
			continue
		}
		s.appendSample(sessions[i].Path, now, stats)
	}
	// Evict any sessions we no longer see — keeping their history would
	// leak memory across many connect/disconnect cycles.
	s.mu.Lock()
	for k := range s.history {
		if _, ok := live[k]; !ok {
			delete(s.history, k)
		}
	}
	s.mu.Unlock()
}

func (s *Sampler) appendSample(path string, now time.Time, stats map[string]int64) {
	in := stats["BYTES_IN"]
	out := stats["BYTES_OUT"]

	s.mu.Lock()
	defer s.mu.Unlock()

	prev := s.history[path]
	sample := Sample{At: now, BytesIn: in, BytesOut: out}
	if len(prev) > 0 {
		last := prev[len(prev)-1]
		dt := now.Sub(last.At).Seconds()
		if dt > 0 {
			// Counters can wrap or reset (backend restart) — clamp negatives to 0.
			if d := in - last.BytesIn; d > 0 {
				sample.DeltaIn = int64(float64(d) / dt)
			}
			if d := out - last.BytesOut; d > 0 {
				sample.DeltaOut = int64(float64(d) / dt)
			}
		}
	}
	if len(prev) >= s.capacity {
		// Shift oldest out.
		prev = append(prev[:0], prev[1:]...)
	}
	s.history[path] = append(prev, sample)
}

// ThroughputHistory is the public accessor on Service. The Sampler stores
// data; Service is the single integration point for the UI.
func (s *Service) ThroughputHistory(sessionPath string) []Sample {
	if s.sampler == nil {
		return nil
	}
	return s.sampler.History(sessionPath)
}

// AttachSampler wires a sampler into the service so ThroughputHistory has
// a backing store. Optional — without it, ThroughputHistory returns nil.
func (s *Service) AttachSampler(sm *Sampler) { s.sampler = sm }
