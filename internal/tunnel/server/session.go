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
	// poolTarget is how many connections the client said it would keep warm.
	poolTarget int

	// poolInFlight counts connections asked for and not yet arrived, and
	// poolArrivals counts those received so a request that was never answered
	// can be told from one still on its way.
	//
	// Without this the pool has no flow control. Every visitor tops it up, a
	// dial takes a round trip to land, and in that window every other visitor
	// computes the same deficit and asks for it again — so a burst does not
	// request the shortfall once, it requests it per visitor. The client then
	// dials at the rate visitors arrive, which is the one thing the overflow
	// carrier exists to stop it doing: measured on a real path, a burst the
	// carrier served without a single error still exhausted the connection
	// table of the proxy in front of the client, and the tunnel went down
	// afterwards because the client could no longer dial at all.
	poolInFlight atomic.Int64
	poolArrivals atomic.Int64

	// refillTarget is how deep the timed refill tries to keep the pool while
	// a carrier is carrying the overflow, and it is the number that decides
	// what fraction of visitors get a spliceable connection.
	//
	// The arithmetic is direct: the refill rate divided by the arrival rate
	// is the share served directly. Measured on a real path, eight per half
	// second against ninety-one requests a second came out at 18% spliced,
	// which is exactly 16/91. Everything else went over a carrier and through
	// userspace.
	//
	// So it climbs, with a signal to tell it when to stop. It grows only when
	// the pool is actually running dry — evidence the depth is being used —
	// and only while the client is answering; it halves the moment a request
	// goes unanswered, which is what a client whose egress has run out looks
	// like from here.
	//
	// The distinction from the version of this that was reverted matters. That
	// one grew on misses alone, and misses are guaranteed under load, so it
	// saturated instantly and hammered the very thing it should have been
	// backing off from. Growth here is conditional on success, and there is a
	// congestion signal underneath it.
	refillTarget atomic.Int64
	// poolMisses counts visitors the pool could not serve since the last
	// tick, which is the demand half of the growth condition.
	poolMisses atomic.Int64
	poolMax    int

	// overflow opens multiplexed streams to the client over connections that
	// are already established. It is the relief valve for an empty pool, and
	// empty when the client offered none.
	//
	// Deliberately not the default path. A stream shares its carrier's
	// congestion window with every other stream on it and cannot be spliced,
	// so serving everything this way would give up both properties the direct
	// pool exists for. It is strictly better than the alternative it replaces,
	// which is not a direct connection but a visitor waiting about two round
	// trips for one to be built.
	//
	// Several rather than one, because one is a single TCP connection with
	// everything the pool could not serve concentrated on it — the next
	// bottleneck twice over, sharing both a congestion window and whatever
	// per-connection limit the middlebox in front of the client applies.
	// Measured through a transparent proxy, concentrating the overflow on a
	// single carrier cost a third of the throughput the same burst reached
	// over many short connections. A handful restores that without restoring
	// the connection churn that caused the outage: the count is fixed, so it
	// does not rise with traffic.
	overflowMu   sync.RWMutex
	overflow     []transport.StreamSource
	overflowNext atomic.Uint64

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

	s.poolMax = capacity
	s.refillTarget.Store(int64(opts.PoolTarget))

	// Both stopped by Close via the done channel.
	go s.workConnRequester()
	go s.tendPool()

	return s
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
	// It arrived, so it is no longer something to wait for. Both outcomes
	// below count: what matters to the accounting is that it is here.
	if left := s.poolInFlight.Add(-1); left < 0 {
		// More arrived than were asked for — the priming batch, or a client
		// that supplies eagerly. Not an error, but the counter must not go
		// negative or it would mask a real deficit later.
		s.poolInFlight.Store(0)
	}
	s.poolArrivals.Add(1)

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
	// it was dead.
	//
	// Before making this visitor wait for a connection to be built, try the
	// overflow carrier: a stream on it costs no handshake, no round trip and
	// no entry in whatever NAT or proxy sits in front of the client — which
	// is the resource that actually runs out first when a burst arrives.
	// The pool was too shallow for this visitor. That is the demand half of
	// the refill controller's growth condition.
	s.poolMisses.Add(1)

	if conn, ok := s.overflowConn(ctx, proxyName, sourceAddr); ok {
		if stale > 0 {
			s.logger.Info("discarded work connections that had gone stale",
				"discarded", stale, "proxy", proxyName)
		}
		return conn, nil
	}

	// No carrier either. Ask the client to dial one and wait for it.
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

// maxOverflowCarriers bounds how many a client may offer, so a buggy or
// hostile one cannot pin server memory by offering them without end.
const maxOverflowCarriers = 8

// SetOverflow adds a multiplexed carrier for this session.
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

// overflowConn opens a stream on the carrier and tells it which proxy it
// serves, matching what a work connection is handed.
//
// A failure here is not reported as an error: the caller's next move is to
// ask for a real connection, which is what it would have done anyway. The
// carrier is dropped so a dead one is not tried again by every visitor.
// Carriers are tried round robin, and a failure moves on to the next rather
// than giving up: they fail independently, and one that has just died should
// not send this visitor back to waiting for a dial while three healthy ones
// are sitting there.
func (s *Session) overflowConn(ctx context.Context, proxyName, sourceAddr string) (net.Conn, bool) {
	s.overflowMu.RLock()
	carriers := s.overflow
	s.overflowMu.RUnlock()

	if len(carriers) == 0 {
		return nil, false
	}

	// Round robin rather than always the first. Spreading streams over the
	// carriers is the whole reason there is more than one: each is a single
	// TCP connection with its own congestion window, and its own share of
	// whatever the middlebox in front of the client will allow it.
	start := int(s.overflowNext.Add(1) - 1)

	for attempt := range carriers {
		source := carriers[(start+attempt)%len(carriers)]

		stream, err := source.Open(ctx)
		if err != nil {
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

// dropOverflow discards one carrier, leaving the others in place.
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

// HasOverflow reports whether any carrier is installed.
func (s *Session) HasOverflow() bool {
	s.overflowMu.RLock()
	defer s.overflowMu.RUnlock()
	return len(s.overflow) > 0
}

// DrainPool closes every warm connection and reports how many there were.
//
// For tests that need the state a burst produces — a visitor arriving with
// nothing warm to serve them — without having to generate the burst.
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
func (s *Session) waitWorkConn(ctx context.Context) (net.Conn, error) {
	select {
	case <-s.done:
		return nil, errors.New("session closed")
	default:
	}

	// Counted like any other request: this one is on its way too, and a
	// visitor who asks for it must not make the next visitor ask again.
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

// replenishPool tops the warm pool back up toward its target, counting what
// is already on its way.
//
// When a carrier is available this does nothing, and that is the point.
//
// Topping up on the visitor path couples the client's dial rate to the
// visitor arrival rate: eight outstanding dials at a time, each landing in a
// round trip, is a hundred and sixty new connections a second sustained
// through whatever NAT or proxy the client sits behind. Bounding the
// outstanding count stopped that being unbounded; it did not stop it being
// proportional to load. Measured, a burst the carrier served without one
// error still exhausted the router's connection table, and the tunnel dropped
// afterwards because the client could no longer dial at all.
//
// With a carrier there is no need to chase demand. Visitors are already being
// served; the pool only has to come back for the steady state, where direct
// connections earn their keep by being spliceable. So it refills on a timer
// instead — a fixed, gentle rate that does not rise with load, which is the
// only shape that a connection table in front of the client can survive.
func (s *Session) replenishPool() {
	if s.HasOverflow() {
		return
	}
	s.topUpPool()
}

// topUpPool asks for whatever the pool is short of, minus what is coming.
func (s *Session) topUpPool() {
	inFlight := s.poolInFlight.Load()

	deficit := s.refillTarget.Load() - int64(len(s.workConns)) - inFlight
	if deficit <= 0 {
		return
	}

	// Never ask for more than the pool could hold, however far behind it is.
	if room := int64(cap(s.workConns)) - int64(len(s.workConns)) - inFlight; deficit > room {
		deficit = room
	}
	if deficit <= 0 {
		return
	}

	s.poolInFlight.Add(deficit)
	s.requestWorkConns(int(deficit))
}

// poolSweepInterval is how often unanswered requests are written off. A
// client whose dials are failing answers nothing, and holding those in the
// count forever would leave the pool unable to ask again — a flow-control
// deadlock that looks exactly like a pool that will not fill.
const poolSweepInterval = 5 * time.Second

// adjustRefillTarget moves the refill depth toward what the client's egress
// will actually bear.
//
// Additive increase, multiplicative decrease, on the only congestion signal
// available here: whether requests are being answered. Growth also requires
// the pool to have been running dry, so a quiet tunnel does not accumulate
// depth it has no use for.
func (s *Session) adjustRefillTarget(stalled bool) {
	target := s.refillTarget.Load()
	misses := s.poolMisses.Swap(0)

	switch {
	case stalled:
		// Halve, never below what the client asked to keep warm.
		lowered := max(target/2, int64(s.poolTarget))
		if lowered != target {
			s.refillTarget.Store(lowered)
			s.logger.Info("client cannot sustain the dial rate, easing off",
				"refill_target", lowered, "was", target)
		}

	case misses > 0 && target < int64(s.poolMax):
		// Demand exceeded the pool and the client is keeping up, so it can
		// take a little more. One at a time: the cost of overshooting is the
		// backoff above, and that costs a visitor nothing because the carrier
		// is serving them meanwhile.
		s.refillTarget.Store(target + 1)
	}
}

// poolRefillInterval is how often the pool is topped up while a carrier is
// carrying the load.
//
// It sets the client's dial rate under sustained pressure, and that is the
// number that matters: at most one pool's worth of dials per interval, no
// matter how many visitors arrive. Half a second refills a pool of eight
// within a round trip of going quiet, so ordinary traffic is back on direct
// spliced connections almost immediately, while a burst cannot drive the rate
// any higher than this.
const poolRefillInterval = 500 * time.Millisecond

// tendPool writes off unanswered requests, and tops the pool up on a timer
// while a carrier means the visitor path no longer does.
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
			// Only when a carrier is present. Without one the visitor path
			// still tops up directly, because then there is no alternative to
			// dialling and waiting half a second to start would be half a
			// second added to somebody's request.
			if !s.HasOverflow() {
				continue
			}

			// The controller runs on this tick, not the sweep, because this
			// is the tick it controls. Driven from the five second sweep it
			// converged an order of magnitude too slowly to matter: a
			// twenty second burst got four steps, taking the depth from 8 to
			// 12 and the spliced share from 18% to 21% when the point was to
			// find the ceiling. A dial lands in well under half a second on
			// any path this serves, so arrivals not advancing across one of
			// these is already a stall.
			arrivals := s.poolArrivals.Load()
			stalled := arrivals == lastArrivals && s.poolInFlight.Load() > 0
			if stalled {
				// Requests went out and nothing came back. From here that is
				// indistinguishable from — and in practice is — a client whose
				// egress has run out of room, so give it back fast.
				s.poolInFlight.Store(0)
			}
			lastArrivals = arrivals

			s.adjustRefillTarget(stalled)
			s.topUpPool()

		case <-sweep.C:
			// The slower sweep still exists for the case the refill tick
			// cannot see: no carrier, so the branch above never runs, and an
			// unanswered request would otherwise strand the in-flight count
			// and stop the pool ever asking again.
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
