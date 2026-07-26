package client

import (
	"context"
	"errors"
	"io"
	"net"
	"time"

	"github.com/zoefix/openfrp/internal/tunnel/protocol"
)

// runWorkConn dials one work connection, waits to be assigned a proxy, then
// forwards until the transfer ends.
//
// Each work connection is its own TCP connection. That is the deliberate
// difference from frp's default: an independent congestion window per tunnel,
// no head-of-line blocking between tunnels, and a bare socket at both ends so
// the relay can be handed to the kernel.
func (s *session) runWorkConn(ctx context.Context) {
	conn, err := s.client.dialer.DialWork(ctx)
	if err != nil {
		if ctx.Err() == nil {
			s.logger.Warn("dial work connection", "error", err)
		}
		return
	}
	defer conn.Close()

	timestamp := time.Now().Unix()
	if err := protocol.WriteMessage(conn, &protocol.NewWorkConn{
		RunID:     s.runID,
		Timestamp: timestamp,
		AuthKey:   protocol.AuthKey(s.client.cfg.Token, timestamp),
	}); err != nil {
		s.logger.Warn("register work connection", "error", err)
		return
	}

	// Close the connection when the session ends so this read unblocks.
	stop := make(chan struct{})
	defer close(stop)
	go func() {
		select {
		case <-ctx.Done():
			conn.Close()
		case <-stop:
		}
	}()

	// This connection now sits idle in the server's warm pool until a user
	// arrives, which may be hours. No read deadline, by design.
	//
	// ReadMessage rather than a Codec: the very next byte after this frame is
	// tunnel payload, and a buffered reader would consume part of it.
	msg, err := protocol.ReadMessage(conn)
	if err != nil {
		if ctx.Err() == nil && !errors.Is(err, io.EOF) && !errors.Is(err, net.ErrClosed) {
			s.logger.Debug("work connection ended before use", "error", err)
		}
		return
	}

	start, ok := msg.(*protocol.StartWorkConn)
	if !ok {
		s.logger.Warn("unexpected message on work connection", "type", msg.Type())
		return
	}
	if start.Error != "" {
		s.logger.Warn("server reported work connection error", "error", start.Error)
		return
	}

	s.forward(ctx, conn, start)
}
