package client

import (
	"context"
	"log/slog"
	"net"
	"time"

	"github.com/zoefix/openfrp/internal/config"
	"github.com/zoefix/openfrp/internal/tunnel/protocol"
	"github.com/zoefix/openfrp/pkg/netutil"
)

const localDialTimeout = 10 * time.Second

func (s *session) forward(ctx context.Context, workConn net.Conn, start *protocol.StartWorkConn) {

	var built *slog.Logger
	logger := func() *slog.Logger {
		if built == nil {
			built = s.logger.With("tunnel", start.ProxyName, "source", start.SourceAddr)
		}
		return built
	}

	tunnel, ok := s.tunnel(start.ProxyName)
	if !ok {
		logger().Warn("work connection assigned to an unknown tunnel")
		return
	}

	s.client.traffic.Open(start.ProxyName)
	defer s.client.traffic.Close(start.ProxyName)

	if tunnel.Type == config.TunnelUDP {
		s.forwardUDP(ctx, workConn, tunnel, start.ProxyName, logger())
		return
	}

	target := s.target(start.ProxyName)

	dialer := &net.Dialer{Timeout: localDialTimeout}
	localConn, err := dialer.DialContext(ctx, "tcp", target)
	if err != nil {
		logger().Warn("dial local service", "target", target, "error", err)
		return
	}
	defer localConn.Close()

	if err := netutil.TuneConn(localConn, netutil.TCPOptions{NoDelay: true}); err != nil {
		logger().Debug("tune local connection", "error", err)
	}

	if tunnel.ProxyProtocol != "" {
		source, err := netutil.ParseProxyAddr(start.SourceAddr)
		if err != nil {
			logger().Warn("cannot announce the visitor address",
				"source", start.SourceAddr, "error", err)
			return
		}
		if err := netutil.WriteProxyHeader(localConn, tunnel.ProxyProtocol,
			source, localConn.RemoteAddr()); err != nil {
			logger().Warn("announce visitor address", "error", err)
			return
		}
	}

	traffic := s.client.traffic
	transferred := netutil.RelayWith(workConn, localConn, netutil.RelayOptions{
		Progress: func(toLocal, toRemote int64) {
			traffic.RecordProgress(start.ProxyName, toLocal, toRemote)
		},
	})

	traffic.RecordClose(start.ProxyName, transferred.Spliced)

	if s.logger.Enabled(ctx, slog.LevelDebug) {
		logger().Debug("transfer complete",
			"target", target,
			"to_local", transferred.AToB,
			"to_remote", transferred.BToA,
			"spliced", transferred.Spliced)
	}
}
