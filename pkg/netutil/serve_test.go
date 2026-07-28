package netutil

import (
	"context"
	"fmt"
	"io"
	"net"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
)

// TestServeDispatchesFromAllLoops proves the direct-dispatch path serves
// connections across every SO_REUSEPORT listener without a merge channel.
func TestServeDispatchesFromAllLoops(t *testing.T) {
	if !reusePortSupported {
		t.Skip("SO_REUSEPORT unavailable on this platform")
	}

	const loops = 4
	ln, err := Listen(context.Background(), "tcp", "127.0.0.1:0",
		ListenOptions{ReusePort: true}, loops)
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}
	defer ln.Close()

	var handled atomic.Int64

	serveDone := make(chan error, 1)
	go func() {
		serveDone <- Serve(ln, func(conn net.Conn) {
			handled.Add(1)
			go func() {
				defer conn.Close()
				io.Copy(conn, conn)
			}()
		})
	}()

	const clients = 32
	var wg sync.WaitGroup
	for i := range clients {
		wg.Add(1)
		go func() {
			defer wg.Done()
			conn, err := net.DialTimeout("tcp", ln.Addr().String(), 5*time.Second)
			if err != nil {
				t.Errorf("dial %d: %v", i, err)
				return
			}
			defer conn.Close()
			assertEcho(t, conn, fmt.Sprintf("client-%d", i))
		}()
	}
	wg.Wait()

	ln.Close()
	select {
	case err := <-serveDone:
		if err != nil {
			t.Errorf("Serve returned %v after a clean close, want nil", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Serve did not return after the listener closed")
	}

	if got := handled.Load(); got != clients {
		t.Errorf("dispatched %d connections, want %d", got, clients)
	}
}

// TestServeAndAcceptAreMutuallyExclusive: whichever consumer engages first
// owns the listener; the other must be refused rather than silently racing
// for the same accept queues.
func TestServeAndAcceptAreMutuallyExclusive(t *testing.T) {
	if !reusePortSupported {
		t.Skip("SO_REUSEPORT unavailable on this platform")
	}

	// Serve first: Accept must be refused. take() is what Serve calls before
	// its first accept, so claiming through it directly makes the ordering
	// deterministic instead of racing two goroutines.
	ln, err := Listen(context.Background(), "tcp", "127.0.0.1:0",
		ListenOptions{ReusePort: true}, 2)
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}
	if _, err := ln.(*fanInListener).take(); err != nil {
		t.Fatalf("take: %v", err)
	}
	if _, err := ln.Accept(); err == nil {
		t.Error("Accept succeeded on a listener already owned by Serve")
	}
	ln.Close()

	// Accept first: Serve must be refused.
	ln2, err := Listen(context.Background(), "tcp", "127.0.0.1:0",
		ListenOptions{ReusePort: true}, 2)
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}
	defer ln2.Close()

	go func() {
		conn, err := net.Dial("tcp", ln2.Addr().String())
		if err == nil {
			conn.Close()
		}
	}()
	if conn, err := ln2.Accept(); err == nil {
		conn.Close()
	}

	if err := Serve(ln2, func(c net.Conn) { c.Close() }); err == nil {
		t.Error("Serve succeeded on a listener already owned by Accept")
	}
}

// fakeListener scripts a sequence of Accept outcomes.
type fakeListener struct {
	mu      sync.Mutex
	outcome []acceptResult
	closed  chan struct{}
	once    sync.Once
}

func (f *fakeListener) Accept() (net.Conn, error) {
	f.mu.Lock()
	if len(f.outcome) > 0 {
		next := f.outcome[0]
		f.outcome = f.outcome[1:]
		f.mu.Unlock()
		return next.conn, next.err
	}
	f.mu.Unlock()

	<-f.closed
	return nil, net.ErrClosed
}

func (f *fakeListener) Close() error {
	f.once.Do(func() { close(f.closed) })
	return nil
}

func (f *fakeListener) Addr() net.Addr {
	return &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 0}
}

// TestServeSurvivesFdExhaustion: EMFILE from accept must be waited out, not
// treated as fatal. The old behaviour tore the whole proxy down the moment
// the process ran out of descriptors — precisely when it was busiest.
func TestServeSurvivesFdExhaustion(t *testing.T) {
	client, server := net.Pipe()
	defer client.Close()

	fl := &fakeListener{
		closed: make(chan struct{}),
		outcome: []acceptResult{
			{err: &net.OpError{Op: "accept", Err: syscall.EMFILE}},
			{err: &net.OpError{Op: "accept", Err: syscall.ECONNABORTED}},
			{conn: server},
		},
	}

	var handled atomic.Int64
	serveDone := make(chan error, 1)
	go func() {
		serveDone <- Serve(fl, func(conn net.Conn) {
			handled.Add(1)
			conn.Close()
			fl.Close()
		})
	}()

	select {
	case err := <-serveDone:
		if err != nil {
			t.Errorf("Serve = %v, want nil", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Serve did not survive the scripted errors")
	}

	if got := handled.Load(); got != 1 {
		t.Errorf("dispatched %d connections, want 1 after transient errors", got)
	}
}

// TestServeReportsFatalAcceptError: an error with no retry classification
// must stop the loops and surface.
func TestServeReportsFatalAcceptError(t *testing.T) {
	fl := &fakeListener{
		closed: make(chan struct{}),
		outcome: []acceptResult{
			{err: &net.OpError{Op: "accept", Err: syscall.EINVAL}},
		},
	}

	err := Serve(fl, func(conn net.Conn) { conn.Close() })
	if err == nil {
		t.Fatal("Serve = nil, want the fatal accept error")
	}
}
