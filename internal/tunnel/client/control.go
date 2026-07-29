package client

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/zoefix/openfrp/internal/config"
	"github.com/zoefix/openfrp/internal/tunnel/protocol"
)

type session struct {
	client *Client
	conn   net.Conn
	codec  *protocol.Codec
	logger *slog.Logger

	runID         string
	serverVersion string

	observedAddr string

	tunnelsMu sync.RWMutex
	tunnels   map[string]config.Tunnel
	targets   map[string]string

	retries retryCounter

	dialSlots chan struct{}

	wg sync.WaitGroup
}

func (s *session) serve(ctx context.Context) error {
	ctx, cancel := context.WithCancel(ctx)

	defer s.wg.Wait()
	defer cancel()

	s.tunnels = make(map[string]config.Tunnel)
	s.targets = make(map[string]string)
	if err := s.publishTunnels(); err != nil {
		return err
	}

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

	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		s.runOverflowCarriers(ctx)
	}()

	go func() {
		<-ctx.Done()
		s.conn.Close()
	}()

	return s.controlLoop(ctx)
}

func (s *session) publishTunnels() error {
	enabled := s.client.cfg.EnabledTunnels()
	if len(enabled) == 0 {
		s.logger.Warn("no tunnels are enabled")
	}

	for _, tunnel := range enabled {
		if err := s.publishTunnel(tunnel); err != nil {
			return err
		}
	}
	return nil
}

func (s *session) publishTunnel(tunnel config.Tunnel) error {
	s.tunnelsMu.Lock()

	if s.tunnels == nil {
		s.tunnels = make(map[string]config.Tunnel)
	}
	if s.targets == nil {
		s.targets = make(map[string]string)
	}
	s.tunnels[tunnel.Name] = tunnel
	s.targets[tunnel.Name] = net.JoinHostPort(
		tunnel.LocalIP, strconv.Itoa(tunnel.LocalPort))
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
	return nil
}

const (
	republishAttempts = 8
	republishDelay    = 250 * time.Millisecond
)

func (s *session) rejected(ctx context.Context, resp *protocol.NewProxyResp) {
	if !strings.Contains(resp.Error, "already routed") &&
		!strings.Contains(resp.Error, "already registered") {
		s.logger.Error("server rejected tunnel", "tunnel", resp.Name, "error", resp.Error)
		return
	}

	tunnel, ok := s.tunnel(resp.Name)
	if !ok {
		s.logger.Error("server rejected an unknown tunnel",
			"tunnel", resp.Name, "error", resp.Error)
		return
	}

	attempt := s.retries.next(resp.Name)
	if attempt > republishAttempts {
		s.logger.Error("tunnel is still claimed by another client",
			"tunnel", resp.Name, "error", resp.Error)
		return
	}

	s.logger.Info("tunnel is still held by a previous session, retrying",
		"tunnel", resp.Name, "attempt", attempt)

	s.wg.Add(1)
	go func() {
		defer s.wg.Done()

		timer := time.NewTimer(republishDelay)
		defer timer.Stop()

		select {
		case <-ctx.Done():
			return
		case <-timer.C:
		}

		if err := s.publishTunnel(tunnel); err != nil {
			s.logger.Debug("republish", "tunnel", tunnel.Name, "error", err)
		}
	}()
}

type retryCounter struct {
	mu     sync.Mutex
	counts map[string]int
}

func (r *retryCounter) next(name string) int {
	r.mu.Lock()
	defer r.mu.Unlock()

	if r.counts == nil {
		r.counts = map[string]int{}
	}
	r.counts[name]++
	return r.counts[name]
}

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

func (s *session) controlLoop(ctx context.Context) error {
	timeout := s.client.cfg.Transport.HeartbeatTimeout.D()

	for {

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

		case *protocol.ReqWorkConn:

			count := min(max(m.Count, 1), 4*config.DefaultMaxPoolCount)
			for range count {
				s.wg.Add(1)
				go func() {
					defer s.wg.Done()
					s.runWorkConn(ctx)
				}()
			}

		case *protocol.NewProxyResp:
			if m.Error != "" {
				s.rejected(ctx, m)
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

func (s *session) tunnel(name string) (config.Tunnel, bool) {
	s.tunnelsMu.RLock()
	defer s.tunnelsMu.RUnlock()

	t, ok := s.tunnels[name]
	return t, ok
}

func (s *session) target(name string) string {
	s.tunnelsMu.RLock()
	defer s.tunnelsMu.RUnlock()
	return s.targets[name]
}
