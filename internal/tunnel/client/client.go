// Package client implements the OpenFrp client daemon.
package client

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"net"
	"os"
	"runtime"
	"strconv"
	"sync"
	"time"

	"github.com/zoefix/openfrp/internal/config"
	"github.com/zoefix/openfrp/internal/stats"
	"github.com/zoefix/openfrp/internal/tunnel/protocol"
	"github.com/zoefix/openfrp/internal/tunnel/transport"
	"github.com/zoefix/openfrp/pkg/netutil"
)

// Reconnect backoff bounds. The jitter matters when a server restarts and
// every client it was serving tries to come back at the same instant.
const (
	minReconnectDelay = 500 * time.Millisecond
	maxReconnectDelay = 30 * time.Second
)

// Client maintains the control connection and the work connection pool.
type Client struct {
	cfg     *config.Client
	logger  *slog.Logger
	version string

	dialer *transport.Dialer

	// runID is assigned by the server on first login and reused on reconnect
	// so the server recognises us rather than counting a second client.
	mu    sync.Mutex
	runID string

	// connected and serverVersion describe the control connection, for the
	// status page. The version is whatever the server announced at login and
	// deliberately survives a disconnect: "last seen 0.2.0, not connected"
	// tells the operator more than a blank.
	connected     bool
	serverVersion string

	// serverStates, when set, contributes every server's state to the
	// published snapshot. The supervisor sets it on the one client that
	// publishes for all of them.
	serverStates func() map[string]ServerSnapshot

	// certs resolves the certificate a tunnel is bound to. Optional: without
	// it, tunnels terminating TLS rely on whatever the server already holds.
	certs CertSource

	// pushedCerts records what the current session has already sent, so the
	// renewal watcher only speaks when something actually changed.
	pushedCerts pushed

	// traffic accumulates per-tunnel byte counts. The client sees every byte
	// on its way to and from the local service, so it can account for them
	// without asking the server.
	traffic *stats.Registry

	// statsPath is where the snapshot is published for the status page.
	statsPath string
}

// New builds a client from cfg.
func New(cfg *config.Client, logger *slog.Logger, version string) (*Client, error) {
	if cfg == nil {
		return nil, errors.New("client: nil config")
	}
	if logger == nil {
		logger = slog.Default()
	}

	var tlsCfg *tls.Config
	if cfg.Transport.TLSEnable {
		tlsCfg = &tls.Config{
			ServerName: cfg.ServerAddr,
			MinVersion: tls.VersionTLS12,
		}
	}

	return &Client{
		cfg:       cfg,
		logger:    logger,
		version:   version,
		traffic:   stats.NewRegistry(),
		statsPath: DefaultStatsPath,
		dialer: &transport.Dialer{
			Addr:       net.JoinHostPort(cfg.ServerAddr, strconv.Itoa(cfg.ServerPort)),
			TLSConfig:  tlsCfg,
			Timeout:    cfg.Transport.DialTimeout.D(),
			TCPOptions: netutil.DefaultTCPOptions(),
		},
	}, nil
}

// Run connects and keeps reconnecting until ctx is cancelled.
func (c *Client) Run(ctx context.Context) error {
	if c.cfg.Transport.Mux {
		c.logger.Warn("multiplexing is enabled: every tunnel will share one " +
			"congestion window and none can use the kernel zero-copy path")
	}

	// Published for as long as the daemon runs, so the status page keeps
	// showing totals across a reconnect rather than blanking out.
	go c.publishTraffic(ctx)
	defer c.removeTraffic()

	delay := minReconnectDelay

	for {
		if ctx.Err() != nil {
			return nil
		}

		start := time.Now()
		err := c.runOnce(ctx)

		if ctx.Err() != nil {
			return nil
		}
		if err != nil {
			c.logger.Warn("connection lost", "error", err)
		} else {
			c.logger.Info("connection closed by server")
		}

		// A session that stayed up a while is evidence the server is healthy,
		// so start the next backoff from the floor rather than escalating.
		if time.Since(start) > time.Minute {
			delay = minReconnectDelay
		}

		wait := jitter(delay)
		c.logger.Info("reconnecting", "in", wait.Round(time.Millisecond))

		timer := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil
		case <-timer.C:
		}

		delay = min(delay*2, maxReconnectDelay)
	}
}

// jitter spreads reconnect attempts so a fleet does not stampede a recovering
// server. Full jitter: pick uniformly from [d/2, d].
func jitter(d time.Duration) time.Duration {
	half := d / 2
	return half + time.Duration(rand.Int64N(int64(half)+1))
}

// runOnce establishes one session and serves it until it ends.
func (c *Client) runOnce(ctx context.Context) error {
	conn, err := c.dialer.DialControl(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()

	session, err := c.login(ctx, conn)
	if err != nil {
		return err
	}

	c.mu.Lock()
	c.connected = true
	c.serverVersion = session.serverVersion
	c.mu.Unlock()
	defer func() {
		c.mu.Lock()
		c.connected = false
		c.mu.Unlock()
	}()

	c.logger.Info("connected",
		"server", c.dialer.Addr,
		"run_id", session.runID,
		"server_version", session.serverVersion)

	return session.serve(ctx)
}

// ServerState reports whether the control connection is up, and the version
// the server announced at the most recent login.
func (c *Client) ServerState() (version string, connected bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.serverVersion, c.connected
}

// login performs the handshake and returns a live session.
func (c *Client) login(ctx context.Context, conn net.Conn) (*session, error) {
	// Bound the whole exchange. Without this a server that completes the TCP
	// handshake and then says nothing — one shutting down, or a middlebox that
	// accepted on its behalf — leaves this read blocked forever. The client
	// stays alive with no connection, never reconnects and logs nothing, and
	// procd reports the service as healthy the entire time.
	//
	// The control loop has always had a deadline; login is the gap it was
	// missing, and it is the one place where the peer has not yet proved it is
	// the server at all.
	deadline := c.cfg.Transport.DialTimeout.D()
	if deadline <= 0 {
		deadline = 30 * time.Second
	}
	if err := conn.SetDeadline(time.Now().Add(deadline)); err != nil {
		return nil, fmt.Errorf("client: login: %w", err)
	}
	// Cleared before returning: the session that follows manages its own, and
	// leaving this one in place would kill an idle but healthy connection.
	defer conn.SetDeadline(time.Time{})

	timestamp := time.Now().Unix()

	hostname, _ := os.Hostname()

	c.mu.Lock()
	previousRunID := c.runID
	c.mu.Unlock()

	login := &protocol.Login{
		Version:    protocol.Version,
		ClientName: c.cfg.Name,
		Hostname:   hostname,
		OS:         runtime.GOOS,
		Arch:       runtime.GOARCH,
		Timestamp:  timestamp,
		AuthKey:    protocol.AuthKey(c.cfg.Token, timestamp),
		PoolCount:  c.cfg.Transport.PoolCount,
		RunID:      previousRunID,
	}

	codec := protocol.NewCodec(conn)
	if err := codec.Write(login); err != nil {
		return nil, err
	}

	msg, err := codec.ReadExpect(protocol.TypeLoginResp)
	if err != nil {
		return nil, fmt.Errorf("client: login: %w", err)
	}
	resp := msg.(*protocol.LoginResp)
	if resp.Error != "" {
		// A rejection with nothing to authenticate with is not a wrong token,
		// it is a server that was never finished being set up — most often a
		// deployment that failed after its address was saved but before its
		// token was written back. Reporting "authentication failed" sends the
		// operator hunting for a mismatch between two values when there is
		// only one, and it is empty.
		if c.cfg.Token == "" {
			return nil, fmt.Errorf("client: server rejected login and no token "+
				"is configured for it — finish the deployment or enter the "+
				"server's token: %s", resp.Error)
		}
		return nil, fmt.Errorf("client: server rejected login: %s", resp.Error)
	}

	c.mu.Lock()
	c.runID = resp.RunID
	c.mu.Unlock()

	return &session{
		client:        c,
		conn:          conn,
		codec:         codec,
		runID:         resp.RunID,
		serverVersion: resp.ServerVersion,
		logger:        c.logger.With("run_id", resp.RunID),
	}, nil
}

// RunID reports the identifier the server assigned, if connected.
func (c *Client) RunID() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.runID
}
