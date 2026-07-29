package server

import (
	"fmt"
	"sort"
	"sync"
)

type Registry struct {
	mu       sync.RWMutex
	sessions map[string]*Session
}

func NewRegistry() *Registry {
	return &Registry{sessions: make(map[string]*Session)}
}

func (r *Registry) Add(s *Session) (replaced *Session) {
	r.mu.Lock()
	defer r.mu.Unlock()

	replaced = r.sessions[s.runID]
	r.sessions[s.runID] = s
	return replaced
}

func (r *Registry) Get(runID string) (*Session, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	s, ok := r.sessions[runID]
	if !ok {
		return nil, fmt.Errorf("no session for run id %q", runID)
	}
	return s, nil
}

func (r *Registry) Remove(s *Session) {
	r.mu.Lock()
	defer r.mu.Unlock()

	if current, ok := r.sessions[s.runID]; ok && current == s {
		delete(r.sessions, s.runID)
	}
}

func (r *Registry) Len() int {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return len(r.sessions)
}

func (r *Registry) Sessions() []*Session {
	r.mu.RLock()
	out := make([]*Session, 0, len(r.sessions))
	for _, s := range r.sessions {
		out = append(out, s)
	}
	r.mu.RUnlock()

	sort.Slice(out, func(i, j int) bool { return out[i].runID < out[j].runID })
	return out
}

func (r *Registry) CloseAll() {
	for _, s := range r.Sessions() {
		s.Close()
	}
}
