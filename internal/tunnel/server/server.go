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
	"github.com/zoefix/openfrp/internal/tunnel/protocol"
	"github.com/zoefix/openfrp/internal/tunnel/transport"
	"github.com/zoefix/openfrp/pkg/netutil"

	// Register the proxy kinds. Blank imports keep the registry populated
	// without server needing to name each factory.
	_ "github.com/zoefix/openfrp/internal/tunnel/server/proxy"
)

// handshakeTimeout bounds the greeting and first message on a new connection.
//
// It exists so a connection that opens and then says nothing cannot pin a
// goroutine indefinitely. It MUST be cleared before a work connection starts
// relaying, or the deadline would kill every long-lived tunnel.
const handshakeTimeout = 15 * time.Second

// Server accepts client connections and publishes their tunnels.
type Server struct {
	cfg      *config.Server
	logger   *slog.Logger
	registry *Registry
	version  string

	listenerMu sync.Mutex
	listener   net.Listener

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
		cfg:      cfg,
		logger:   logger,
		registry: NewRegistry(),
		version:  version,
	}, nil
}

// Registry exposes the connected sessions.
func (s *Server) Registry() *Registry { return s.registry }

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
	ln, err := netutil.Listen(ctx, "tcp", addr, netutil.ListenOptions{
		ReusePort: s.cfg.AcceptLoops != 1,
		KeepAlive: 30 * time.Second,
	}, s.cfg.AcceptLoops)
	if err != nil {
		return fmt.Errorf("server: %w", err)
	}

	s.listener = ln
	s.logger.Info("control listener started",
		"addr", ln.Addr().String(),
		"accept_loops", netutil.AcceptLoops(ln),
		"version", s.version)
	return nil
}

// Serve accepts connections until ctx is cancelled.
func (s *Server) Serve(ctx context.Context) error {
	if err := s.Listen(ctx); err != nil {
		return err
	}

	s.listenerMu.Lock()
	ln := s.listener
	s.listenerMu.Unlock()

	go func() {
		<-ctx.Done()
		ln.Close()
	}()

	defer s.wg.Wait()
	defer s.registry.CloseAll()

	for {
		conn, err := ln.Accept()
		if err != nil {
			if ctx.Err() != nil || errors.Is(err, net.ErrClosed) {
				s.logger.Info("control listener stopped")
				return nil
			}
			return fmt.Errorf("server: accept: %w", err)
		}

		s.wg.Add(1)
		go func() {
			defer s.wg.Done()
			s.handleConn(ctx, conn)
		}()
	}
}

// handleConn reads the greeting and dispatches on the declared mode.
func (s *Server) handleConn(ctx context.Context, conn net.Conn) {
	if err := netutil.TuneConn(conn, netutil.DefaultTCPOptions()); err != nil {
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

// Close stops the listener and every session.
func (s *Server) Close() error {
	s.listenerMu.Lock()
	ln := s.listener
	s.listener = nil
	s.listenerMu.Unlock()

	s.registry.CloseAll()

	if ln != nil {
		return ln.Close()
	}
	return nil
}
