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

// session is one live connection to the server.
type session struct {
	client *Client
	conn   net.Conn
	codec  *protocol.Codec
	logger *slog.Logger

	runID         string
	serverVersion string

	// tunnels maps a proxy name onto its local target, so a work connection
	// assigned to a proxy knows where to forward. targets holds the same
	// destinations already rendered as host:port, because that string is
	// needed once per visitor connection and never changes.
	tunnelsMu sync.RWMutex
	tunnels   map[string]config.Tunnel
	targets   map[string]string

	// retries bounds how long a tunnel keeps waiting for a previous session's
	// claim to be released.
	retries retryCounter

	// dialSlots bounds concurrent work-connection dials. See
	// maxConcurrentDials for why the limit protects the client rather than
	// the server.
	dialSlots chan struct{}

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
	s.targets = make(map[string]string)
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
		if err := s.publishTunnel(tunnel); err != nil {
			return err
		}
	}
	return nil
}

// publishTunnel asks the server to publish one tunnel.
func (s *session) publishTunnel(tunnel config.Tunnel) error {
	s.tunnelsMu.Lock()
	// Initialised here rather than only in serve, so a session assembled any
	// other way — the republish goroutine below reaching a session whose
	// serve has already returned, or a test building one directly — records
	// the tunnel instead of panicking on a nil map.
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

// republishAttempts and republishDelay bound the retry below.
//
// Two seconds of retrying covers the overlap; beyond that the conflict is with
// something that genuinely intends to keep the name.
const (
	republishAttempts = 8
	republishDelay    = 250 * time.Millisecond
)

// rejected handles a tunnel the server refused.
//
// A name or domain still held by a previous session is retried rather than
// given up on. Restarting the client overlaps the two processes — procd starts
// the new one before the old one has finished exiting — so the new session
// publishes while the server still holds the old session's routes, and reaps
// them a fraction of a second later. Failing there leaves the tunnel down
// until something else restarts it, which is a self-inflicted outage on every
// configuration change.
//
// Anything else is a real disagreement and is reported.
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

// retryCounter tracks republish attempts per tunnel for one session.
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
			// The server batches its requests, so Count above one is routine.
			// The ceiling guards against a corrupt or hostile value spawning
			// an unbounded number of dials; anything clamped away is simply
			// re-requested once the server notices its pool is still short.
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

// tunnel looks up a tunnel by proxy name.
func (s *session) tunnel(name string) (config.Tunnel, bool) {
	s.tunnelsMu.RLock()
	defer s.tunnelsMu.RUnlock()

	t, ok := s.tunnels[name]
	return t, ok
}

// target returns a tunnel's local destination as host:port, rendered when the
// tunnel was published rather than for every connection it carries.
func (s *session) target(name string) string {
	s.tunnelsMu.RLock()
	defer s.tunnelsMu.RUnlock()
	return s.targets[name]
}
