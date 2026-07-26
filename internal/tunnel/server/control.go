package server

import (
	"context"
	"errors"
	"io"
	"net"
	"time"

	"github.com/zoefix/openfrp/internal/tunnel/protocol"
)

// handleLogin authenticates a client and runs its control loop.
func (s *Server) handleLogin(ctx context.Context, conn net.Conn, login *protocol.Login) {
	logger := s.logger.With("remote", conn.RemoteAddr().String())

	if err := protocol.CheckVersion(login.Version); err != nil {
		protocol.WriteMessage(conn, &protocol.LoginResp{
			Version: protocol.Version,
			Error:   err.Error(),
		})
		conn.Close()
		return
	}

	if err := protocol.VerifyAuth(s.cfg.Token, login.AuthKey, login.Timestamp,
		time.Now(), protocol.DefaultAuthSkew); err != nil {
		logger.Warn("login failed authentication", "client_name", login.ClientName)
		protocol.WriteMessage(conn, &protocol.LoginResp{
			Version: protocol.Version,
			Error:   "authentication failed",
		})
		conn.Close()
		return
	}

	runID := login.RunID
	if runID == "" {
		generated, err := protocol.NewRunID()
		if err != nil {
			logger.Error("generate run id", "error", err)
			conn.Close()
			return
		}
		runID = generated
	}

	poolTarget := login.PoolCount
	if poolTarget < 1 {
		poolTarget = 1
	}
	if poolTarget > s.cfg.MaxPoolCount {
		logger.Debug("clamping requested pool size",
			"requested", poolTarget, "max", s.cfg.MaxPoolCount)
		poolTarget = s.cfg.MaxPoolCount
	}

	// The control connection carries only framed messages for the rest of its
	// life, so a buffered codec is the right tool here.
	codec := protocol.NewCodec(conn)

	session := newSession(SessionOptions{
		RunID:       runID,
		Name:        login.ClientName,
		Conn:        conn,
		Codec:       codec,
		Logger:      logger.With("run_id", runID, "client", login.ClientName),
		PoolTarget:  poolTarget,
		MaxPool:     s.cfg.MaxPoolCount,
		BindAddr:    s.cfg.BindAddr,
		AcceptLoops: s.cfg.AcceptLoops,
		ReusePort:   s.cfg.AcceptLoops != 1,
	})

	if replaced := s.registry.Add(session); replaced != nil {
		logger.Info("replacing stale session", "run_id", runID)
		replaced.Close()
	}
	defer func() {
		s.registry.Remove(session)
		session.Close()
	}()

	if err := codec.Write(&protocol.LoginResp{
		Version:       protocol.Version,
		RunID:         runID,
		ServerVersion: s.version,
	}); err != nil {
		logger.Warn("send login response", "error", err)
		return
	}

	// The handshake is over; the control connection is long-lived from here.
	// Liveness is enforced by the heartbeat below rather than by a deadline.
	if err := conn.SetDeadline(time.Time{}); err != nil {
		return
	}

	logger.Info("client connected",
		"run_id", runID,
		"client", login.ClientName,
		"os", login.OS,
		"arch", login.Arch,
		"pool_target", poolTarget)

	// Prime the warm pool so the first user connection does not pay for a dial.
	session.requestWorkConns(poolTarget)

	s.controlLoop(ctx, session, codec, login)

	logger.Info("client disconnected", "run_id", runID)
}

// controlLoop services the client's control messages until the connection
// closes or ctx is cancelled.
func (s *Server) controlLoop(ctx context.Context, session *Session, codec *protocol.Codec, login *protocol.Login) {
	logger := session.logger

	// Unblock the read when the server is shutting down.
	go func() {
		<-ctx.Done()
		session.Close()
	}()

	for {
		msg, err := codec.Read()
		if err != nil {
			if errors.Is(err, io.EOF) || ctx.Err() != nil {
				return
			}
			if errors.Is(err, protocol.ErrUnknownType) {
				// A newer client sent something this build predates. The
				// stream is still aligned, so keep going.
				logger.Debug("ignoring unknown message", "error", err)
				continue
			}
			logger.Debug("control read", "error", err)
			return
		}

		switch m := msg.(type) {
		case *protocol.Ping:
			if err := codec.Write(&protocol.Pong{Timestamp: m.Timestamp}); err != nil {
				logger.Debug("send pong", "error", err)
				return
			}

		case *protocol.NewProxy:
			s.handleNewProxy(ctx, session, codec, m)

		case *protocol.CloseProxy:
			if err := session.RemoveProxy(m.Name); err != nil {
				logger.Debug("close proxy", "proxy", m.Name, "error", err)
			} else {
				logger.Info("proxy withdrawn", "proxy", m.Name)
			}

		case *protocol.CertPush:
			// Edge TLS termination arrives in P6. Acknowledge explicitly so a
			// client that runs ahead of the server gets a clear answer rather
			// than silence.
			codec.Write(&protocol.CertPushResp{
				Error: "certificate push is not supported by this server build",
			})

		default:
			logger.Debug("unexpected control message", "type", msg.Type())
		}
	}
}

// handleNewProxy publishes a proxy and reports the outcome.
func (s *Server) handleNewProxy(ctx context.Context, session *Session, codec *protocol.Codec, msg *protocol.NewProxy) {
	resp := &protocol.NewProxyResp{Name: msg.Proxy.Name}

	port, err := session.AddProxy(ctx, msg.Proxy)
	if err != nil {
		session.logger.Warn("publish proxy", "proxy", msg.Proxy.Name, "error", err)
		resp.Error = err.Error()
	} else {
		resp.RemotePort = port
		session.logger.Info("proxy published",
			"proxy", msg.Proxy.Name,
			"kind", msg.Proxy.Kind,
			"remote_port", port)
	}

	if err := codec.Write(resp); err != nil {
		session.logger.Debug("send proxy response", "error", err)
	}
}
