package server

import (
	"fmt"
	"sort"
	"sync"
)

// Registry tracks connected clients by run ID.
//
// Work connections arrive on their own TCP connections carrying only a run ID,
// so this lookup is what reunites them with the control session that owns them.
type Registry struct {
	mu       sync.RWMutex
	sessions map[string]*Session
}

// NewRegistry returns an empty registry.
func NewRegistry() *Registry {
	return &Registry{sessions: make(map[string]*Session)}
}

// Add registers a session, replacing any previous session with the same run
// ID. A client that reconnects before the server noticed the old connection
// had died is the common case, and displacing the stale session is what lets
// it recover without waiting for a heartbeat timeout.
func (r *Registry) Add(s *Session) (replaced *Session) {
	r.mu.Lock()
	defer r.mu.Unlock()

	replaced = r.sessions[s.runID]
	r.sessions[s.runID] = s
	return replaced
}

// Get looks up a session by run ID.
func (r *Registry) Get(runID string) (*Session, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	s, ok := r.sessions[runID]
	if !ok {
		return nil, fmt.Errorf("no session for run id %q", runID)
	}
	return s, nil
}

// Remove deregisters a session, but only if it is still the one registered
// under that run ID. This avoids a reconnecting client's session being evicted
// by the teardown of the stale one it displaced.
func (r *Registry) Remove(s *Session) {
	r.mu.Lock()
	defer r.mu.Unlock()

	if current, ok := r.sessions[s.runID]; ok && current == s {
		delete(r.sessions, s.runID)
	}
}

// Len reports how many sessions are connected.
func (r *Registry) Len() int {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return len(r.sessions)
}

// Sessions returns a snapshot of the connected sessions, ordered by run ID so
// status output is stable.
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

// CloseAll tears down every session.
func (r *Registry) CloseAll() {
	for _, s := range r.Sessions() {
		s.Close()
	}
}
