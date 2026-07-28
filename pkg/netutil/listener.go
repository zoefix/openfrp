package netutil

import (
	"context"
	"errors"
	"fmt"
	"net"
	"runtime"
	"sync"
	"syscall"
	"time"
)

// Listen binds addr and returns a net.Listener.
//
// When opts.ReusePort is set and more than one accept loop is requested, this
// binds the address several times with SO_REUSEPORT. The kernel then spreads
// incoming connections across the listeners, which removes the single
// accept-queue lock that becomes the first bottleneck under high connection
// churn.
//
// Serve is the intended consumer: it accepts on every underlying listener in
// parallel, which is what actually cashes in the SO_REUSEPORT distribution.
// Plain Accept also works — the listeners are merged into one stream — but it
// re-serialises what the kernel just spread out, so it is kept only for tests
// and casual callers.
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

// Serve accepts connections from ln until it closes, calling dispatch for each
// one.
//
// When ln fans several SO_REUSEPORT listeners in, one accept loop runs per
// underlying listener and dispatch is called directly on that loop's
// goroutine — no channel, no handoff, no shared lock. This is the nginx worker
// shape, and it is the difference between SO_REUSEPORT helping and SO_REUSEPORT
// being decoration: a merged Accept stream funnels every kernel-balanced
// connection back through one consumer.
//
// dispatch runs on the accept goroutine, so it must only hand the connection
// off — typically wg.Add plus go — and return. Blocking in dispatch stalls
// that loop's accepts.
//
// Accept errors that mean "too busy right now" do not stop serving. Running
// out of file descriptors is the first thing that happens to a relay under
// real load, and the previous behaviour — returning, which tore down the
// whole proxy — turned a transient overload into an outage. Those errors are
// waited out instead; nginx does the same.
//
// Serve returns nil once every accept loop has ended after a close, or the
// first fatal accept error otherwise. Connections already dispatched are the
// caller's to wait for.
func Serve(ln net.Listener, dispatch func(net.Conn)) error {
	listeners := []net.Listener{ln}
	if fan, ok := ln.(*fanInListener); ok {
		var err error
		if listeners, err = fan.take(); err != nil {
			return err
		}
	}

	var (
		wg       sync.WaitGroup
		errOnce  sync.Once
		fatalErr error
	)

	for _, sub := range listeners {
		wg.Add(1)
		go func() {
			defer wg.Done()

			for {
				conn, err := sub.Accept()
				if err != nil {
					if delay := acceptRetryDelay(err); delay > 0 {
						time.Sleep(delay)
						continue
					}
					if !errors.Is(err, net.ErrClosed) {
						errOnce.Do(func() { fatalErr = err })
					}
					// Stop the sibling loops too: a caller retrying a fatal
					// error on one loop while others serve would be worse than
					// a clean stop.
					ln.Close()
					return
				}
				dispatch(conn)
			}
		}()
	}

	wg.Wait()
	return fatalErr
}

// acceptRetryDelay classifies an accept error. A positive delay means "wait
// this long and try again"; zero means the error is fatal to the loop.
//
//   - EMFILE/ENFILE: the process or system is out of file descriptors. The
//     connection stays in the kernel backlog while we wait for handlers to
//     finish and free some.
//   - ECONNABORTED: the peer gave up between SYN and accept. Nothing is wrong
//     with the listener; take the next one immediately.
//   - EINTR: interrupted, retry immediately.
func acceptRetryDelay(err error) time.Duration {
	switch {
	case errors.Is(err, syscall.EMFILE), errors.Is(err, syscall.ENFILE):
		return 10 * time.Millisecond
	case errors.Is(err, syscall.ECONNABORTED), errors.Is(err, syscall.EINTR):
		return time.Nanosecond // retry now, but non-zero to mean "not fatal"
	default:
		return 0
	}
}

// fanInListener presents several SO_REUSEPORT listeners as one.
//
// Serve consumes the listeners directly and in parallel; that is the fast
// path. The Accept method exists so the type still honours net.Listener for
// tests and incidental callers, and starts its merge pumps only on first use —
// a listener that is only ever Served must not have a competing consumer.
type fanInListener struct {
	listeners []net.Listener

	// mode guards the choice between Accept's merged stream and Serve's
	// direct consumption. Whichever is called first wins; mixing them would
	// mean two consumers racing for the same accept queues.
	modeMu sync.Mutex
	mode   fanInMode

	accepted chan acceptResult

	closeOnce sync.Once
	closed    chan struct{}
	pumpWG    sync.WaitGroup
}

type fanInMode int

const (
	fanInIdle fanInMode = iota
	fanInAccept
	fanInServe
)

type acceptResult struct {
	conn net.Conn
	err  error
}

func newFanInListener(listeners []net.Listener) *fanInListener {
	return &fanInListener{
		listeners: listeners,
		accepted:  make(chan acceptResult),
		closed:    make(chan struct{}),
	}
}

// take hands the underlying listeners to Serve and locks Accept out.
func (l *fanInListener) take() ([]net.Listener, error) {
	l.modeMu.Lock()
	defer l.modeMu.Unlock()

	switch l.mode {
	case fanInAccept:
		return nil, errors.New("netutil: listener is already being consumed via Accept")
	case fanInServe:
		return nil, errors.New("netutil: listener is already being served")
	}
	l.mode = fanInServe
	return l.listeners, nil
}

// startPumps begins merging accepts into the channel, for Accept-mode use.
func (l *fanInListener) startPumps() error {
	l.modeMu.Lock()
	defer l.modeMu.Unlock()

	switch l.mode {
	case fanInServe:
		return errors.New("netutil: listener is already being served")
	case fanInAccept:
		return nil
	}
	l.mode = fanInAccept

	for _, ln := range l.listeners {
		l.pumpWG.Add(1)
		go l.pump(ln)
	}
	return nil
}

func (l *fanInListener) pump(ln net.Listener) {
	defer l.pumpWG.Done()

	for {
		conn, err := ln.Accept()
		if err != nil {
			select {
			case <-l.closed:
				// Shutting down; the error is just the listener closing.
				return
			default:
			}
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
	if err := l.startPumps(); err != nil {
		return nil, err
	}

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
		l.pumpWG.Wait()
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
