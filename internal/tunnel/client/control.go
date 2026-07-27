package client

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"sync"
	"time"

	"github.com/zoefix/openfrp/internal/config"
	"github.com/zoefix/openfrp/internal/tunnel/protocol"
)

// session is one live connection to the server.
type session struct {
	client *Client
	conn   net.Conn
	codec  *protocol.Codec
	logger *slog.Logger

	runID         string
	serverVersion string

	// tunnels maps a proxy name onto its local target, so a work connection
	// assigned to a proxy knows where to forward.
	tunnelsMu sync.RWMutex
	tunnels   map[string]config.Tunnel

	wg sync.WaitGroup
}

// serve publishes the configured tunnels and runs the control loop.
func (s *session) serve(ctx context.Context) error {
	ctx, cancel := context.WithCancel(ctx)

	// Order matters, and defers run last-registered-first: this cancels and
	// then waits.
	//
	// Waiting first deadlocks the moment any goroutine's only exit is the
	// cancellation — it would never arrive, and the client would sit alive
	// with no connection, never reconnecting and logging nothing at all. The
	// heartbeat happened to survive that ordering because its next write fails
	// on a dead socket and it returns on its own; the certificate watcher has
	// no such accident to rely on.
	defer s.wg.Wait()
	defer cancel()

	s.tunnels = make(map[string]config.Tunnel)
	if err := s.publishTunnels(); err != nil {
		return err
	}

	// Certificates go up after the tunnels exist on the server, so the store
	// is populated before the first request can arrive for one of them. The
	// record of what was sent is cleared first: this is a new session, and the
	// server it belongs to holds nothing yet.
	s.client.pushedCerts.reset()
	s.client.pushCertificates(ctx, s)

	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		s.client.watchCertificates(ctx, s)
	}()

	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		s.heartbeat(ctx)
	}()

	go func() {
		<-ctx.Done()
		s.conn.Close()
	}()

	return s.controlLoop(ctx)
}

// publishTunnels asks the server to publish every enabled tunnel.
func (s *session) publishTunnels() error {
	enabled := s.client.cfg.EnabledTunnels()
	if len(enabled) == 0 {
		s.logger.Warn("no tunnels are enabled")
	}

	for _, tunnel := range enabled {
		s.tunnelsMu.Lock()
		s.tunnels[tunnel.Name] = tunnel
		s.tunnelsMu.Unlock()

		spec := protocol.ProxySpec{
			Name:       tunnel.Name,
			Kind:       string(tunnel.Type),
			RemotePort: tunnel.RemotePort,
			Domains:    tunnel.Domains,
			TLSMode:    string(tunnel.TLSMode),
			SecretKey:  tunnel.SecretKey,
		}
		if err := s.codec.Write(&protocol.NewProxy{Proxy: spec}); err != nil {
			return fmt.Errorf("client: publish tunnel %q: %w", tunnel.Name, err)
		}
	}
	return nil
}

// heartbeat keeps the control connection alive and detects a server that has
// gone away without closing the socket.
func (s *session) heartbeat(ctx context.Context) {
	interval := s.client.cfg.Transport.HeartbeatInterval.D()
	if interval <= 0 {
		return
	}

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := s.codec.Write(&protocol.Ping{Timestamp: time.Now().Unix()}); err != nil {
				s.logger.Debug("heartbeat failed", "error", err)
				s.conn.Close()
				return
			}
		}
	}
}

// controlLoop services server messages until the connection ends.
func (s *session) controlLoop(ctx context.Context) error {
	timeout := s.client.cfg.Transport.HeartbeatTimeout.D()

	for {
		// A silent server is indistinguishable from a black-holed connection,
		// so bound how long we will wait between messages. The server answers
		// every ping, which guarantees traffic well inside this window.
		if timeout > 0 {
			if err := s.conn.SetReadDeadline(time.Now().Add(timeout)); err != nil {
				return err
			}
		}

		msg, err := s.codec.Read()
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			if errors.Is(err, io.EOF) {
				return nil
			}
			if errors.Is(err, protocol.ErrUnknownType) {
				s.logger.Debug("ignoring unknown message", "error", err)
				continue
			}
			return err
		}

		switch m := msg.(type) {
		case *protocol.Pong:
			// Liveness confirmed; the deadline above is refreshed on the next
			// loop iteration.

		case *protocol.ReqWorkConn:
			count := max(m.Count, 1)
			for range count {
				s.wg.Add(1)
				go func() {
					defer s.wg.Done()
					s.runWorkConn(ctx)
				}()
			}

		case *protocol.NewProxyResp:
			if m.Error != "" {
				s.logger.Error("server rejected tunnel", "tunnel", m.Name, "error", m.Error)
				continue
			}
			s.logger.Info("tunnel published", "tunnel", m.Name, "remote_port", m.RemotePort)

		case *protocol.CertPushResp:
			if m.Error != "" {
				s.logger.Warn("certificate push rejected", "error", m.Error)
			}

		default:
			s.logger.Debug("unexpected control message", "type", msg.Type())
		}
	}
}

// tunnel looks up a tunnel by proxy name.
func (s *session) tunnel(name string) (config.Tunnel, bool) {
	s.tunnelsMu.RLock()
	defer s.tunnelsMu.RUnlock()

	t, ok := s.tunnels[name]
	return t, ok
}
