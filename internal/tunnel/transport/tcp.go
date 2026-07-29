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

	// SocketGID and SocketMark ask the kernel to make this daemon's outbound
	// connections identifiable, so a transparent proxy on the same host can
	// be persuaded to leave them alone. Zero means do not ask.
	//
	// Neither is a guarantee and they open different locks. A redirect-based
	// proxy exempts by the socket's owning group — OpenClash's output chain
	// opens with `meta skgid 65534 return` so its own traffic is not
	// redirected into itself — while proxies built on TPROXY and policy
	// routing exempt by fwmark instead. A proxy that exempts neither
	// intercepts this too, which is why the client asks the server what
	// address it actually arrived from rather than assuming.
	SocketGID  int
	SocketMark int
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

// DialWorkWith opens a work connection and sends its opening message in the
// same write as the greeting, so the whole handshake costs one syscall rather
// than two.
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

	// The group has to be in place before the socket exists, so it wraps the
	// dial rather than adjusting the result: the kernel copies the creating
	// thread's filesystem GID into the socket and there is no way to change
	// it afterwards.
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
