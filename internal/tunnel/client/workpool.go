package client

import (
	"context"
	"errors"
	"io"
	"net"
	"time"

	"github.com/zoefix/openfrp/internal/tunnel/protocol"
)

// maxConcurrentDials bounds how many work connections this client will be
// building at once.
//
// The number is not about our own capacity — goroutines and sockets are cheap
// — but about what sits between this client and the server. A home router
// reaches the internet through NAT, and often through a transparent proxy as
// well; both are connection-table-bound and neither degrades gracefully. A
// burst of visitors otherwise produces a burst of simultaneous dials into
// that middlebox, which then delays or drops the very traffic this client
// needs to stay alive, including its own control connection.
//
// Losing the control connection is what makes this worth bounding. It does
// not fail one tunnel: it ends the session, and every other tunnel this
// client publishes goes down with it. That was observed — load against one
// tunnel took an unrelated tunnel on a different server offline, because both
// were served by the same client process.
//
// Queuing behind this limit costs a visitor some latency. Not queuing costs
// every visitor of every tunnel their connection.
const maxConcurrentDials = 16

// runWorkConn dials one work connection, waits to be assigned a proxy, then
// forwards until the transfer ends.
//
// Each work connection is its own TCP connection. That is the deliberate
// difference from frp's default: an independent congestion window per tunnel,
// no head-of-line blocking between tunnels, and a bare socket at both ends so
// the relay can be handed to the kernel.
func (s *session) runWorkConn(ctx context.Context) {
	// Held only for the dial. Once the connection is up it parks in the
	// server's pool for as long as nobody visits, and holding a slot for that
	// would confuse "how many am I building" with "how many do I have".
	select {
	case s.dialSlots <- struct{}{}:
	case <-ctx.Done():
		return
	}

	// Greeting and registration in one write. This runs once per visitor
	// connection, because each one consumes a work connection, and the write
	// syscall is the largest single line in this data plane's CPU profile.
	timestamp := time.Now().Unix()
	conn, err := s.client.dialer.DialWorkWith(ctx, &protocol.NewWorkConn{
		RunID:     s.runID,
		Timestamp: timestamp,
		AuthKey:   protocol.AuthKey(s.client.cfg.Token, timestamp),
	})
	<-s.dialSlots

	if err != nil {
		if ctx.Err() == nil {
			s.logger.Warn("dial work connection", "error", err)
		}
		return
	}
	defer conn.Close()

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
