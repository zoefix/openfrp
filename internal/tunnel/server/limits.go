package server

import (
	"sync"
	"sync/atomic"

	"github.com/zoefix/openfrp/internal/tunnel/protocol"
	"github.com/zoefix/openfrp/pkg/netutil"
)

type TunnelLimits struct {
	down  *netutil.Limiter
	up    *netutil.Limiter
	quota int64

	used atomic.Int64
}

type Limits struct {
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

func (l *Limits) SetClientLimits(downRate, upRate, quota int64) {
	l.mu.Lock()
	defer l.mu.Unlock()

	l.clientDown = netutil.NewLimiter(downRate)
	l.clientUp = netutil.NewLimiter(upRate)
	l.clientCap = quota

	for _, t := range l.tunnels {
		t.down = t.down.Under(l.clientDown)
		t.up = t.up.Under(l.clientUp)
	}
}

func (l *Limits) ClientExhausted() bool {
	l.mu.RLock()
	cap := l.clientCap
	l.mu.RUnlock()

	return cap > 0 && l.clientUsed.Load() >= cap
}

func (l *Limits) ClientUsage() (used, quota int64) {
	l.mu.RLock()
	defer l.mu.RUnlock()
	return l.clientUsed.Load(), l.clientCap
}

func (l *Limits) Publish(spec protocol.ProxySpec) {
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

func (l *Limits) For(name string) *TunnelLimits {
	l.mu.RLock()
	defer l.mu.RUnlock()
	return l.tunnels[name]
}

func (t *TunnelLimits) Rates() (toClient, toVisitor *netutil.Limiter) {
	if t == nil {
		return nil, nil
	}
	return t.up, t.down
}

func (t *TunnelLimits) Exhausted() bool {
	if t == nil || t.quota <= 0 {
		return false
	}
	return t.used.Load() >= t.quota
}

func (t *TunnelLimits) Spend(bytes int64) {
	if t == nil || t.quota <= 0 || bytes <= 0 {
		return
	}
	t.used.Add(bytes)
}

func (t *TunnelLimits) Usage() (used, quota int64) {
	if t == nil {
		return 0, 0
	}
	return t.used.Load(), t.quota
}

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
