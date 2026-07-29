package server

import (
	"sync"
	"sync/atomic"

	"github.com/zoefix/openfrp/internal/tunnel/protocol"
	"github.com/zoefix/openfrp/pkg/netutil"
)

// TunnelLimits is the rate and quota a tunnel is held to.
//
// It lives on the server because the server is the only party that sees all
// of a tunnel's traffic. A limit applied on the client would bound only the
// connections that reached it, and a visitor turned away for exceeding a
// quota would already have consumed a work connection to be told so.
type TunnelLimits struct {
	down  *netutil.Limiter
	up    *netutil.Limiter
	quota int64

	used atomic.Int64
}

// Limits holds one session's limits: the client-wide pair every tunnel draws
// from, and each tunnel's own.
type Limits struct {
	// Client-wide, from the login. Every tunnel's limiter is nested under
	// these, so the wider figure is a ceiling rather than something each
	// tunnel ignores separately.
	clientDown *netutil.Limiter
	clientUp   *netutil.Limiter
	clientCap  int64
	clientUsed atomic.Int64

	mu      sync.RWMutex
	tunnels map[string]*TunnelLimits
}

func NewLimits() *Limits {
	return &Limits{tunnels: map[string]*TunnelLimits{}}
}

// SetClientLimits applies the figures a client declared at login.
func (l *Limits) SetClientLimits(downRate, upRate, quota int64) {
	l.mu.Lock()
	defer l.mu.Unlock()

	l.clientDown = netutil.NewLimiter(downRate)
	l.clientUp = netutil.NewLimiter(upRate)
	l.clientCap = quota

	// Tunnels published before the figures arrived are re-nested, so the
	// order of login and publish does not decide whether a limit applies.
	for _, t := range l.tunnels {
		t.down = t.down.Under(l.clientDown)
		t.up = t.up.Under(l.clientUp)
	}
}

// ClientExhausted reports whether the client has spent its overall cap.
func (l *Limits) ClientExhausted() bool {
	l.mu.RLock()
	cap := l.clientCap
	l.mu.RUnlock()

	return cap > 0 && l.clientUsed.Load() >= cap
}

// ClientUsage reports bytes spent against the overall cap.
func (l *Limits) ClientUsage() (used, quota int64) {
	l.mu.RLock()
	defer l.mu.RUnlock()
	return l.clientUsed.Load(), l.clientCap
}

// Publish records the limits a tunnel was published with.
//
// The rates are per tunnel rather than per connection: every connection of a
// tunnel shares one limiter, so ten visitors at a megabyte a second get a
// megabyte a second between them rather than ten.
func (l *Limits) Publish(spec protocol.ProxySpec) {
	// A tunnel with no limits of its own still needs a record when the client
	// has limits: that is how it inherits them.
	l.mu.RLock()
	inherits := l.clientDown != nil || l.clientUp != nil
	l.mu.RUnlock()

	if spec.DownRate <= 0 && spec.UpRate <= 0 && spec.Quota <= 0 && !inherits {
		l.Remove(spec.Name)
		return
	}

	l.mu.Lock()

	limits := &TunnelLimits{
		down:  netutil.NewLimiter(spec.DownRate).Under(l.clientDown),
		up:    netutil.NewLimiter(spec.UpRate).Under(l.clientUp),
		quota: spec.Quota,
	}

	// A republish keeps what has been spent. Otherwise restarting a client
	// would hand back a fresh quota, and the cap would mean nothing to
	// anyone willing to reconnect.
	if previous, ok := l.tunnels[spec.Name]; ok {
		limits.used.Store(previous.used.Load())
	}
	l.tunnels[spec.Name] = limits
	l.mu.Unlock()
}

func (l *Limits) Remove(name string) {
	l.mu.Lock()
	delete(l.tunnels, name)
	l.mu.Unlock()
}

// For returns a tunnel's limits, or nil when it has none.
func (l *Limits) For(name string) *TunnelLimits {
	l.mu.RLock()
	defer l.mu.RUnlock()
	return l.tunnels[name]
}

// Rates returns the limiters for a relay, in the order Relay takes them:
// visitor to client, then client to visitor.
func (t *TunnelLimits) Rates() (toClient, toVisitor *netutil.Limiter) {
	if t == nil {
		return nil, nil
	}
	return t.up, t.down
}

// Exhausted reports whether the tunnel has spent its quota.
func (t *TunnelLimits) Exhausted() bool {
	if t == nil || t.quota <= 0 {
		return false
	}
	return t.used.Load() >= t.quota
}

// Spend adds to what the tunnel has used.
func (t *TunnelLimits) Spend(bytes int64) {
	if t == nil || t.quota <= 0 || bytes <= 0 {
		return
	}
	t.used.Add(bytes)
}

// Usage reports bytes spent and the cap, for the status view.
func (t *TunnelLimits) Usage() (used, quota int64) {
	if t == nil {
		return 0, 0
	}
	return t.used.Load(), t.quota
}

// sessionLimits adapts the registry to the three operations proxy needs,
// looking each tunnel up by name.
type sessionLimits struct{ limits *Limits }

func (s sessionLimits) Rates(tunnel string) (toClient, toVisitor *netutil.Limiter) {
	return s.limits.For(tunnel).Rates()
}

func (s sessionLimits) Exhausted(tunnel string) bool {
	return s.limits.ClientExhausted() || s.limits.For(tunnel).Exhausted()
}

func (s sessionLimits) Spend(tunnel string, bytes int64) {
	s.limits.For(tunnel).Spend(bytes)
	if bytes > 0 {
		s.limits.clientUsed.Add(bytes)
	}
}
