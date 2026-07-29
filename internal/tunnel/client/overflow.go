package client

import (
	"context"
	"errors"
	"net"
	"time"

	"github.com/zoefix/openfrp/internal/tunnel/protocol"
	"github.com/zoefix/openfrp/internal/tunnel/transport"
)

// The overflow carrier is one connection, held open for the life of the
// session, on which the server opens a multiplexed stream whenever its warm
// pool is empty.
//
// It exists because of what the alternative costs. A visitor arriving with
// nothing warm to serve them waits for the server to ask, this client to dial
// across the internet, and the connection to be registered — roughly two
// round trips before their request has moved at all. Worse, that dial lands
// on whatever NAT or transparent proxy this client reaches the internet
// through, and under a burst those are what run out first: not our sockets,
// not our CPU, but somebody's connection table.
//
// A stream on the carrier costs none of that. No handshake, no round trip,
// no new entry anywhere.
//
// What it gives up is real, which is why it is the second choice and not the
// first. Streams share their carrier's congestion window, so a lost packet
// stalls all of them, and a stream is not a *net.TCPConn so it cannot be
// spliced — every byte goes through userspace. Both costs scale with transfer
// size, and the case this serves is the small, bursty one. The direct pool
// remains the default and the fast path; this is what happens instead of
// stalling.
const (
	// carrierRetryMin and carrierRetryMax bound the redial backoff. The
	// carrier is a fallback, so failing to hold one is not urgent: the
	// session still works, it just loses the relief valve until this
	// succeeds.
	carrierRetryMin = time.Second
	carrierRetryMax = 30 * time.Second
)

// errUnsupportedCarrier means the server did not answer the multiplexer, so
// it predates carriers and never will. Retrying it would be a connection
// attempt every thirty seconds for the life of the session, achieving
// nothing — and fleets run mixed versions for as long as an upgrade takes,
// so this is the ordinary case rather than a corner one.
var errUnsupportedCarrier = errors.New("client: server does not accept an overflow carrier")

// runOverflowCarrier keeps one carrier connection up until ctx is cancelled.
func (s *session) runOverflowCarrier(ctx context.Context) {
	delay := carrierRetryMin

	for ctx.Err() == nil {
		started := time.Now()

		err := s.serveCarrier(ctx)
		if errors.Is(err, errUnsupportedCarrier) {
			s.logger.Info("server does not accept an overflow carrier; " +
				"an empty pool will wait for a fresh connection instead")
			return
		}
		if err != nil && ctx.Err() == nil {
			s.logger.Debug("overflow carrier ended", "error", err)
		}

		// A carrier that stayed up is evidence the path is fine, so the next
		// failure starts from the floor rather than inheriting a long wait.
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

// serveCarrier dials one carrier, offers it, and serves streams until it ends.
func (s *session) serveCarrier(ctx context.Context) error {
	conn, err := s.client.dialer.DialWork(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()

	timestamp := time.Now().Unix()
	if err := protocol.WriteMessage(conn, &protocol.NewMuxConn{
		RunID:     s.runID,
		Timestamp: timestamp,
		AuthKey:   protocol.AuthKey(s.client.cfg.Token, timestamp),
	}); err != nil {
		return err
	}

	// This client accepts; the server opens. That is the reverse of every
	// other connection here, and it is the whole reason a multiplexer is
	// involved: the server cannot dial a client behind NAT, but it can open a
	// stream on a connection the client dialled.
	acceptor, err := transport.NewMuxAcceptor(conn, transport.DefaultMuxConfig())
	if err != nil {
		return err
	}
	defer acceptor.Close()

	// Make the server answer a round trip before believing in this. Starting
	// a session is lazy and succeeds against a peer that has never heard of a
	// carrier, so without this the client announces a fallback path that does
	// not exist and keeps offering it to a server that will never take one.
	if confirmer, ok := acceptor.(transport.Confirmer); ok {
		if err := confirmer.Confirm(); err != nil {
			return errUnsupportedCarrier
		}
	}

	// Close on cancellation so the blocking Accept below returns.
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

// serveStream handles one overflow stream, which carries exactly what a work
// connection carries: a StartWorkConn naming the proxy, then raw payload.
func (s *session) serveStream(ctx context.Context, stream net.Conn) {
	// Unlike a pooled work connection this is not parked waiting for a
	// visitor — the server opened it because one is already here — so a
	// deadline on the handshake costs nothing and bounds a stream that is
	// opened and then never spoken on.
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

	// The transfer that follows may be long lived.
	if err := stream.SetReadDeadline(time.Time{}); err != nil {
		return
	}

	s.forward(ctx, stream, start)
}
