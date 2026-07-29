package transport

import (
	"context"
	"fmt"
	"io"
	"net"
	"time"

	"github.com/hashicorp/yamux"
)

// MuxConfig tunes a yamux session.
type MuxConfig struct {
	// StreamWindow is the per-stream flow-control window in bytes.
	//
	// This is the single most important knob on the multiplexed path and the
	// one frp leaves at yamux's 256 KiB default. A stream cannot exceed
	// window/RTT, so 256 KiB caps a stream at roughly 2.5 MB/s over a 100ms
	// path no matter how much bandwidth is available. 8 MiB moves that ceiling
	// to about 80 MB/s.
	StreamWindow int

	// KeepAliveInterval probes a session that has gone quiet.
	KeepAliveInterval time.Duration

	// WriteTimeout bounds a blocked write before the session is torn down.
	WriteTimeout time.Duration
}

// DefaultMuxConfig returns the tuning applied when a caller opts into
// multiplexing without specifying details.
func DefaultMuxConfig() MuxConfig {
	return MuxConfig{
		StreamWindow:      8 << 20,
		KeepAliveInterval: 30 * time.Second,
		WriteTimeout:      10 * time.Second,
	}
}

// CarrierMuxConfig tunes a session that carries overflow streams.
//
// The window is a fraction of the default and that is the whole point. Eight
// megabytes per stream is right when a session holds a handful of long-lived
// transfers; a carrier holds one short stream per visitor who arrived while
// the warm pool was empty, and there can be hundreds at once. At the default,
// 256 concurrent streams reserve two gigabytes — more than the memory of the
// server this was measured on, so a burst would not have been slow, it would
// have been an out-of-memory kill.
//
// 512 KiB still allows about 10 MB/s on a stream over a 50 ms path, far above
// what the requests this path exists for will ever ask for, and bounds the
// same 256 streams at 128 MiB.
//
// The keepalive is shorter than the default because the carrier is the thing
// standing between a burst and a stall: noticing it has died in ten seconds
// rather than thirty is three times less of a window in which the fallback
// silently is not there.
func CarrierMuxConfig() MuxConfig {
	return MuxConfig{
		StreamWindow:      512 << 10,
		KeepAliveInterval: 10 * time.Second,
		WriteTimeout:      10 * time.Second,
	}
}

func (c MuxConfig) toYamux() *yamux.Config {
	cfg := yamux.DefaultConfig()

	if c.StreamWindow > 0 {
		cfg.MaxStreamWindowSize = uint32(c.StreamWindow)
	}
	if c.KeepAliveInterval > 0 {
		cfg.EnableKeepAlive = true
		cfg.KeepAliveInterval = c.KeepAliveInterval
	} else {
		cfg.EnableKeepAlive = false
	}
	if c.WriteTimeout > 0 {
		cfg.ConnectionWriteTimeout = c.WriteTimeout
	}

	// yamux logs to stderr by default, which would interleave with our own
	// structured output. Errors surface through the returned values instead.
	cfg.LogOutput = io.Discard

	return cfg
}

// muxSource opens yamux streams over one shared connection.
type muxSource struct {
	session *yamux.Session
}

// NewMuxSource wraps conn in a yamux client session.
//
// Prefer NewDirectSource. Everything opened here shares a single congestion
// window and retransmission queue, so one lost packet stalls every tunnel at
// once, and no stream can ever be spliced.
func NewMuxSource(conn net.Conn, cfg MuxConfig) (StreamSource, error) {
	session, err := yamux.Client(conn, cfg.toYamux())
	if err != nil {
		return nil, fmt.Errorf("transport: start mux client: %w", err)
	}
	return &muxSource{session: session}, nil
}

func (s *muxSource) Open(ctx context.Context) (net.Conn, error) {
	// yamux has no context-aware open, so race it against cancellation and
	// discard the stream if the caller gave up first.
	type result struct {
		stream *yamux.Stream
		err    error
	}
	done := make(chan result, 1)
	go func() {
		stream, err := s.session.OpenStream()
		done <- result{stream, err}
	}()

	select {
	case <-ctx.Done():
		go func() {
			if res := <-done; res.err == nil {
				res.stream.Close()
			}
		}()
		return nil, ctx.Err()
	case res := <-done:
		if res.err != nil {
			return nil, fmt.Errorf("transport: open mux stream: %w", res.err)
		}
		return res.stream, nil
	}
}

func (s *muxSource) Close() error { return s.session.Close() }

func (s *muxSource) Multiplexed() bool { return true }

// muxAcceptor is the server-side mirror of muxSource.
type muxAcceptor struct {
	session *yamux.Session
}

// NewMuxAcceptor wraps conn in a yamux server session.
func NewMuxAcceptor(conn net.Conn, cfg MuxConfig) (StreamAcceptor, error) {
	session, err := yamux.Server(conn, cfg.toYamux())
	if err != nil {
		return nil, fmt.Errorf("transport: start mux server: %w", err)
	}
	return &muxAcceptor{session: session}, nil
}

func (a *muxAcceptor) Accept() (net.Conn, error) {
	stream, err := a.session.AcceptStream()
	if err != nil {
		return nil, err
	}
	return stream, nil
}

func (a *muxAcceptor) Close() error { return a.session.Close() }

func (a *muxAcceptor) Multiplexed() bool { return true }

// Confirm proves the peer is really speaking this protocol, by making it
// answer a round trip.
//
// Starting a session does not: the handshake is lazy, so wrapping a
// connection whose far end has no idea what a multiplexer is succeeds
// immediately and fails only later, when a stream is finally wanted. A caller
// that announced success on that basis would be reporting a working fallback
// path that does not exist — and would keep offering it to a peer that will
// never accept one.
func (a *muxAcceptor) Confirm() error {
	_, err := a.session.Ping()
	return err
}

// Confirmer is implemented by acceptors that can prove the far end agrees.
type Confirmer interface {
	Confirm() error
}

var _ Confirmer = (*muxAcceptor)(nil)
