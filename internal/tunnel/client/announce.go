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

// Announce opens a control connection, sends one message, waits for the reply
// and disconnects.
//
// It exists because certificate issuance runs in its own process — the job
// worker's — which has no control connection of its own and must not borrow
// the daemon's. Publishing an ACME challenge is a handful of bytes and a
// second of connection, so a full client would be the wrong shape entirely.
//
// The session it opens publishes no tunnels, so it claims nothing and takes
// nothing away from the daemon's session.
func Announce(ctx context.Context, upstream config.Upstream, version string,
	message protocol.Message, want protocol.Type) (protocol.Message, error) {

	cfg := &config.Client{
		ServerAddr: upstream.Addr,
		ServerPort: upstream.Port,
		Token:      upstream.Token,
		Transport:  upstream.Transport,
	}
	cfg.ApplyDefaults()

	// The smallest pool the server will accept. This session serves no
	// tunnels, so every work connection it was asked for would be opened and
	// then abandoned.
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

	// Bounded like the login before it: the server is answering a single
	// message, and a silent one must not hold up an issuance.
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

	// Read past anything the server says on its own account.
	//
	// It asks for work connections as soon as a client logs in, so the very
	// next frame is a ReqWorkConn rather than the reply — insisting on the
	// expected type immediately made every publish fail with "unexpected
	// message". This session serves no tunnels, so those requests are
	// deliberately ignored rather than answered.
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
