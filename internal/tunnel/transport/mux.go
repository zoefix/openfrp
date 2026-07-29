package transport

import (
	"context"
	"fmt"
	"io"
	"net"
	"time"

	"github.com/hashicorp/yamux"
)

type MuxConfig struct {
	StreamWindow int

	KeepAliveInterval time.Duration

	WriteTimeout time.Duration
}

func DefaultMuxConfig() MuxConfig {
	return MuxConfig{
		StreamWindow:      8 << 20,
		KeepAliveInterval: 30 * time.Second,
		WriteTimeout:      10 * time.Second,
	}
}

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

	cfg.LogOutput = io.Discard

	return cfg
}

type muxSource struct {
	session *yamux.Session
}

func NewMuxSource(conn net.Conn, cfg MuxConfig) (StreamSource, error) {
	session, err := yamux.Client(conn, cfg.toYamux())
	if err != nil {
		return nil, fmt.Errorf("transport: start mux client: %w", err)
	}
	return &muxSource{session: session}, nil
}

func (s *muxSource) Open(ctx context.Context) (net.Conn, error) {

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

type muxAcceptor struct {
	session *yamux.Session
}

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

func (a *muxAcceptor) Confirm() error {
	_, err := a.session.Ping()
	return err
}

type Confirmer interface {
	Confirm() error
}

var _ Confirmer = (*muxAcceptor)(nil)
