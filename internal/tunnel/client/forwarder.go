package client

import (
	"context"
	"log/slog"
	"net"
	"strconv"
	"time"

	"github.com/zoefix/openfrp/internal/config"
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

	// Count the connection from here, where the tunnel is known, and release
	// it on every exit path below. Opening later would leave a failed dial
	// uncounted; releasing anywhere but a defer would leak the active count on
	// the error returns.
	s.client.traffic.Open(start.ProxyName)
	defer s.client.traffic.Close(start.ProxyName)

	// UDP is framed rather than streamed, so it takes a different path. It
	// accounts per packet rather than returning a total: it has several exit
	// paths and a reply goroutine that outlives the call.
	if tunnel.Type == config.TunnelUDP {
		s.forwardUDP(ctx, workConn, tunnel, start.ProxyName, logger)
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

	// Announce the visitor before any payload. Without this the local service
	// sees the connection coming from this router and logs every visitor as
	// the same address.
	//
	// Written straight to the socket rather than through a wrapper, so the
	// relay below still hands the kernel a raw *net.TCPConn and keeps the
	// splice path. The header costs one small write per connection.
	if tunnel.ProxyProtocol != "" {
		source, err := netutil.ParseProxyAddr(start.SourceAddr)
		if err != nil {
			logger.Warn("cannot announce the visitor address",
				"source", start.SourceAddr, "error", err)
			return
		}
		if err := netutil.WriteProxyHeader(localConn, tunnel.ProxyProtocol,
			source, localConn.RemoteAddr()); err != nil {
			logger.Warn("announce visitor address", "error", err)
			return
		}
	}

	// AToB is work connection to local service: traffic arriving from the
	// internet. BToA is the reply. Naming them from the tunnel's point of view
	// keeps "in" meaning the same thing on both ends.
	transferred := netutil.Relay(workConn, localConn)

	s.client.traffic.RecordTransfer(start.ProxyName,
		transferred.AToB, transferred.BToA, transferred.Spliced)

	// Guarded: per-connection, and the attribute boxing allocates even with
	// debug logging off.
	if logger.Enabled(ctx, slog.LevelDebug) {
		logger.Debug("transfer complete",
			"target", target,
			"to_local", transferred.AToB,
			"to_remote", transferred.BToA,
			"spliced", transferred.Spliced)
	}
}
