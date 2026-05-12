package secrets

import "sync"

// Memory is an in-memory Store for tests. Not exported as a separate
// "fakes" package because both production code (e.g. headless dev runs)
// and tests benefit from it.
type Memory struct {
	mu sync.Mutex
	m  map[string]string
}

func NewMemory() *Memory { return &Memory{m: map[string]string{}} }

func (s *Memory) Get(id string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	v, ok := s.m[id]
	if !ok {
		return "", ErrNotFound
	}
	return v, nil
}

func (s *Memory) Set(id, value string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.m[id] = value
	return nil
}

func (s *Memory) Delete(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.m[id]; !ok {
		return ErrNotFound
	}
	delete(s.m, id)
	return nil
}
