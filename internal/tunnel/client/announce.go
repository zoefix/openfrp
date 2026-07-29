package client

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"strconv"
	"time"

	"github.com/zoefix/openfrp/internal/config"
	"github.com/zoefix/openfrp/internal/tunnel/protocol"
	"github.com/zoefix/openfrp/internal/tunnel/transport"
	"github.com/zoefix/openfrp/pkg/netutil"
)

func Announce(ctx context.Context, upstream config.Upstream, version string,
	message protocol.Message, want protocol.Type) (protocol.Message, error) {

	cfg := &config.Client{
		ServerAddr: upstream.Addr,
		ServerPort: upstream.Port,
		Token:      upstream.Token,
		Transport:  upstream.Transport,
	}
	cfg.ApplyDefaults()

	cfg.Transport.PoolCount = 1

	client := &Client{
		cfg:    cfg,
		logger: slog.New(slog.DiscardHandler),
		dialer: &transport.Dialer{
			Addr:       net.JoinHostPort(cfg.ServerAddr, strconv.Itoa(cfg.ServerPort)),
			Timeout:    cfg.Transport.DialTimeout.D(),
			TCPOptions: netutil.DefaultTCPOptions(),
		},
		version: version,
	}

	conn, err := client.dialer.DialControl(ctx)
	if err != nil {
		return nil, fmt.Errorf("client: reach %s: %w", client.dialer.Addr, err)
	}
	defer conn.Close()

	session, err := client.login(ctx, conn)
	if err != nil {
		return nil, err
	}

	deadline := cfg.Transport.DialTimeout.D()
	if deadline <= 0 {
		deadline = 30 * time.Second
	}
	if err := conn.SetDeadline(time.Now().Add(deadline)); err != nil {
		return nil, err
	}

	if err := session.codec.Write(message); err != nil {
		return nil, fmt.Errorf("client: send %s: %w", message.Type(), err)
	}

	for {
		reply, err := session.codec.Read()
		if err != nil {
			return nil, fmt.Errorf("client: %s: %w", message.Type(), err)
		}
		if reply.Type() == want {
			return reply, nil
		}
	}
}
