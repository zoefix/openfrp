package vhost

import (
	"fmt"
	"sort"
	"sync"
	"sync/atomic"
)

// Scheme distinguishes the two vhost listeners.
type Scheme string

const (
	SchemeHTTP  Scheme = "http"
	SchemeHTTPS Scheme = "https"
)

// Route is what a matched host resolves to.
type Route struct {
	// Pattern is the normalised pattern that produced this route.
	Pattern string
	// RunID and ProxyName identify the tunnel that should receive the traffic.
	RunID     string
	ProxyName string
	// TLSMode is none, passthrough or terminate. Only meaningful for https.
	TLSMode string
}

// table is an immutable routing snapshot.
type table struct {
	root     *node
	catchAll *Route
	count    int
}

func newTable() *table {
	return &table{root: newNode()}
}

// Router resolves hostnames to tunnels.
//
// Reads are lock-free: the whole table is rebuilt on every change and swapped
// atomically. Route changes are rare — a tunnel being published or withdrawn —
// while lookups happen on every inbound connection, so paying a full rebuild
// to keep the hot path free of contention is the right trade. It also removes
// any possibility of a lookup observing a half-applied change.
//
// This is also what lets us reconfigure without a restart, which frps cannot
// do at all: its API is read-only and a port or certificate change means
// bouncing the process and every client with it.
type Router struct {
	current atomic.Pointer[table]

	// mu serialises writers only. Readers never touch it.
	mu sync.Mutex
	// routes is the writer-side source of truth, keyed by pattern.
	routes map[string]*Route
}

// NewRouter returns an empty router.
func NewRouter() *Router {
	r := &Router{routes: make(map[string]*Route)}
	r.current.Store(newTable())
	return r
}

// Add registers every pattern for a tunnel. It is all-or-nothing: if any
// pattern collides, nothing is added, so a rejected publish cannot leave half
// its domains claimed.
func (r *Router) Add(patterns []string, route Route) error {
	if len(patterns) == 0 {
		return fmt.Errorf("vhost: tunnel %q supplied no domains", route.ProxyName)
	}

	parsed := make([]Pattern, 0, len(patterns))
	for _, raw := range patterns {
		p, err := ParsePattern(raw)
		if err != nil {
			return err
		}
		parsed = append(parsed, p)
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	// Check every pattern before mutating anything.
	for _, p := range parsed {
		if existing, taken := r.routes[p.String()]; taken {
			return fmt.Errorf("vhost: %q is already routed to tunnel %q on client %q",
				p, existing.ProxyName, existing.RunID)
		}
	}

	for _, p := range parsed {
		claimed := route
		claimed.Pattern = p.String()
		r.routes[p.String()] = &claimed
	}

	return r.rebuildLocked()
}

// Remove withdraws every route belonging to one tunnel.
func (r *Router) Remove(runID, proxyName string) {
	r.mu.Lock()
	defer r.mu.Unlock()

	for pattern, route := range r.routes {
		if route.RunID == runID && route.ProxyName == proxyName {
			delete(r.routes, pattern)
		}
	}
	r.rebuildLocked()
}

// RemoveClient withdraws every route belonging to one client, which is what a
// disconnect needs.
func (r *Router) RemoveClient(runID string) {
	r.mu.Lock()
	defer r.mu.Unlock()

	for pattern, route := range r.routes {
		if route.RunID == runID {
			delete(r.routes, pattern)
		}
	}
	r.rebuildLocked()
}

// rebuildLocked recomputes the immutable table and publishes it.
func (r *Router) rebuildLocked() error {
	next := newTable()

	// Sort so a rebuild is deterministic and any collision error names the
	// same pattern every time.
	patterns := make([]string, 0, len(r.routes))
	for pattern := range r.routes {
		patterns = append(patterns, pattern)
	}
	sort.Strings(patterns)

	for _, pattern := range patterns {
		route := r.routes[pattern]

		parsed, err := ParsePattern(pattern)
		if err != nil {
			// Unreachable: patterns were validated on the way in.
			return fmt.Errorf("vhost: rebuilding table: %w", err)
		}
		if parsed.IsCatchAll() {
			next.catchAll = route
			next.count++
			continue
		}
		if err := next.root.insert(parsed.Labels(), route); err != nil {
			return err
		}
		next.count++
	}

	r.current.Store(next)
	return nil
}

// Lookup resolves a host to a route. The host may carry a port and any casing.
func (r *Router) Lookup(host string) (*Route, bool) {
	labels, err := splitHostLabels(host)
	if err != nil {
		// A malformed host can still land on the catch-all, which is what an
		// operator expects from a fallback route.
		t := r.current.Load()
		if t.catchAll != nil {
			return t.catchAll, true
		}
		return nil, false
	}

	t := r.current.Load()
	if route := t.root.lookup(labels); route != nil {
		return route, true
	}
	if t.catchAll != nil {
		return t.catchAll, true
	}
	return nil, false
}

// Len reports how many patterns are routed.
func (r *Router) Len() int { return r.current.Load().count }

// Routes returns a snapshot of every route, ordered by pattern.
func (r *Router) Routes() []Route {
	t := r.current.Load()

	out := make([]Route, 0, t.count)
	t.root.walk(func(route *Route) { out = append(out, *route) })
	if t.catchAll != nil {
		out = append(out, *t.catchAll)
	}

	sort.Slice(out, func(i, j int) bool { return out[i].Pattern < out[j].Pattern })
	return out
}
