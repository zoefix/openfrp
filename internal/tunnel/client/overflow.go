package client

import (
	"context"
	"errors"
	"net"
	"time"

	"github.com/zoefix/openfrp/internal/tunnel/protocol"
	"github.com/zoefix/openfrp/internal/tunnel/transport"
)

const (
	carrierRetryMin = time.Second
	carrierRetryMax = 30 * time.Second
)

var errUnsupportedCarrier = errors.New("client: server does not accept an overflow carrier")

const overflowCarriers = 4

func (s *session) runOverflowCarriers(ctx context.Context) {
	for range overflowCarriers {
		s.wg.Add(1)
		go func() {
			defer s.wg.Done()
			s.runOverflowCarrier(ctx)
		}()
	}
}

const carrierRefusalsBeforeGivingUp = 3

func (s *session) runOverflowCarrier(ctx context.Context) {
	delay := carrierRetryMin
	var refusals int

	for ctx.Err() == nil {
		started := time.Now()

		err := s.serveCarrier(ctx)
		switch {
		case errors.Is(err, errUnsupportedCarrier):
			refusals++
			if refusals >= carrierRefusalsBeforeGivingUp {
				s.logger.Info("server does not accept an overflow carrier; " +
					"an empty pool will wait for a fresh connection instead")
				return
			}
		default:

			refusals = 0
		}
		if err != nil && ctx.Err() == nil {
			s.logger.Debug("overflow carrier ended", "error", err)
		}

		if time.Since(started) > time.Minute {
			delay = carrierRetryMin
		}

		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
		delay = min(delay*2, carrierRetryMax)
	}
}

func (s *session) serveCarrier(ctx context.Context) error {
	timestamp := time.Now().Unix()
	conn, err := s.client.dialer.DialWorkWith(ctx, &protocol.NewMuxConn{
		RunID:     s.runID,
		Timestamp: timestamp,
		AuthKey:   protocol.AuthKey(s.client.cfg.Token, timestamp),
	})
	if err != nil {
		return err
	}
	defer conn.Close()

	acceptor, err := transport.NewMuxAcceptor(conn, transport.CarrierMuxConfig())
	if err != nil {
		return err
	}
	defer acceptor.Close()

	if confirmer, ok := acceptor.(transport.Confirmer); ok {
		if err := confirmer.Confirm(); err != nil {
			return errUnsupportedCarrier
		}
	}

	stop := make(chan struct{})
	defer close(stop)
	go func() {
		select {
		case <-ctx.Done():
			acceptor.Close()
		case <-stop:
		}
	}()

	s.logger.Info("overflow carrier established")

	for {
		stream, err := acceptor.Accept()
		if err != nil {
			if ctx.Err() != nil || errors.Is(err, net.ErrClosed) {
				return nil
			}
			return err
		}

		s.wg.Add(1)
		go func() {
			defer s.wg.Done()
			defer stream.Close()
			s.serveStream(ctx, stream)
		}()
	}
}

func (s *session) serveStream(ctx context.Context, stream net.Conn) {

	if err := stream.SetReadDeadline(time.Now().Add(carrierRetryMax)); err != nil {
		return
	}

	msg, err := protocol.ReadMessage(stream)
	if err != nil {
		s.logger.Debug("overflow stream ended before it was assigned", "error", err)
		return
	}

	start, ok := msg.(*protocol.StartWorkConn)
	if !ok {
		s.logger.Warn("unexpected message on an overflow stream", "type", msg.Type())
		return
	}
	if start.Error != "" {
		s.logger.Warn("server reported an overflow stream error", "error", start.Error)
		return
	}

	if err := stream.SetReadDeadline(time.Time{}); err != nil {
		return
	}

	s.forward(ctx, stream, start)
}
