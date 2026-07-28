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

	// KeepAlive is the keepalive interval for accepted connections. Zero
	// leaves the data-plane default in place; negative disables keepalives.
	KeepAlive time.Duration

	// NoDelayOff disables the TCP_NODELAY that accepted connections get by
	// default. The default is on because tunnels carry interactive traffic;
	// this exists so a caller can say otherwise, and is spelled negatively so
	// the zero value is the one we want.
	NoDelayOff bool

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

// AcceptedTCPOptions returns the tuning an accepted connection should end up
// with, given the listener's options.
func AcceptedTCPOptions(opts ListenOptions) TCPOptions {
	tuned := DefaultTCPOptions()
	tuned.NoDelay = !opts.NoDelayOff
	if opts.KeepAlive != 0 {
		tuned.KeepAlive = opts.KeepAlive
	}
	if tuned.KeepAlive < 0 {
		tuned.KeepAlive = 0
	}
	return tuned
}

// NewListenConfig builds a net.ListenConfig honouring opts.
//
// Where the platform clones socket options into accepted connections, the
// connection tuning is applied here, to the listening socket, and every
// connection arrives already tuned. That is worth doing carefully because of
// what it removes: three setsockopt syscalls per accepted connection from us,
// and three more from the runtime.
//
// The runtime's are the surprising half. net.ListenConfig treats a zero
// KeepAlive as "on, at my default" and implements it per accepted connection,
// which both costs the syscalls and silently overwrites what the listener was
// configured with — a 47 second idle set at bind time came back as Go's 15.
// Passing a negative value is what turns that off.
func NewListenConfig(opts ListenOptions) net.ListenConfig {
	tuned := AcceptedTCPOptions(opts)

	cfg := net.ListenConfig{}
	if acceptedInheritsOptions {
		// Negative rather than zero: see above. Zero would have the runtime
		// re-state keepalive on every connection, over the top of ours.
		cfg.KeepAlive = -1
	} else {
		cfg.KeepAlive = tuned.KeepAlive
	}

	deferSecs := 0
	if opts.DeferAccept > 0 && deferAcceptSupported {
		// Round up: a sub-second request must not truncate to "kernel
		// default", which is what zero means to the option.
		deferSecs = int((opts.DeferAccept + time.Second - 1) / time.Second)
	}

	if opts.ReusePort || deferSecs > 0 || acceptedInheritsOptions {
		cfg.Control = func(_, _ string, rc syscall.RawConn) error {
			var sockErr error
			if err := rc.Control(func(fd uintptr) {
				if opts.ReusePort {
					if sockErr = setReusePort(fd); sockErr != nil {
						return
					}
				}
				if deferSecs > 0 {
					if sockErr = setDeferAccept(fd, deferSecs); sockErr != nil {
						return
					}
				}
				if acceptedInheritsOptions {
					sockErr = tuneListenerFD(fd, tuned)
				}
			}); err != nil {
				return err
			}
			return sockErr
		}
	}

	return cfg
}

// TuneAccepted applies the connection tuning to a freshly accepted socket.
//
// It is a no-op where the listener's options were inherited, which is the
// whole point: the call stays at every accept site, so the tuning is
// guaranteed on every platform, and costs nothing on the one that ships.
func TuneAccepted(c net.Conn, opts ListenOptions) error {
	if acceptedInheritsOptions {
		return nil
	}
	return TuneConn(c, AcceptedTCPOptions(opts))
}
