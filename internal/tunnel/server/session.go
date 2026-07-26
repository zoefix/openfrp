package server

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"sync"
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
	// poolTarget is how many connections the client said it would keep warm.
	poolTarget int

	proxiesMu sync.RWMutex
	proxies   map[string]*runningProxy

	closeOnce sync.Once
	done      chan struct{}

	bindAddr    string
	acceptLoops int
	reusePort   bool

	routes         proxy.RouteRegistrar
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

	Routes         proxy.RouteRegistrar
	VhostHTTPPort  int
	VhostHTTPSPort int
}

func newSession(opts SessionOptions) *Session {
	capacity := opts.MaxPool
	if capacity < 1 {
		capacity = 1
	}

	return &Session{
		runID:       opts.RunID,
		name:        opts.Name,
		ctrlConn:    opts.Conn,
		codec:       opts.Codec,
		logger:      opts.Logger,
		workConns:   make(chan net.Conn, capacity),
		poolTarget:  opts.PoolTarget,
		proxies:     make(map[string]*runningProxy),
		done:        make(chan struct{}),
		bindAddr:    opts.BindAddr,
		acceptLoops: opts.AcceptLoops,
		reusePort:   opts.ReusePort,

		routes:         opts.Routes,
		vhostHTTPPort:  opts.VhostHTTPPort,
		vhostHTTPSPort: opts.VhostHTTPSPort,
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
		s.logger.Debug("work connection pooled", "pooled", len(s.workConns))
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
func (s *Session) GetWorkConn(ctx context.Context, proxyName, sourceAddr string) (net.Conn, error) {
	conn, err := s.takeWorkConn(ctx)
	if err != nil {
		return nil, err
	}

	// Written with the unbuffered helper on purpose: this connection switches
	// to raw payload immediately after, and a buffered reader would swallow
	// the leading bytes of it.
	if err := protocol.WriteMessage(conn, &protocol.StartWorkConn{
		ProxyName:  proxyName,
		SourceAddr: sourceAddr,
	}); err != nil {
		conn.Close()
		return nil, fmt.Errorf("session %s: start work conn: %w", s.runID, err)
	}

	s.replenishPool()
	return conn, nil
}

// takeWorkConn pops a pooled connection, asking the client for more if the
// pool has run dry.
func (s *Session) takeWorkConn(ctx context.Context) (net.Conn, error) {
	select {
	case conn := <-s.workConns:
		return conn, nil
	case <-s.done:
		return nil, errors.New("session closed")
	default:
	}

	// Pool empty. Ask the client to dial, then wait.
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

// replenishPool tops the warm pool back up toward its target.
func (s *Session) replenishPool() {
	if deficit := s.poolTarget - len(s.workConns); deficit > 0 {
		s.requestWorkConns(deficit)
	}
}

// requestWorkConns asks the client to open n more work connections. Failure is
// only logged: the control loop will notice a dead connection on its own.
func (s *Session) requestWorkConns(n int) {
	if n <= 0 {
		return
	}
	if err := s.codec.Write(&protocol.ReqWorkConn{Count: n}); err != nil {
		s.logger.Debug("request work connections", "count", n, "error", err)
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
