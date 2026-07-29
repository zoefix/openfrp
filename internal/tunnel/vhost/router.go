package vhost

import (
	"fmt"
	"sort"
	"sync"
	"sync/atomic"
)

type Scheme string

const (
	SchemeHTTP  Scheme = "http"
	SchemeHTTPS Scheme = "https"
)

type Route struct {
	Pattern string

	RunID     string
	ProxyName string

	TLSMode string
}

type table struct {
	root     *node
	catchAll *Route
	count    int
}

func newTable() *table {
	return &table{root: newNode()}
}

type Router struct {
	current atomic.Pointer[table]

	mu sync.Mutex

	routes map[string]*Route
}

func NewRouter() *Router {
	r := &Router{routes: make(map[string]*Route)}
	r.current.Store(newTable())
	return r
}

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

func (r *Router) rebuildLocked() error {
	next := newTable()

	patterns := make([]string, 0, len(r.routes))
	for pattern := range r.routes {
		patterns = append(patterns, pattern)
	}
	sort.Strings(patterns)

	for _, pattern := range patterns {
		route := r.routes[pattern]

		parsed, err := ParsePattern(pattern)
		if err != nil {

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

func (r *Router) Lookup(host string) (*Route, bool) {
	labels, err := splitHostLabels(host)
	if err != nil {

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

func (r *Router) Len() int { return r.current.Load().count }

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
