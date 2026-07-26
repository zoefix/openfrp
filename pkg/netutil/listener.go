package netutil

import (
	"context"
	"errors"
	"fmt"
	"net"
	"runtime"
	"sync"
)

// Listen binds addr and returns a net.Listener.
//
// When opts.ReusePort is set and more than one accept loop is requested, this
// binds the address several times with SO_REUSEPORT and fans the results into
// one Accept stream. The kernel then spreads incoming connections across the
// listeners, which removes the single accept-queue lock that becomes the first
// bottleneck under high connection churn.
//
// Falling back to a single listener is always safe, and that is what happens
// on platforms without SO_REUSEPORT.
func Listen(ctx context.Context, network, addr string, opts ListenOptions, loops int) (net.Listener, error) {
	if loops <= 0 {
		loops = runtime.NumCPU()
	}
	if !opts.ReusePort || !reusePortSupported {
		loops = 1
	}

	cfg := NewListenConfig(opts)

	if loops == 1 {
		ln, err := cfg.Listen(ctx, network, addr)
		if err != nil {
			return nil, fmt.Errorf("netutil: listen %s %s: %w", network, addr, err)
		}
		return ln, nil
	}

	listeners := make([]net.Listener, 0, loops)
	for i := range loops {
		ln, err := cfg.Listen(ctx, network, addr)
		if err != nil {
			for _, done := range listeners {
				done.Close()
			}
			return nil, fmt.Errorf("netutil: listen %s %s (loop %d/%d): %w",
				network, addr, i+1, loops, err)
		}
		// Every listener after the first must land on the same port. Binding
		// port 0 would otherwise hand each loop a different ephemeral port.
		if i == 0 && len(listeners) == 0 {
			addr = ln.Addr().String()
		}
		listeners = append(listeners, ln)
	}

	return newFanInListener(listeners), nil
}

// fanInListener presents several SO_REUSEPORT listeners as one.
type fanInListener struct {
	listeners []net.Listener
	accepted  chan acceptResult

	closeOnce sync.Once
	closed    chan struct{}
	wg        sync.WaitGroup
}

type acceptResult struct {
	conn net.Conn
	err  error
}

func newFanInListener(listeners []net.Listener) *fanInListener {
	l := &fanInListener{
		listeners: listeners,
		accepted:  make(chan acceptResult),
		closed:    make(chan struct{}),
	}

	for _, ln := range listeners {
		l.wg.Add(1)
		go l.acceptLoop(ln)
	}
	return l
}

func (l *fanInListener) acceptLoop(ln net.Listener) {
	defer l.wg.Done()

	for {
		conn, err := ln.Accept()
		if err != nil {
			select {
			case <-l.closed:
				// Shutting down; the error is just the listener closing.
				return
			default:
			}
			// A temporary error should not take the whole loop down, but the
			// net package no longer exposes a reliable way to classify one, so
			// surface it and let the caller decide.
			select {
			case l.accepted <- acceptResult{err: err}:
			case <-l.closed:
			}
			return
		}

		select {
		case l.accepted <- acceptResult{conn: conn}:
		case <-l.closed:
			conn.Close()
			return
		}
	}
}

// Accept returns the next connection from any of the underlying listeners.
func (l *fanInListener) Accept() (net.Conn, error) {
	select {
	case res := <-l.accepted:
		return res.conn, res.err
	case <-l.closed:
		return nil, net.ErrClosed
	}
}

// Close shuts every underlying listener down.
func (l *fanInListener) Close() error {
	var errs []error
	l.closeOnce.Do(func() {
		close(l.closed)
		for _, ln := range l.listeners {
			if err := ln.Close(); err != nil {
				errs = append(errs, err)
			}
		}
		l.wg.Wait()
	})
	return errors.Join(errs...)
}

// Addr reports the shared bound address.
func (l *fanInListener) Addr() net.Addr {
	return l.listeners[0].Addr()
}

// AcceptLoops reports how many accept loops back this listener. It returns 1
// for an ordinary net.Listener, and exists so tests and the status panel can
// confirm the fan-in actually engaged.
func AcceptLoops(ln net.Listener) int {
	if fan, ok := ln.(*fanInListener); ok {
		return len(fan.listeners)
	}
	return 1
}
