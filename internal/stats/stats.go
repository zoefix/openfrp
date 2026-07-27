// Package stats accounts for tunnel traffic.
//
// Counters live in memory and are read lock-free on the hot path. Persistence
// is deliberately not here: writing a counter to disk per connection would put
// flash wear and a syscall on the data path, which is a poor trade for numbers
// whose whole purpose is to be looked at occasionally.
package stats

import (
	"sort"
	"sync"
	"sync/atomic"
	"time"
)

// Counters is one tunnel's running totals.
//
// Every field is atomic so the relay can update them without a lock. They are
// read as an inconsistent snapshot — bytes and connections may be a few
// operations apart — which is correct for a status display and would be wrong
// for billing.
type Counters struct {
	BytesIn     atomic.Int64
	BytesOut    atomic.Int64
	Connections atomic.Int64
	// Active is incremented on open and decremented on close, so it can go
	// briefly negative under a race during shutdown; readers clamp it.
	Active atomic.Int64
	// Spliced counts relays that reached the kernel fast path, and Buffered
	// those that fell back. The ratio is the health signal for the property
	// the whole data plane is built around.
	Spliced  atomic.Int64
	Buffered atomic.Int64

	lastSeen atomic.Int64
}

// Snapshot is a point-in-time copy.
type Snapshot struct {
	Name        string    `json:"name"`
	BytesIn     int64     `json:"bytes_in"`
	BytesOut    int64     `json:"bytes_out"`
	Connections int64     `json:"connections"`
	Active      int64     `json:"active"`
	Spliced     int64     `json:"spliced"`
	Buffered    int64     `json:"buffered"`
	LastSeen    time.Time `json:"last_seen,omitempty"`
}

// SplicedFraction reports what proportion of relays used the kernel fast path.
//
// This is the number to watch. A deployment where it drops is one where
// something started wrapping work connections, and the throughput advantage
// over frp has quietly gone with it.
func (s Snapshot) SplicedFraction() float64 {
	total := s.Spliced + s.Buffered
	if total == 0 {
		return 0
	}
	return float64(s.Spliced) / float64(total)
}

// Registry holds counters for every tunnel.
type Registry struct {
	mu       sync.RWMutex
	counters map[string]*Counters

	started time.Time
}

// NewRegistry returns an empty registry.
func NewRegistry() *Registry {
	return &Registry{counters: map[string]*Counters{}, started: time.Now()}
}

// For returns the counters for a tunnel, creating them on first use.
func (r *Registry) For(name string) *Counters {
	r.mu.RLock()
	counters, ok := r.counters[name]
	r.mu.RUnlock()
	if ok {
		return counters
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	// Re-check: another goroutine may have created it while we upgraded.
	if counters, ok := r.counters[name]; ok {
		return counters
	}
	counters = &Counters{}
	r.counters[name] = counters
	return counters
}

// RecordTransfer accounts for one completed relay.
func (r *Registry) RecordTransfer(name string, in, out int64, spliced bool) {
	counters := r.For(name)

	counters.BytesIn.Add(in)
	counters.BytesOut.Add(out)
	counters.Connections.Add(1)
	counters.lastSeen.Store(time.Now().Unix())

	if spliced {
		counters.Spliced.Add(1)
	} else {
		counters.Buffered.Add(1)
	}
}

// Open records a connection starting.
func (r *Registry) Open(name string) { r.For(name).Active.Add(1) }

// Close records a connection ending.
func (r *Registry) Close(name string) { r.For(name).Active.Add(-1) }

// Snapshot returns one tunnel's totals.
func (r *Registry) Snapshot(name string) Snapshot {
	return snapshotOf(name, r.For(name))
}

// All returns every tunnel's totals, ordered by name.
func (r *Registry) All() []Snapshot {
	r.mu.RLock()
	out := make([]Snapshot, 0, len(r.counters))
	for name, counters := range r.counters {
		out = append(out, snapshotOf(name, counters))
	}
	r.mu.RUnlock()

	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// Total sums every tunnel.
func (r *Registry) Total() Snapshot {
	total := Snapshot{Name: "total"}
	for _, item := range r.All() {
		total.BytesIn += item.BytesIn
		total.BytesOut += item.BytesOut
		total.Connections += item.Connections
		total.Active += item.Active
		total.Spliced += item.Spliced
		total.Buffered += item.Buffered
	}
	return total
}

// Forget drops a tunnel's counters, for one that has been withdrawn.
func (r *Registry) Forget(name string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.counters, name)
}

// Uptime reports how long the registry has been collecting.
func (r *Registry) Uptime() time.Duration { return time.Since(r.started) }

func snapshotOf(name string, counters *Counters) Snapshot {
	snapshot := Snapshot{
		Name:        name,
		BytesIn:     counters.BytesIn.Load(),
		BytesOut:    counters.BytesOut.Load(),
		Connections: counters.Connections.Load(),
		Active:      counters.Active.Load(),
		Spliced:     counters.Spliced.Load(),
		Buffered:    counters.Buffered.Load(),
	}

	// Active is incremented and decremented from different goroutines, so a
	// shutdown race can leave it momentarily below zero. Reporting a negative
	// connection count would be worse than reporting zero.
	if snapshot.Active < 0 {
		snapshot.Active = 0
	}
	if seen := counters.lastSeen.Load(); seen > 0 {
		snapshot.LastSeen = time.Unix(seen, 0).UTC()
	}
	return snapshot
}
