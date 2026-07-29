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

		if i == 0 && len(listeners) == 0 {
			addr = ln.Addr().String()
		}
		listeners = append(listeners, ln)
	}

	return newFanInListener(listeners), nil
}

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

func acceptRetryDelay(err error) time.Duration {
	switch {
	case errors.Is(err, syscall.EMFILE), errors.Is(err, syscall.ENFILE):
		return 10 * time.Millisecond
	case errors.Is(err, syscall.ECONNABORTED), errors.Is(err, syscall.EINTR):
		return time.Nanosecond
	default:
		return 0
	}
}

type fanInListener struct {
	listeners []net.Listener

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

func (l *fanInListener) Addr() net.Addr {
	return l.listeners[0].Addr()
}

func AcceptLoops(ln net.Listener) int {
	if fan, ok := ln.(*fanInListener); ok {
		return len(fan.listeners)
	}
	return 1
}
