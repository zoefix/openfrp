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

const (
	minReconnectDelay = 500 * time.Millisecond
	maxReconnectDelay = 30 * time.Second
)

type Client struct {
	cfg     *config.Client
	logger  *slog.Logger
	version string

	dialer *transport.Dialer

	mu    sync.Mutex
	runID string

	connected     bool
	serverVersion string

	serverStates func() map[string]ServerSnapshot

	certs CertSource

	pushedCerts pushed

	traffic *stats.Registry

	statsPath string
}

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
			SocketGID:  cfg.SocketGID,
			SocketMark: cfg.SocketMark,
		},
	}, nil
}

func (c *Client) Run(ctx context.Context) error {
	if c.cfg.Transport.Mux {
		c.logger.Warn("multiplexing is enabled: every tunnel will share one " +
			"congestion window and none can use the kernel zero-copy path")
	}

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

func jitter(d time.Duration) time.Duration {
	half := d / 2
	return half + time.Duration(rand.Int64N(int64(half)+1))
}

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
		"server_version", session.serverVersion,
		"seen_by_server_as", session.observedAddr)

	c.reportEgress(session.observedAddr)

	return session.serve(ctx)
}

func (c *Client) ServerState() (version string, connected bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.serverVersion, c.connected
}

func (c *Client) login(ctx context.Context, conn net.Conn) (*session, error) {

	deadline := c.cfg.Transport.DialTimeout.D()
	if deadline <= 0 {
		deadline = 30 * time.Second
	}
	if err := conn.SetDeadline(time.Now().Add(deadline)); err != nil {
		return nil, fmt.Errorf("client: login: %w", err)
	}

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
		DownRate:   c.cfg.DownRate,
		UpRate:     c.cfg.UpRate,
		Quota:      c.cfg.Quota,
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
		observedAddr:  resp.ObservedAddr,
		logger:        c.logger.With("run_id", resp.RunID),
		dialSlots:     make(chan struct{}, maxConcurrentDials),
	}, nil
}

func (c *Client) RunID() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.runID
}
