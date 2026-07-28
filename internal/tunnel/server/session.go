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
)

// workConnTimeout bounds how long a user connection waits for the client to
// supply a work connection before we give up and close it.
const workConnTimeout = 10 * time.Second

// Session is one connected client: its control connection, its warm pool of
// work connections, and the proxies it has published.
type Session struct {
	runID string
	name  string

	ctrlConn net.Conn
	codec    *protocol.Codec
	logger   *slog.Logger

	// workConns is the warm pool. The client pre-establishes connections into
	// it so that a user connection does not have to wait a full round trip for
	// one to be dialled.
	workConns chan net.Conn
	// poolFloor is how many connections the client said it would keep warm,
	// and the size the pool returns to when nothing is happening.
	poolFloor int
	// poolMax is the ceiling the server will let this client hold.
	poolMax int

	// poolWant is how many are currently being asked for, which rises with
	// demand and settles back to poolFloor. See adaptPool.
	poolWant atomic.Int64
	// poolMisses counts visitors who found the pool empty since the last
	// decay tick, which is the signal the pool is too small.
	poolMisses atomic.Int64
	// poolPeak is the largest target reached, so growth is reported once
	// rather than on every step.
	poolPeak atomic.Int64

	// reqPending accumulates work-connection requests between writes, and
	// reqKick wakes the requester goroutine that flushes them. One control
	// message per accumulation instead of one per user connection: under a
	// burst of arrivals the previous shape serialised every handler on the
	// codec's write mutex and spent a syscall per connection saying "one
	// more, please" — this batches everything that arrived while the last
	// write was in flight into a single message.
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
}

type runningProxy struct {
	proxy  proxy.Proxy
	cancel context.CancelFunc
}

// SessionOptions configures a new session.
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
	// Challenges is where this client's ACME validations are held, so they can
	// be withdrawn when it disconnects.
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
		poolFloor:   opts.PoolTarget,
		poolMax:     capacity,
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
	s.poolWant.Store(int64(opts.PoolTarget))
	s.poolPeak.Store(int64(opts.PoolTarget))

	// Both stopped by Close via the done channel.
	go s.workConnRequester()
	go s.adaptPool()

	return s
}

// Pool adaptation constants.
//
// poolDecayInterval is how long the pool must go without a single visitor
// finding it empty before it starts giving connections back. It is generous
// on purpose: a pool that shrinks between two bursts has to be rebuilt at the
// cost of the very stalls it grew to prevent, and an idle pooled connection
// costs a socket and nothing else.
const (
	poolDecayInterval = 90 * time.Second
	poolDecayFraction = 4 // give back a quarter of the surplus per interval
)

// adaptPool returns the pool toward its floor during quiet periods.
//
// Growth is not here — it happens where the evidence appears, in the miss
// path of GetWorkConn. This is only the other half of the loop: without it a
// pool that grew for one burst would hold those sockets until the client
// disconnected.
func (s *Session) adaptPool() {
	ticker := time.NewTicker(poolDecayInterval)
	defer ticker.Stop()

	for {
		select {
		case <-s.done:
			return
		case <-ticker.C:
		}

		s.decayPool()
	}
}

// decayPool gives back part of the surplus if the last interval passed
// without a single visitor finding the pool empty.
func (s *Session) decayPool() {
	// Any miss at all means the last interval needed what it had.
	if s.poolMisses.Swap(0) > 0 {
		return
	}

	want := s.poolWant.Load()
	surplus := want - int64(s.poolFloor)
	if surplus <= 0 {
		return
	}

	s.poolWant.Store(want - max(surplus/poolDecayFraction, 1))
}

// growPool records that a visitor found the pool empty and widens it.
//
// One connection per miss, which needs no estimate of the arrival rate to be
// right: a burst of forty simultaneous visitors produces forty misses and
// forty steps, landing exactly where the demand was. The cost of being wrong
// upward is an idle socket; the cost of being wrong downward is a visitor
// waiting about two round trips — the server asking, the client dialling,
// the connection being registered — before their request moves at all.
func (s *Session) growPool() {
	s.poolMisses.Add(1)

	want := s.poolWant.Add(1)
	if want > int64(s.poolMax) {
		s.poolWant.Store(int64(s.poolMax))
		want = int64(s.poolMax)
	}

	// Reported once per new high, not per step: a burst is many misses and
	// would otherwise be many lines saying the same thing.
	if peak := s.poolPeak.Load(); want > peak &&
		s.poolPeak.CompareAndSwap(peak, want) {
		s.logger.Info("widening the warm pool to keep up with demand",
			"pool", want, "configured", s.poolFloor, "max", s.poolMax)
	}
}

// RunID identifies the session.
func (s *Session) RunID() string { return s.runID }

// Name is the operator-chosen client name.
func (s *Session) Name() string { return s.name }

// AddWorkConn deposits a freshly dialled work connection into the pool.
//
// If the pool is already full the connection is closed rather than queued: a
// client that over-supplies should not be able to pin server memory.
func (s *Session) AddWorkConn(conn net.Conn) {
	select {
	case <-s.done:
		conn.Close()
	case s.workConns <- conn:
		// Guarded: one work connection arrives per tunnelled connection, and
		// the attribute boxing allocates even with debug logging off.
		if s.logger.Enabled(context.Background(), slog.LevelDebug) {
			s.logger.Debug("work connection pooled", "pooled", len(s.workConns))
		}
	default:
		s.logger.Warn("work connection pool full, dropping connection",
			"capacity", cap(s.workConns))
		conn.Close()
	}
}

// GetWorkConn implements proxy.WorkConnSource.
//
// The returned connection has already been told which proxy it serves, and
// carries nothing but raw payload afterwards. It is deliberately handed back
// unwrapped so netutil.Relay can reach the splice fast path.
//
// A pooled connection may be dead by the time it is wanted, and this is the
// ordinary case rather than the exceptional one. The pool is warm, so its
// connections sit idle for exactly as long as nobody visits the site — hours,
// on a tunnel that is quiet overnight — and anything on the path holding NAT
// or proxy state gives up long before that. Nothing reads from a parked work
// connection, so the peer's close is not noticed when it arrives; it waits in
// the socket until the first write, which is this one.
//
// So a dead one is skipped and the next is tried. Failing the visitor's
// request on it, which is what this used to do, is what made a site that had
// been quiet answer 502 to the first few people and then work perfectly: one
// 502 per stale connection, until the visitors had emptied the pool of them.
func (s *Session) GetWorkConn(ctx context.Context, proxyName, sourceAddr string) (net.Conn, error) {
	// The pool is topped back up however this ends. A request that discarded
	// every connection it found needs replacements more than one that took a
	// live connection first time.
	defer s.replenishPool()

	var stale int

	// Warm connections first. Each stale one costs a failed write and nothing
	// else — the close is already in the socket, so this does not wait.
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

	// Nothing warm left, either because the pool was empty or because all of
	// it was dead. Ask the client for one and wait for it.
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

// startWorkConn tells one connection which proxy it is about to carry.
//
// Written with the unbuffered helper on purpose: this connection switches to
// raw payload immediately after, and a buffered reader would swallow the
// leading bytes of it.
func (s *Session) startWorkConn(conn net.Conn, proxyName, sourceAddr string) error {
	return protocol.WriteMessage(conn, &protocol.StartWorkConn{
		ProxyName:  proxyName,
		SourceAddr: sourceAddr,
	})
}

// takePooled pops a warm connection if there is one, without waiting.
func (s *Session) takePooled() (net.Conn, bool) {
	select {
	case conn := <-s.workConns:
		return conn, true
	default:
		return nil, false
	}
}

// waitWorkConn asks the client to dial one and waits for it to arrive.
//
// Reaching here is the miss: this visitor is about to wait out a request to
// the client and a fresh dial back, which on a real path is roughly two round
// trips before their first byte moves. It is the evidence the pool is too
// small, and the only place that evidence exists.
func (s *Session) waitWorkConn(ctx context.Context) (net.Conn, error) {
	select {
	case <-s.done:
		return nil, errors.New("session closed")
	default:
	}

	s.growPool()
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

// replenishPool tops the warm pool back up toward its current target.
func (s *Session) replenishPool() {
	if deficit := int(s.poolWant.Load()) - len(s.workConns); deficit > 0 {
		s.requestWorkConns(deficit)
	}
}

// PoolTarget reports how many warm connections are currently being asked for.
func (s *Session) PoolTarget() int { return int(s.poolWant.Load()) }

// requestWorkConns asks the client to open n more work connections.
//
// The request is accumulated rather than written: the requester goroutine
// flushes whatever has gathered into one control message. Callers on the user
// connection path therefore never block on the codec's write mutex.
func (s *Session) requestWorkConns(n int) {
	if n <= 0 {
		return
	}
	s.reqPending.Add(int64(n))
	select {
	case s.reqKick <- struct{}{}:
	default:
		// The requester is already awake and will pick this up in its next
		// swap; a second kick would only make it spin once on zero.
	}
}

// workConnRequester turns accumulated requests into control messages, one
// message per batch. Failure is only logged: the control loop will notice a
// dead connection on its own.
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
		// The pool can never hold more than its capacity, so asking for more
		// than that only makes the client dial connections the pool will
		// refuse. Anything clamped away is re-requested by the next
		// replenish if it turns out to be needed.
		if limit := int64(cap(s.workConns)); n > limit {
			n = limit
		}

		if err := s.codec.Write(&protocol.ReqWorkConn{Count: int(n)}); err != nil {
			s.logger.Debug("request work connections", "count", n, "error", err)
		}
	}
}

// AddProxy publishes a proxy and starts serving it.
func (s *Session) AddProxy(ctx context.Context, spec protocol.ProxySpec) (int, error) {
	s.proxiesMu.Lock()
	if _, exists := s.proxies[spec.Name]; exists {
		s.proxiesMu.Unlock()
		return 0, fmt.Errorf("proxy %q already published", spec.Name)
	}
	s.proxiesMu.Unlock()

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
		VhostHTTPPort:  s.vhostHTTPPort,
		VhostHTTPSPort: s.vhostHTTPSPort,
	})
	if err != nil {
		return 0, err
	}

	// Bind before reporting success so the response can carry the port the
	// kernel actually assigned when the client asked for any port.
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

// RemoveProxy withdraws a published proxy.
func (s *Session) RemoveProxy(name string) error {
	s.proxiesMu.Lock()
	running, exists := s.proxies[name]
	delete(s.proxies, name)
	s.proxiesMu.Unlock()

	if !exists {
		return fmt.Errorf("proxy %q is not published", name)
	}

	running.cancel()
	return running.proxy.Close()
}

// ProxyPort reports the port a published proxy is bound to. It answers the
// question the status panel and the tests both need: which port did the server
// actually allocate when the client asked for any.
func (s *Session) ProxyPort(name string) (int, bool) {
	s.proxiesMu.RLock()
	defer s.proxiesMu.RUnlock()

	running, ok := s.proxies[name]
	if !ok {
		return 0, false
	}
	return running.proxy.RemotePort(), true
}

// ProxyNames lists the currently published proxies.
func (s *Session) ProxyNames() []string {
	s.proxiesMu.RLock()
	defer s.proxiesMu.RUnlock()

	names := make([]string, 0, len(s.proxies))
	for name := range s.proxies {
		names = append(names, name)
	}
	return names
}

// Close tears the session down: every proxy, every pooled connection, and the
// control connection itself.
func (s *Session) Close() error {
	var err error

	s.closeOnce.Do(func() {
		close(s.done)

		// Withdraw every domain this client claimed. Individual proxy Close
		// calls do this too, but a client that drops mid-publish can leave a
		// route behind, and a stale route would black-hole its hostname.
		if s.routes != nil {
			s.routes.RemoveClient(s.runID)
		}

		// ACME challenges deliberately outlive the session that published
		// them. Issuance runs in its own process, which opens a control
		// connection, publishes, and hangs up long before the authority
		// fetches anything — so a disconnect is the ordinary end of a
		// successful publish, not a client that vanished. Withdrawing here
		// deleted every challenge within milliseconds of it being stored, and
		// every HTTP-01 validation got the unclaimed-host 404. Cleanup is the
		// solver's explicit withdrawal, and the store's TTL behind it.

		s.proxiesMu.Lock()
		for _, running := range s.proxies {
			running.cancel()
			running.proxy.Close()
		}
		s.proxies = map[string]*runningProxy{}
		s.proxiesMu.Unlock()

		// Drain the warm pool. These sockets are otherwise leaked.
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
