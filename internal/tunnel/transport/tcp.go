package transport

import (
	"context"
	"crypto/tls"
	"fmt"
	"net"
	"syscall"
	"time"

	"github.com/zoefix/openfrp/internal/tunnel/protocol"
	"github.com/zoefix/openfrp/pkg/netutil"
)

type Dialer struct {
	Addr string

	TLSConfig *tls.Config

	Timeout time.Duration

	TCPOptions netutil.TCPOptions

	SocketGID  int
	SocketMark int
}

func (d *Dialer) DialControl(ctx context.Context) (net.Conn, error) {
	conn, err := d.dial(ctx)
	if err != nil {
		return nil, err
	}

	if d.TLSConfig != nil {
		tlsConn := tls.Client(conn, d.TLSConfig)
		if err := tlsConn.HandshakeContext(ctx); err != nil {
			conn.Close()
			return nil, fmt.Errorf("transport: TLS handshake with %s: %w", d.Addr, err)
		}
		conn = tlsConn
	}

	if err := d.greet(conn, protocol.ModePlain); err != nil {
		conn.Close()
		return nil, err
	}
	return conn, nil
}

func (d *Dialer) DialWork(ctx context.Context) (net.Conn, error) {
	conn, err := d.dial(ctx)
	if err != nil {
		return nil, err
	}
	if err := d.greet(conn, protocol.ModePlain); err != nil {
		conn.Close()
		return nil, err
	}
	return conn, nil
}

func (d *Dialer) DialWorkWith(ctx context.Context, m protocol.Message) (net.Conn, error) {
	conn, err := d.dial(ctx)
	if err != nil {
		return nil, err
	}
	if err := protocol.WriteGreeting(conn, protocol.Preamble{
		Version: protocol.Version,
		Mode:    protocol.ModePlain,
	}, m); err != nil {
		conn.Close()
		return nil, err
	}
	return conn, nil
}

func (d *Dialer) DialMux(ctx context.Context) (net.Conn, error) {
	conn, err := d.dial(ctx)
	if err != nil {
		return nil, err
	}
	if err := d.greet(conn, protocol.ModeMux); err != nil {
		conn.Close()
		return nil, err
	}
	return conn, nil
}

func (d *Dialer) dial(ctx context.Context) (net.Conn, error) {
	timeout := d.Timeout
	if timeout <= 0 {
		timeout = 10 * time.Second
	}

	dialer := &net.Dialer{Timeout: timeout}
	if d.SocketMark > 0 {
		dialer.Control = func(_, _ string, rc syscall.RawConn) error {
			var markErr error
			if err := rc.Control(func(fd uintptr) {
				markErr = netutil.SetSocketMark(fd, d.SocketMark)
			}); err != nil {
				return err
			}
			return markErr
		}
	}

	conn, err := netutil.DialWithSocketGID(d.SocketGID, func() (net.Conn, error) {
		return dialer.DialContext(ctx, "tcp", d.Addr)
	})
	if err != nil {
		return nil, fmt.Errorf("transport: dial %s: %w", d.Addr, err)
	}

	if err := netutil.TuneConn(conn, d.TCPOptions); err != nil {
		conn.Close()
		return nil, fmt.Errorf("transport: tune %s: %w", d.Addr, err)
	}
	return conn, nil
}

func (d *Dialer) greet(conn net.Conn, mode protocol.Mode) error {
	return protocol.WritePreamble(conn, protocol.Preamble{
		Version: protocol.Version,
		Mode:    mode,
	})
}

type directSource struct {
	dialer *Dialer
}

func NewDirectSource(d *Dialer) StreamSource { return &directSource{dialer: d} }

func (s *directSource) Open(ctx context.Context) (net.Conn, error) {
	return s.dialer.DialWork(ctx)
}

func (s *directSource) Close() error { return nil }

func (s *directSource) Multiplexed() bool { return false }
