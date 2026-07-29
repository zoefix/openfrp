package stats

import (
	"sort"
	"sync"
	"sync/atomic"
	"time"
)

type Counters struct {
	BytesIn     atomic.Int64
	BytesOut    atomic.Int64
	Connections atomic.Int64

	Active atomic.Int64

	Spliced  atomic.Int64
	Buffered atomic.Int64

	lastSeen atomic.Int64
}

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

func (s Snapshot) SplicedFraction() float64 {
	total := s.Spliced + s.Buffered
	if total == 0 {
		return 0
	}
	return float64(s.Spliced) / float64(total)
}

type Registry struct {
	mu       sync.RWMutex
	counters map[string]*Counters

	started time.Time
}

func NewRegistry() *Registry {
	return &Registry{counters: map[string]*Counters{}, started: time.Now()}
}

func (r *Registry) For(name string) *Counters {
	r.mu.RLock()
	counters, ok := r.counters[name]
	r.mu.RUnlock()
	if ok {
		return counters
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	if counters, ok := r.counters[name]; ok {
		return counters
	}
	counters = &Counters{}
	r.counters[name] = counters
	return counters
}

func (r *Registry) RecordTransfer(name string, in, out int64, spliced bool) {
	r.RecordProgress(name, in, out)
	r.RecordClose(name, spliced)
}

func (r *Registry) RecordProgress(name string, in, out int64) {
	if in == 0 && out == 0 {
		return
	}
	counters := r.For(name)

	counters.BytesIn.Add(in)
	counters.BytesOut.Add(out)
	counters.lastSeen.Store(time.Now().Unix())
}

func (r *Registry) RecordClose(name string, spliced bool) {
	counters := r.For(name)

	counters.Connections.Add(1)
	counters.lastSeen.Store(time.Now().Unix())

	if spliced {
		counters.Spliced.Add(1)
	} else {
		counters.Buffered.Add(1)
	}
}

func (r *Registry) Open(name string) { r.For(name).Active.Add(1) }

func (r *Registry) Close(name string) { r.For(name).Active.Add(-1) }

func (r *Registry) Snapshot(name string) Snapshot {
	return snapshotOf(name, r.For(name))
}

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

func (r *Registry) Forget(name string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.counters, name)
}

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

	if snapshot.Active < 0 {
		snapshot.Active = 0
	}
	if seen := counters.lastSeen.Load(); seen > 0 {
		snapshot.LastSeen = time.Unix(seen, 0).UTC()
	}
	return snapshot
}
