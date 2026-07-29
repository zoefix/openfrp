// Package server implements the OpenFrp server daemon.
package server

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"strconv"
	"sync"
	"time"

	"github.com/zoefix/openfrp/internal/config"
	"github.com/zoefix/openfrp/internal/stats"
	"github.com/zoefix/openfrp/internal/tunnel/protocol"
	"github.com/zoefix/openfrp/internal/tunnel/server/proxy"
	"github.com/zoefix/openfrp/internal/tunnel/transport"
	"github.com/zoefix/openfrp/internal/tunnel/vhost"
	"github.com/zoefix/openfrp/pkg/netutil"
)

// handshakeTimeout bounds the greeting and first message on a new connection.
//
// It exists so a connection that opens and then says nothing cannot pin a
// goroutine indefinitely. It MUST be cleared before a work connection starts
// relaying, or the deadline would kill every long-lived tunnel.
const handshakeTimeout = 15 * time.Second

// Server accepts client connections and publishes their tunnels.
type Server struct {
	cfg        *config.Server
	logger     *slog.Logger
	registry   *Registry
	router     *vhost.Router
	certs      *CertStore
	challenges *ChallengeStore
	stats      *stats.Registry
	version    string

	listenerMu sync.Mutex
	listener   net.Listener
	vhosts     []*vhostListener

	// listenOpts is remembered so accepted connections can be tuned to match
	// on platforms that do not inherit the listener's options.
	listenOpts netutil.ListenOptions

	wg sync.WaitGroup
}

// New builds a server from cfg.
func New(cfg *config.Server, logger *slog.Logger, version string) (*Server, error) {
	if cfg == nil {
		return nil, errors.New("server: nil config")
	}
	if logger == nil {
		logger = slog.Default()
	}
	if cfg.Token == "" {
		logger.Warn("no token configured: any client that can reach this port may connect")
	}

	return &Server{
		cfg:        cfg,
		logger:     logger,
		registry:   NewRegistry(),
		router:     vhost.NewRouter(),
		certs:      NewCertStore(),
		challenges: NewChallengeStore(),
		stats:      stats.NewRegistry(),
		version:    version,
	}, nil
}

// Registry exposes the connected sessions.
func (s *Server) Registry() *Registry { return s.registry }

// Router exposes the domain routing table.
func (s *Server) Router() *vhost.Router { return s.router }

// Certs exposes the certificate store used for edge TLS termination.
func (s *Server) Certs() *CertStore { return s.certs }

// Challenges exposes the ACME HTTP-01 challenges clients have published.
func (s *Server) Challenges() *ChallengeStore { return s.challenges }

// Stats exposes the traffic counters.
func (s *Server) Stats() *stats.Registry { return s.stats }

// VhostAddr reports the bound address of one vhost listener, or nil when that
// scheme is not configured.
func (s *Server) VhostAddr(scheme vhost.Scheme) net.Addr {
	s.listenerMu.Lock()
	defer s.listenerMu.Unlock()

	for _, v := range s.vhosts {
		if v.scheme == scheme {
			return v.addr()
		}
	}
	return nil
}

// routeRegistrar hands proxies only the two operations they need, so the vhost
// proxies never see the full router.
func (s *Server) routeRegistrar() proxy.RouteRegistrar {
	if len(s.vhosts) == 0 {
		return nil
	}
	return s.router
}

// Addr reports the bound control address, or nil before Serve has bound it.
func (s *Server) Addr() net.Addr {
	s.listenerMu.Lock()
	defer s.listenerMu.Unlock()

	if s.listener == nil {
		return nil
	}
	return s.listener.Addr()
}

// Listen binds the control port without serving. Serve calls this when needed;
// callers use it directly when they must know the address first, which is what
// tests binding port 0 need.
func (s *Server) Listen(ctx context.Context) error {
	s.listenerMu.Lock()
	defer s.listenerMu.Unlock()

	if s.listener != nil {
		return nil
	}

	addr := net.JoinHostPort(s.cfg.BindAddr, strconv.Itoa(s.cfg.BindPort))
	s.listenOpts = netutil.ListenOptions{
		ReusePort: s.cfg.AcceptLoops != 1,
		KeepAlive: 30 * time.Second,
		// Every legitimate connection here opens with our preamble, so accept
		// can wait for it: one wakeup saved per connection, and a scanner that
		// connects silently never costs a goroutine.
		DeferAccept: 5 * time.Second,
	}
	ln, err := netutil.Listen(ctx, "tcp", addr, s.listenOpts, s.cfg.AcceptLoops)
	if err != nil {
		return fmt.Errorf("server: %w", err)
	}

	s.listener = ln
	s.logger.Info("control listener started",
		"addr", ln.Addr().String(),
		"accept_loops", netutil.AcceptLoops(ln),
		"version", s.version)

	// Bind the vhost listeners here too, so a port conflict fails startup
	// rather than surfacing later as an unexplained publish rejection.
	vhostPorts := []struct {
		scheme vhost.Scheme
		port   int
	}{
		{vhost.SchemeHTTP, s.cfg.VhostHTTPPort},
		{vhost.SchemeHTTPS, s.cfg.VhostHTTPSPort},
	}

	for _, want := range vhostPorts {
		if want.port == 0 {
			continue
		}
		v := newVhostListener(want.scheme, want.port, s.cfg.BindAddr,
			s.router, s.registry, s.certs, s.challenges, s.stats,
			s.logger, s.cfg.AcceptLoops)
		if err := v.listen(ctx); err != nil {
			for _, started := range s.vhosts {
				started.close()
			}
			s.vhosts = nil
			ln.Close()
			s.listener = nil
			return err
		}
		s.vhosts = append(s.vhosts, v)
	}

	if len(s.vhosts) == 0 {
		s.logger.Info("no vhost ports configured; http and https tunnels " +
			"cannot be published")
	}
	return nil
}

// Serve accepts connections until ctx is cancelled.
func (s *Server) Serve(ctx context.Context) error {
	if err := s.Listen(ctx); err != nil {
		return err
	}

	s.listenerMu.Lock()
	ln := s.listener
	vhosts := append([]*vhostListener(nil), s.vhosts...)
	s.listenerMu.Unlock()

	go func() {
		<-ctx.Done()
		ln.Close()
	}()

	// Each vhost listener serves alongside the control listener.
	for _, v := range vhosts {
		s.wg.Add(1)
		go func() {
			defer s.wg.Done()
			if err := v.serve(ctx); err != nil {
				s.logger.Error("vhost listener stopped",
					"scheme", string(v.scheme), "error", err)
			}
		}()
	}

	defer s.wg.Wait()
	defer s.registry.CloseAll()

	// The control listener carries one connection per tunnel handoff at steady
	// state — every work connection lands here — so it accepts in parallel for
	// the same reason the proxy listeners do.
	err := netutil.Serve(ln, func(conn net.Conn) {
		s.wg.Add(1)
		go func() {
			defer s.wg.Done()
			s.handleConn(ctx, conn)
		}()
	})
	if err != nil {
		if ctx.Err() != nil || errors.Is(err, net.ErrClosed) {
			s.logger.Info("control listener stopped")
			return nil
		}
		return fmt.Errorf("server: accept: %w", err)
	}
	s.logger.Info("control listener stopped")
	return nil
}

// handleConn reads the greeting and dispatches on the declared mode.
func (s *Server) handleConn(ctx context.Context, conn net.Conn) {
	// Free on Linux: the listener was tuned at bind time and this connection
	// was cloned from it. Work connections in particular arrive at the rate
	// tunnelled connections are served, so this is on the hot path.
	if err := netutil.TuneAccepted(conn, s.listenOpts); err != nil {
		s.logger.Debug("tune connection", "error", err)
	}

	if err := conn.SetDeadline(time.Now().Add(handshakeTimeout)); err != nil {
		conn.Close()
		return
	}

	preamble, err := protocol.ReadPreamble(conn)
	if err != nil {
		// Routine on a public port: scanners and stray HTTP land here. Debug
		// level keeps them out of the operator's face.
		s.logger.Debug("rejected connection", "remote", conn.RemoteAddr().String(), "error", err)
		conn.Close()
		return
	}

	switch preamble.Mode {
	case protocol.ModePlain:
		s.handlePlain(ctx, conn)
	case protocol.ModeMux:
		s.handleMux(ctx, conn)
	default:
		s.logger.Debug("unsupported mode", "mode", preamble.Mode)
		conn.Close()
	}
}

// handleMux serves a yamux session, treating every stream as a plain
// connection.
func (s *Server) handleMux(ctx context.Context, conn net.Conn) {
	// The session outlives the handshake, so the deadline must go.
	if err := conn.SetDeadline(time.Time{}); err != nil {
		conn.Close()
		return
	}

	acceptor, err := transport.NewMuxAcceptor(conn, transport.DefaultMuxConfig())
	if err != nil {
		s.logger.Warn("start mux session", "error", err)
		conn.Close()
		return
	}
	defer acceptor.Close()

	go func() {
		<-ctx.Done()
		acceptor.Close()
	}()

	var wg sync.WaitGroup
	defer wg.Wait()

	for {
		stream, err := acceptor.Accept()
		if err != nil {
			return
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			// Streams get their own handshake budget.
			stream.SetDeadline(time.Now().Add(handshakeTimeout))
			s.handlePlain(ctx, stream)
		}()
	}
}

// handlePlain reads the first message and decides what the connection is for.
func (s *Server) handlePlain(ctx context.Context, conn net.Conn) {
	// Read the opening message without buffering: a work connection carries
	// raw payload straight after it, and a buffered reader would eat the start
	// of that payload.
	msg, err := protocol.ReadMessage(conn)
	if err != nil {
		s.logger.Debug("read opening message", "remote", conn.RemoteAddr().String(), "error", err)
		conn.Close()
		return
	}

	switch m := msg.(type) {
	case *protocol.Login:
		s.handleLogin(ctx, conn, m)
	case *protocol.NewWorkConn:
		s.attachWorkConn(conn, m)
	case *protocol.NewMuxConn:
		s.attachMuxConn(conn, m)
	default:
		s.logger.Debug("unexpected opening message", "type", msg.Type())
		protocol.WriteMessage(conn, &protocol.LoginResp{
			Version: protocol.Version,
			Error:   fmt.Sprintf("unexpected opening message %s", msg.Type()),
		})
		conn.Close()
	}
}

// attachWorkConn hands a work connection to the session that owns it.
func (s *Server) attachWorkConn(conn net.Conn, msg *protocol.NewWorkConn) {
	if err := protocol.VerifyAuth(s.cfg.Token, msg.AuthKey, msg.Timestamp,
		time.Now(), protocol.DefaultAuthSkew); err != nil {
		s.logger.Warn("work connection failed authentication",
			"remote", conn.RemoteAddr().String(), "run_id", msg.RunID)
		conn.Close()
		return
	}

	session, err := s.registry.Get(msg.RunID)
	if err != nil {
		s.logger.Debug("work connection for unknown session", "run_id", msg.RunID)
		conn.Close()
		return
	}

	// Critical: clear the handshake deadline. This connection is about to sit
	// idle in the warm pool and then carry a long-lived tunnel; leaving the
	// deadline set would kill it mid-transfer.
	if err := conn.SetDeadline(time.Time{}); err != nil {
		conn.Close()
		return
	}

	session.AddWorkConn(conn)
}

// attachMuxConn takes a connection the client has offered as an overflow
// carrier and makes the server the side that opens streams on it.
//
// The role reversal is the point. Every other connection in this protocol is
// dialled by the client because the client is the one that can reach out; on
// this one the server needs to initiate, since it is the server that learns a
// visitor has arrived. Multiplexing is what allows that without the client
// having to be reachable.
func (s *Server) attachMuxConn(conn net.Conn, msg *protocol.NewMuxConn) {
	if err := protocol.VerifyAuth(s.cfg.Token, msg.AuthKey, msg.Timestamp,
		time.Now(), protocol.DefaultAuthSkew); err != nil {
		s.logger.Warn("overflow carrier failed authentication",
			"remote", conn.RemoteAddr().String(), "run_id", msg.RunID)
		conn.Close()
		return
	}

	session, err := s.registry.Get(msg.RunID)
	if err != nil {
		s.logger.Debug("overflow carrier for unknown session", "run_id", msg.RunID)
		conn.Close()
		return
	}

	// The carrier outlives the handshake by design: it stays open for as long
	// as the client does, so the deadline has to go or it would kill it.
	if err := conn.SetDeadline(time.Time{}); err != nil {
		conn.Close()
		return
	}

	source, err := transport.NewMuxSource(conn, transport.DefaultMuxConfig())
	if err != nil {
		s.logger.Warn("could not start the overflow carrier", "error", err)
		conn.Close()
		return
	}

	session.SetOverflow(source)
}

// Close stops the listener and every session.
func (s *Server) Close() error {
	s.listenerMu.Lock()
	ln := s.listener
	s.listener = nil
	vhosts := s.vhosts
	s.vhosts = nil
	s.listenerMu.Unlock()

	s.registry.CloseAll()

	var errs []error
	for _, v := range vhosts {
		if err := v.close(); err != nil {
			errs = append(errs, err)
		}
	}
	if ln != nil {
		if err := ln.Close(); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}
