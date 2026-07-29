package netutil

import (
	"errors"
	"net"
	"syscall"
	"time"
)

type TCPOptions struct {
	NoDelay bool

	SendBuffer int
	RecvBuffer int

	KeepAlive time.Duration
}

func DefaultTCPOptions() TCPOptions {
	return TCPOptions{
		NoDelay:   true,
		KeepAlive: 30 * time.Second,
	}
}

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

type ListenOptions struct {
	ReusePort bool

	KeepAlive time.Duration

	NoDelayOff bool

	DeferAccept time.Duration
}

func ReusePortSupported() bool { return reusePortSupported }

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

func NewListenConfig(opts ListenOptions) net.ListenConfig {
	tuned := AcceptedTCPOptions(opts)

	cfg := net.ListenConfig{}
	if acceptedInheritsOptions {

		cfg.KeepAlive = -1
	} else {
		cfg.KeepAlive = tuned.KeepAlive
	}

	deferSecs := 0
	if opts.DeferAccept > 0 && deferAcceptSupported {

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

func TuneAccepted(c net.Conn, opts ListenOptions) error {
	if acceptedInheritsOptions {
		return nil
	}
	return TuneConn(c, AcceptedTCPOptions(opts))
}
