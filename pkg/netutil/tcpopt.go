package netutil

import (
	"errors"
	"net"
	"syscall"
	"time"
)

// TCPOptions tunes a single connection.
type TCPOptions struct {
	// NoDelay disables Nagle's algorithm. Tunnels carry interactive traffic,
	// so the extra latency Nagle buys back in packet efficiency is a bad trade.
	NoDelay bool

	// SendBuffer and RecvBuffer set SO_SNDBUF and SO_RCVBUF. Zero leaves the
	// kernel default in place, which is usually right thanks to autotuning;
	// raise them only when a measured high-BDP path needs it.
	SendBuffer int
	RecvBuffer int

	// KeepAlive enables TCP keepalives at this interval. Zero disables them.
	KeepAlive time.Duration
}

// DefaultTCPOptions returns the tuning applied to every data connection.
func DefaultTCPOptions() TCPOptions {
	return TCPOptions{
		NoDelay:   true,
		KeepAlive: 30 * time.Second,
	}
}

// TuneConn applies opts to c. Connections that are not TCP underneath are left
// alone rather than treated as an error, so callers can apply this blindly.
func TuneConn(c net.Conn, opts TCPOptions) error {
	tc, ok := unwrap(c).(*net.TCPConn)
	if !ok {
		return nil
	}

	var errs []error
	if err := tc.SetNoDelay(opts.NoDelay); err != nil {
		errs = append(errs, err)
	}
	if opts.SendBuffer > 0 {
		if err := tc.SetWriteBuffer(opts.SendBuffer); err != nil {
			errs = append(errs, err)
		}
	}
	if opts.RecvBuffer > 0 {
		if err := tc.SetReadBuffer(opts.RecvBuffer); err != nil {
			errs = append(errs, err)
		}
	}
	if opts.KeepAlive > 0 {
		if err := tc.SetKeepAlive(true); err != nil {
			errs = append(errs, err)
		}
		if err := tc.SetKeepAlivePeriod(opts.KeepAlive); err != nil {
			errs = append(errs, err)
		}
	} else if err := tc.SetKeepAlive(false); err != nil {
		errs = append(errs, err)
	}

	return errors.Join(errs...)
}

// ListenOptions configures a listening socket.
type ListenOptions struct {
	// ReusePort sets SO_REUSEPORT so several accept loops can share one port.
	// The kernel then load-balances incoming connections across them, which
	// removes the single-accept-queue lock contention that shows up first
	// under high connection churn.
	ReusePort bool

	// KeepAlive is applied to accepted connections by the runtime.
	KeepAlive time.Duration

	// DeferAccept sets TCP_DEFER_ACCEPT (Linux; silently ignored elsewhere)
	// so accept fires only when the first payload bytes have arrived.
	//
	// Only for listeners whose protocol is strictly client-first: the vhost
	// ports (an HTTP request or a ClientHello opens every connection) and the
	// control port (our preamble does). Never for a plain tcp proxy, where
	// the tunnelled protocol may be server-first — an ssh client waits for
	// the server's banner, and deferring accept would deadlock it against
	// the kernel until this timeout expired.
	DeferAccept time.Duration
}

// ReusePortSupported reports whether SO_REUSEPORT is available here.
func ReusePortSupported() bool { return reusePortSupported }

// NewListenConfig builds a net.ListenConfig honouring opts.
func NewListenConfig(opts ListenOptions) net.ListenConfig {
	cfg := net.ListenConfig{KeepAlive: opts.KeepAlive}

	deferSecs := 0
	if opts.DeferAccept > 0 && deferAcceptSupported {
		// Round up: a sub-second request must not truncate to "kernel
		// default", which is what zero means to the option.
		deferSecs = int((opts.DeferAccept + time.Second - 1) / time.Second)
	}

	if opts.ReusePort || deferSecs > 0 {
		cfg.Control = func(_, _ string, rc syscall.RawConn) error {
			var sockErr error
			if err := rc.Control(func(fd uintptr) {
				if opts.ReusePort {
					if sockErr = setReusePort(fd); sockErr != nil {
						return
					}
				}
				if deferSecs > 0 {
					sockErr = setDeferAccept(fd, deferSecs)
				}
			}); err != nil {
				return err
			}
			return sockErr
		}
	}

	return cfg
}
