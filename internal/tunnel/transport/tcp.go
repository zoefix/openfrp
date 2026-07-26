package transport

import (
	"context"
	"crypto/tls"
	"fmt"
	"net"
	"time"

	"github.com/zoefix/openfrp/internal/tunnel/protocol"
	"github.com/zoefix/openfrp/pkg/netutil"
)

// Dialer opens connections to the server with consistent socket tuning.
type Dialer struct {
	// Addr is the server's host:port.
	Addr string
	// TLSConfig protects the control connection. Nil disables TLS.
	TLSConfig *tls.Config
	// Timeout bounds a single dial.
	Timeout time.Duration
	// TCPOptions is applied to every connection before anything is written.
	TCPOptions netutil.TCPOptions
}

// DialControl opens the control connection.
//
// TLS is applied here when configured. There is exactly one control connection
// per client and it carries only small JSON messages, so the handshake and the
// per-record overhead are irrelevant.
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

// DialWork opens a work connection, which carries tunnel payload.
//
// TLS is deliberately NOT applied here even when configured for the control
// connection. A tls.Conn is not a *net.TCPConn, so wrapping the socket would
// forfeit splice(2) and force every byte of every tunnel through userspace —
// the exact cost this design exists to avoid. Payload confidentiality belongs
// to the tunnelled protocol: an https tunnel is already encrypted end to end,
// and wrapping it again just spends CPU to encrypt ciphertext.
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

// DialMux opens the single connection that will carry a yamux session.
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
	conn, err := dialer.DialContext(ctx, "tcp", d.Addr)
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

// directSource hands out one fresh TCP connection per stream.
type directSource struct {
	dialer *Dialer
}

// NewDirectSource returns the default, non-multiplexed stream source.
func NewDirectSource(d *Dialer) StreamSource { return &directSource{dialer: d} }

func (s *directSource) Open(ctx context.Context) (net.Conn, error) {
	return s.dialer.DialWork(ctx)
}

func (s *directSource) Close() error { return nil }

func (s *directSource) Multiplexed() bool { return false }
