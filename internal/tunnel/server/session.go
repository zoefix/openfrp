package server

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"sync"
	"sync/atomic"
	"time"

	"github.com/zoefix/openfrp/internal/tunnel/protocol"
	"github.com/zoefix/openfrp/internal/tunnel/server/proxy"
	"github.com/zoefix/openfrp/internal/tunnel/transport"
)

const workConnTimeout = 10 * time.Second

const overflowOpenTimeout = 3 * time.Second

type Session struct {
	runID string
	name  string

	ctrlConn net.Conn
	codec    *protocol.Codec
	logger   *slog.Logger

	workConns chan net.Conn

	poolTarget int

	poolInFlight atomic.Int64
	poolArrivals atomic.Int64

	refillTarget atomic.Int64

	poolMisses atomic.Int64
	poolMax    int

	overflowMu   sync.RWMutex
	overflow     []transport.StreamSource
	overflowNext atomic.Uint64

	reqPending atomic.Int64
	reqKick    chan struct{}

	proxiesMu sync.RWMutex
	proxies   map[string]*runningProxy

	closeOnce sync.Once
	done      chan struct{}

	bindAddr    string
	acceptLoops int
	reusePort   bool

	routes         proxy.RouteRegistrar
	challenges     *ChallengeStore
	recorder       proxy.Recorder
	vhostHTTPPort  int
	vhostHTTPSPort int

	limits *Limits
}

type runningProxy struct {
	proxy  proxy.Proxy
	cancel context.CancelFunc
}

type SessionOptions struct {
	RunID      string
	Name       string
	Conn       net.Conn
	Codec      *protocol.Codec
	Logger     *slog.Logger
	PoolTarget int
	MaxPool    int

	BindAddr    string
	AcceptLoops int
	ReusePort   bool

	Routes proxy.RouteRegistrar

	Challenges     *ChallengeStore
	Recorder       proxy.Recorder
	VhostHTTPPort  int
	VhostHTTPSPort int
}

func newSession(opts SessionOptions) *Session {
	capacity := opts.MaxPool
	if capacity < 1 {
		capacity = 1
	}

	s := &Session{
		runID:       opts.RunID,
		name:        opts.Name,
		ctrlConn:    opts.Conn,
		codec:       opts.Codec,
		logger:      opts.Logger,
		workConns:   make(chan net.Conn, capacity),
		poolTarget:  opts.PoolTarget,
		reqKick:     make(chan struct{}, 1),
		proxies:     make(map[string]*runningProxy),
		done:        make(chan struct{}),
		bindAddr:    opts.BindAddr,
		acceptLoops: opts.AcceptLoops,
		reusePort:   opts.ReusePort,

		routes:         opts.Routes,
		challenges:     opts.Challenges,
		recorder:       opts.Recorder,
		vhostHTTPPort:  opts.VhostHTTPPort,
		vhostHTTPSPort: opts.VhostHTTPSPort,
	}

	s.limits = NewLimits()
	s.poolMax = capacity
	s.refillTarget.Store(int64(opts.PoolTarget))

	go s.workConnRequester()
	go s.tendPool()

	return s
}

func (s *Session) RunID() string { return s.runID }

func (s *Session) Name() string { return s.name }

func (s *Session) AddWorkConn(conn net.Conn) {

	if left := s.poolInFlight.Add(-1); left < 0 {

		s.poolInFlight.Store(0)
	}
	s.poolArrivals.Add(1)

	select {
	case <-s.done:
		conn.Close()
	case s.workConns <- conn:

		if s.logger.Enabled(context.Background(), slog.LevelDebug) {
			s.logger.Debug("work connection pooled", "pooled", len(s.workConns))
		}
	default:
		s.logger.Warn("work connection pool full, dropping connection",
			"capacity", cap(s.workConns))
		conn.Close()
	}
}

func (s *Session) GetWorkConn(ctx context.Context, proxyName, sourceAddr string) (net.Conn, error) {

	defer s.replenishPool()

	var stale int

	for {
		conn, ok := s.takePooled()
		if !ok {
			break
		}

		if err := s.startWorkConn(conn, proxyName, sourceAddr); err != nil {
			conn.Close()
			stale++
			continue
		}

		if stale > 0 {
			s.logger.Info("discarded work connections that had gone stale",
				"discarded", stale, "proxy", proxyName)
		}
		return conn, nil
	}

	s.poolMisses.Add(1)

	if conn, ok := s.overflowConn(ctx, proxyName, sourceAddr); ok {
		if stale > 0 {
			s.logger.Info("discarded work connections that had gone stale",
				"discarded", stale, "proxy", proxyName)
		}
		return conn, nil
	}

	conn, err := s.waitWorkConn(ctx)
	if err != nil {
		if stale > 0 {
			return nil, fmt.Errorf(
				"session %s: %w (after discarding %d stale connection(s))",
				s.runID, err, stale)
		}
		return nil, fmt.Errorf("session %s: %w", s.runID, err)
	}

	if err := s.startWorkConn(conn, proxyName, sourceAddr); err != nil {
		conn.Close()
		return nil, fmt.Errorf("session %s: start work conn: %w", s.runID, err)
	}

	if stale > 0 {
		s.logger.Info("discarded work connections that had gone stale",
			"discarded", stale, "proxy", proxyName)
	}
	return conn, nil
}

func (s *Session) startWorkConn(conn net.Conn, proxyName, sourceAddr string) error {
	return protocol.WriteMessage(conn, &protocol.StartWorkConn{
		ProxyName:  proxyName,
		SourceAddr: sourceAddr,
	})
}

const maxOverflowCarriers = 8

func (s *Session) SetOverflow(source transport.StreamSource) {
	s.overflowMu.Lock()
	if len(s.overflow) >= maxOverflowCarriers {
		s.overflowMu.Unlock()
		source.Close()
		s.logger.Warn("client offered more overflow carriers than allowed",
			"max", maxOverflowCarriers)
		return
	}
	s.overflow = append(s.overflow, source)
	count := len(s.overflow)
	s.overflowMu.Unlock()

	s.logger.Info("client offered a multiplexed overflow carrier", "carriers", count)
}

func (s *Session) overflowConn(ctx context.Context, proxyName, sourceAddr string) (net.Conn, bool) {
	s.overflowMu.RLock()
	carriers := s.overflow
	s.overflowMu.RUnlock()

	if len(carriers) == 0 {
		return nil, false
	}

	ctx, cancel := context.WithTimeout(ctx, overflowOpenTimeout)
	defer cancel()

	start := int(s.overflowNext.Add(1) - 1)

	for attempt := range carriers {
		source := carriers[(start+attempt)%len(carriers)]

		stream, err := source.Open(ctx)
		if err != nil {

			if ctx.Err() != nil {
				s.logger.Warn("overflow carrier is saturated; falling back to a dial",
					"proxy", proxyName, "carriers", len(carriers))
				return nil, false
			}
			s.logger.Warn("overflow carrier failed, trying the next",
				"proxy", proxyName, "error", err)
			s.dropOverflow(source)
			continue
		}

		if err := s.startWorkConn(stream, proxyName, sourceAddr); err != nil {
			stream.Close()
			s.logger.Warn("overflow stream failed before it carried anything",
				"proxy", proxyName, "error", err)
			s.dropOverflow(source)
			continue
		}
		return stream, true
	}
	return nil, false
}

func (s *Session) dropOverflow(failed transport.StreamSource) {
	s.overflowMu.Lock()
	kept := s.overflow[:0]
	for _, source := range s.overflow {
		if source != failed {
			kept = append(kept, source)
		}
	}
	s.overflow = kept
	s.overflowMu.Unlock()

	failed.Close()
}

func (s *Session) HasOverflow() bool {
	s.overflowMu.RLock()
	defer s.overflowMu.RUnlock()
	return len(s.overflow) > 0
}

func (s *Session) DrainPool() int {
	var drained int
	for {
		conn, ok := s.takePooled()
		if !ok {
			return drained
		}
		conn.Close()
		drained++
	}
}

func (s *Session) takePooled() (net.Conn, bool) {
	select {
	case conn := <-s.workConns:
		return conn, true
	default:
		return nil, false
	}
}

func (s *Session) waitWorkConn(ctx context.Context) (net.Conn, error) {
	select {
	case <-s.done:
		return nil, errors.New("session closed")
	default:
	}

	s.poolInFlight.Add(1)
	s.requestWorkConns(1)

	timer := time.NewTimer(workConnTimeout)
	defer timer.Stop()

	select {
	case conn := <-s.workConns:
		return conn, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-s.done:
		return nil, errors.New("session closed")
	case <-timer.C:
		return nil, fmt.Errorf("timed out after %s waiting for a work connection", workConnTimeout)
	}
}

func (s *Session) replenishPool() {
	if s.HasOverflow() {
		return
	}
	s.topUpPool()
}

func (s *Session) topUpPool() {
	inFlight := s.poolInFlight.Load()

	deficit := s.refillTarget.Load() - int64(len(s.workConns)) - inFlight
	if deficit <= 0 {
		return
	}

	if room := int64(cap(s.workConns)) - int64(len(s.workConns)) - inFlight; deficit > room {
		deficit = room
	}
	if deficit <= 0 {
		return
	}

	s.poolInFlight.Add(deficit)
	s.requestWorkConns(int(deficit))
}

const poolSweepInterval = 5 * time.Second

func (s *Session) adjustRefillTarget(stalled bool) {
	target := s.refillTarget.Load()
	misses := s.poolMisses.Swap(0)

	switch {
	case stalled:

		lowered := max(target/2, int64(s.poolTarget))
		if lowered != target {
			s.refillTarget.Store(lowered)
			s.logger.Info("client cannot sustain the dial rate, easing off",
				"refill_target", lowered, "was", target)
		}

	case misses > 0 && target < int64(s.poolMax):

		s.refillTarget.Store(target + 1)
	}
}

const poolRefillInterval = 500 * time.Millisecond

func (s *Session) tendPool() {
	sweep := time.NewTicker(poolSweepInterval)
	defer sweep.Stop()
	refill := time.NewTicker(poolRefillInterval)
	defer refill.Stop()

	lastArrivals := s.poolArrivals.Load()

	for {
		select {
		case <-s.done:
			return

		case <-refill.C:

			if !s.HasOverflow() {
				continue
			}

			arrivals := s.poolArrivals.Load()
			stalled := arrivals == lastArrivals && s.poolInFlight.Load() > 0
			if stalled {

				s.poolInFlight.Store(0)
			}
			lastArrivals = arrivals

			s.adjustRefillTarget(stalled)
			s.topUpPool()

		case <-sweep.C:

			if s.HasOverflow() {
				continue
			}
			arrivals := s.poolArrivals.Load()
			if arrivals == lastArrivals && s.poolInFlight.Load() > 0 {
				s.poolInFlight.Store(0)
			}
			lastArrivals = arrivals
		}
	}
}

func (s *Session) requestWorkConns(n int) {
	if n <= 0 {
		return
	}
	s.reqPending.Add(int64(n))
	select {
	case s.reqKick <- struct{}{}:
	default:

	}
}

func (s *Session) workConnRequester() {
	for {
		select {
		case <-s.done:
			return
		case <-s.reqKick:
		}

		n := s.reqPending.Swap(0)
		if n <= 0 {
			continue
		}

		if limit := int64(cap(s.workConns)); n > limit {
			n = limit
		}

		if err := s.codec.Write(&protocol.ReqWorkConn{Count: int(n)}); err != nil {
			s.logger.Debug("request work connections", "count", n, "error", err)
		}
	}
}

func (s *Session) AddProxy(ctx context.Context, spec protocol.ProxySpec) (int, error) {
	s.proxiesMu.Lock()
	if _, exists := s.proxies[spec.Name]; exists {
		s.proxiesMu.Unlock()
		return 0, fmt.Errorf("proxy %q already published", spec.Name)
	}
	s.proxiesMu.Unlock()

	s.limits.Publish(spec)

	p, err := proxy.New(proxy.Options{
		Spec:        spec,
		Source:      s,
		Logger:      s.logger,
		RunID:       s.runID,
		BindAddr:    s.bindAddr,
		AcceptLoops: s.acceptLoops,
		ReusePort:   s.reusePort,

		Routes:         s.routes,
		Recorder:       s.recorder,
		Limits:         sessionLimits{limits: s.limits},
		VhostHTTPPort:  s.vhostHTTPPort,
		VhostHTTPSPort: s.vhostHTTPSPort,
	})
	if err != nil {
		return 0, err
	}

	if err := proxy.Bind(ctx, p); err != nil {
		return 0, err
	}

	proxyCtx, cancel := context.WithCancel(ctx)

	s.proxiesMu.Lock()
	if _, exists := s.proxies[spec.Name]; exists {
		s.proxiesMu.Unlock()
		cancel()
		p.Close()
		return 0, fmt.Errorf("proxy %q already published", spec.Name)
	}
	s.proxies[spec.Name] = &runningProxy{proxy: p, cancel: cancel}
	s.proxiesMu.Unlock()

	go func() {
		if err := p.Run(proxyCtx); err != nil && proxyCtx.Err() == nil {
			s.logger.Error("proxy stopped", "proxy", spec.Name, "error", err)
		}
	}()

	return p.RemotePort(), nil
}

func (s *Session) RemoveProxy(name string) error {
	s.proxiesMu.Lock()
	running, exists := s.proxies[name]
	delete(s.proxies, name)
	s.proxiesMu.Unlock()

	if !exists {
		return fmt.Errorf("proxy %q is not published", name)
	}

	s.limits.Remove(name)
	running.cancel()
	return running.proxy.Close()
}

func (s *Session) ProxyPort(name string) (int, bool) {
	s.proxiesMu.RLock()
	defer s.proxiesMu.RUnlock()

	running, ok := s.proxies[name]
	if !ok {
		return 0, false
	}
	return running.proxy.RemotePort(), true
}

func (s *Session) ProxyNames() []string {
	s.proxiesMu.RLock()
	defer s.proxiesMu.RUnlock()

	names := make([]string, 0, len(s.proxies))
	for name := range s.proxies {
		names = append(names, name)
	}
	return names
}

func (s *Session) Close() error {
	var err error

	s.closeOnce.Do(func() {
		close(s.done)

		if s.routes != nil {
			s.routes.RemoveClient(s.runID)
		}

		s.overflowMu.Lock()
		carriers := s.overflow
		s.overflow = nil
		s.overflowMu.Unlock()
		for _, source := range carriers {
			source.Close()
		}

		s.proxiesMu.Lock()
		for _, running := range s.proxies {
			running.cancel()
			running.proxy.Close()
		}
		s.proxies = map[string]*runningProxy{}
		s.proxiesMu.Unlock()

		for {
			select {
			case conn := <-s.workConns:
				conn.Close()
			default:
				err = s.ctrlConn.Close()
				return
			}
		}
	})

	return err
}

// TunnelLimits reports what a published tunnel is held to, for the status view.
func (s *Session) TunnelLimits(name string) *TunnelLimits {
	return s.limits.For(name)
}
