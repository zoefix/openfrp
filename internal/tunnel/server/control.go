package server

import (
	"context"
	"errors"
	"io"
	"net"
	"time"

	"github.com/zoefix/openfrp/internal/tunnel/protocol"
)

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

		Routes:         s.routeRegistrar(),
		Challenges:     s.challenges,
		Recorder:       s.stats,
		VhostHTTPPort:  s.cfg.VhostHTTPPort,
		VhostHTTPSPort: s.cfg.VhostHTTPSPort,
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

		ObservedAddr: conn.RemoteAddr().String(),
	}); err != nil {
		logger.Warn("send login response", "error", err)
		return
	}

	if err := conn.SetDeadline(time.Time{}); err != nil {
		return
	}

	logger.Info("client connected",
		"run_id", runID,
		"client", login.ClientName,
		"os", login.OS,
		"arch", login.Arch,
		"pool_target", poolTarget)

	session.requestWorkConns(poolTarget)

	s.controlLoop(ctx, session, codec, login)

	logger.Info("client disconnected", "run_id", runID)
}

func (s *Server) controlLoop(ctx context.Context, session *Session, codec *protocol.Codec, login *protocol.Login) {
	logger := session.logger

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
			s.handleCertPush(session, codec, m)

		case *protocol.HTTPChallenge:
			s.handleHTTPChallenge(session, codec, m)

		default:
			logger.Debug("unexpected control message", "type", msg.Type())
		}
	}
}

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

func (s *Server) handleHTTPChallenge(session *Session, codec *protocol.Codec,
	msg *protocol.HTTPChallenge) {

	resp := &protocol.HTTPChallengeResp{}

	if msg.Remove {
		s.challenges.Withdraw(session.runID, msg.Token)
		session.logger.Debug("withdrew an ACME challenge", "domain", msg.Domain)
	} else if err := s.challenges.Publish(
		session.runID, msg.Domain, msg.Token, msg.KeyAuth); err != nil {
		session.logger.Warn("ACME challenge rejected",
			"domain", msg.Domain, "error", err)
		resp.Error = err.Error()
	} else {
		session.logger.Info("answering an ACME challenge on behalf of a client",
			"domain", msg.Domain, "outstanding", s.challenges.Len())
	}

	if err := codec.Write(resp); err != nil {
		session.logger.Debug("send challenge response", "error", err)
	}
}

func (s *Server) handleCertPush(session *Session, codec *protocol.Codec, msg *protocol.CertPush) {
	resp := &protocol.CertPushResp{}

	installed, err := s.certs.Install(msg.FullchainPEM, msg.PrivateKeyPEM)
	if err != nil {
		session.logger.Warn("certificate push rejected",
			"domains", msg.Domains, "error", err)
		resp.Error = err.Error()
	} else {
		session.logger.Info("certificate installed without dropping a connection",
			"patterns", installed,
			"expires", time.Unix(msg.NotAfter, 0).UTC().Format(time.RFC3339),
			"total_patterns", s.certs.Len())
	}

	if err := codec.Write(resp); err != nil {
		session.logger.Debug("send certificate push response", "error", err)
	}
}
