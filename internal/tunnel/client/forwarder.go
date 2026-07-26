package client

import (
	"context"
	"net"
	"strconv"
	"time"

	"github.com/zoefix/openfrp/internal/tunnel/protocol"
	"github.com/zoefix/openfrp/pkg/netutil"
)

// localDialTimeout bounds connecting to the LAN service. It is short because
// the target is on the local network: a slow dial means it is down, and the
// remote user is waiting.
const localDialTimeout = 10 * time.Second

// forward connects to the tunnel's local target and relays until either side
// closes.
func (s *session) forward(ctx context.Context, workConn net.Conn, start *protocol.StartWorkConn) {
	logger := s.logger.With("tunnel", start.ProxyName, "source", start.SourceAddr)

	tunnel, ok := s.tunnel(start.ProxyName)
	if !ok {
		logger.Warn("work connection assigned to an unknown tunnel")
		return
	}

	target := net.JoinHostPort(tunnel.LocalIP, strconv.Itoa(tunnel.LocalPort))

	dialer := &net.Dialer{Timeout: localDialTimeout}
	localConn, err := dialer.DialContext(ctx, "tcp", target)
	if err != nil {
		logger.Warn("dial local service", "target", target, "error", err)
		return
	}
	defer localConn.Close()

	if err := netutil.TuneConn(localConn, netutil.DefaultTCPOptions()); err != nil {
		logger.Debug("tune local connection", "error", err)
	}

	stats := netutil.Relay(workConn, localConn)

	logger.Debug("transfer complete",
		"target", target,
		"to_local", stats.AToB,
		"to_remote", stats.BToA,
		"spliced", stats.Spliced)
}
