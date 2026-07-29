package client

import (
	"context"
	"errors"
	"io"
	"net"
	"time"

	"github.com/zoefix/openfrp/internal/tunnel/protocol"
)

const maxConcurrentDials = 16

func (s *session) runWorkConn(ctx context.Context) {

	select {
	case s.dialSlots <- struct{}{}:
	case <-ctx.Done():
		return
	}

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

	stop := make(chan struct{})
	defer close(stop)
	go func() {
		select {
		case <-ctx.Done():
			conn.Close()
		case <-stop:
		}
	}()

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
