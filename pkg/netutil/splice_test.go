package netutil

import (
	"bytes"
	"crypto/rand"
	"io"
	"net"
	"sync"
	"testing"
	"time"
)

// tcpPair returns two ends of a real loopback TCP connection.
func tcpPair(t *testing.T) (client, server net.Conn) {
	t.Helper()

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer ln.Close()

	type result struct {
		conn net.Conn
		err  error
	}
	accepted := make(chan result, 1)
	go func() {
		c, err := ln.Accept()
		accepted <- result{c, err}
	}()

	client, err = net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatalf("dial: %v", err)
	}

	got := <-accepted
	if got.err != nil {
		t.Fatalf("accept: %v", got.err)
	}

	t.Cleanup(func() {
		client.Close()
		got.conn.Close()
	})
	return client, got.conn
}

// plainWrapper hides the underlying connection entirely. It stands in for the
// mistake this package exists to prevent: a well-meaning counter or shim that
// silently costs us the kernel fast path.
type plainWrapper struct{ net.Conn }

// transparentWrapper does not transform bytes and correctly opts in to
// Unwrapper, so the fast path must survive it.
type transparentWrapper struct{ net.Conn }

func (w transparentWrapper) Unwrap() net.Conn { return w.Conn }

// cyclicWrapper returns itself, to prove the unwrap walk terminates.
type cyclicWrapper struct{ net.Conn }

func (w *cyclicWrapper) Unwrap() net.Conn { return w }

func TestUnwrapResolvesToUnderlyingConn(t *testing.T) {
	client, _ := tcpPair(t)

	if _, ok := unwrap(client).(*net.TCPConn); !ok {
		t.Fatal("bare TCPConn should unwrap to itself")
	}

	if _, ok := unwrap(transparentWrapper{client}).(*net.TCPConn); !ok {
		t.Error("transparent wrapper should unwrap to the TCPConn underneath")
	}

	// Two layers deep still resolves.
	nested := transparentWrapper{transparentWrapper{client}}
	if _, ok := unwrap(nested).(*net.TCPConn); !ok {
		t.Error("nested transparent wrappers should unwrap to the TCPConn")
	}

	if _, ok := unwrap(plainWrapper{client}).(*net.TCPConn); ok {
		t.Error("an opaque wrapper must NOT resolve to the raw TCPConn")
	}

	// Must terminate rather than spin.
	done := make(chan struct{})
	go func() {
		defer close(done)
		unwrap(&cyclicWrapper{client})
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("unwrap did not terminate on a cyclic wrapper")
	}
}

// TestCanSpliceFastPath is the regression guard the plan calls for: it fails if
// a change quietly makes the zero-copy path ineligible.
func TestCanSpliceFastPath(t *testing.T) {
	a, _ := tcpPair(t)
	b, _ := tcpPair(t)

	if !spliceSupported {
		if CanSplice(a, b) {
			t.Fatal("CanSplice must be false where splice(2) is unavailable")
		}
		t.Skipf("splice(2) unavailable on this platform; type resolution is covered by TestUnwrapResolvesToUnderlyingConn")
	}

	if !CanSplice(a, b) {
		t.Error("two raw TCP connections must be splice-eligible")
	}
	if !CanSplice(transparentWrapper{a}, transparentWrapper{b}) {
		t.Error("transparent wrappers must preserve splice eligibility")
	}
	if CanSplice(plainWrapper{a}, b) {
		t.Error("an opaque wrapper must forfeit splice eligibility")
	}

	// A pipe is not a socket, so it can never splice.
	p1, p2 := net.Pipe()
	defer p1.Close()
	defer p2.Close()
	if CanSplice(p1, p2) {
		t.Error("net.Pipe must not be splice-eligible")
	}
}

func TestRelayCopiesBothDirections(t *testing.T) {
	// Two independent connection pairs joined by a relay, which is exactly the
	// shape of a proxied tunnel: user <-> server <-> local service.
	userSide, serverIn := tcpPair(t)
	serverOut, service := tcpPair(t)

	go Relay(serverIn, serverOut)

	toService := []byte("request from the user")
	toUser := []byte("response from the service")

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		got := make([]byte, len(toService))
		if _, err := io.ReadFull(service, got); err != nil {
			t.Errorf("service read: %v", err)
			return
		}
		if !bytes.Equal(got, toService) {
			t.Errorf("service got %q, want %q", got, toService)
		}
		if _, err := service.Write(toUser); err != nil {
			t.Errorf("service write: %v", err)
		}
	}()

	if _, err := userSide.Write(toService); err != nil {
		t.Fatalf("user write: %v", err)
	}

	got := make([]byte, len(toUser))
	if _, err := io.ReadFull(userSide, got); err != nil {
		t.Fatalf("user read: %v", err)
	}
	if !bytes.Equal(got, toUser) {
		t.Errorf("user got %q, want %q", got, toUser)
	}

	wg.Wait()
}

func TestRelayReportsByteCounts(t *testing.T) {
	userSide, serverIn := tcpPair(t)
	serverOut, service := tcpPair(t)

	payload := make([]byte, 1<<20) // 1 MiB, larger than one buffer
	if _, err := rand.Read(payload); err != nil {
		t.Fatalf("rand: %v", err)
	}

	statsCh := make(chan RelayStats, 1)
	go func() { statsCh <- Relay(serverIn, serverOut) }()

	// Drain the service side so the writer cannot block.
	drained := make(chan int64, 1)
	go func() {
		n, _ := io.Copy(io.Discard, service)
		drained <- n
	}()

	if _, err := userSide.Write(payload); err != nil {
		t.Fatalf("write: %v", err)
	}
	// Half close so the relay sees EOF and finishes this direction.
	if err := userSide.(*net.TCPConn).CloseWrite(); err != nil {
		t.Fatalf("CloseWrite: %v", err)
	}

	if n := <-drained; n != int64(len(payload)) {
		t.Errorf("service received %d bytes, want %d", n, len(payload))
	}

	service.Close()
	stats := <-statsCh

	if stats.AToB != int64(len(payload)) {
		t.Errorf("stats.AToB = %d, want %d", stats.AToB, len(payload))
	}
	if stats.Spliced != spliceSupported {
		t.Errorf("stats.Spliced = %v, want %v on this platform", stats.Spliced, spliceSupported)
	}
}

// TestRelayHalfClosePreservesReverseDirection covers the case that breaks HTTP
// if it regresses: closing the request direction must not truncate the
// response still in flight.
func TestRelayHalfClosePreservesReverseDirection(t *testing.T) {
	userSide, serverIn := tcpPair(t)
	serverOut, service := tcpPair(t)

	go Relay(serverIn, serverOut)

	if _, err := userSide.Write([]byte("GET /")); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := userSide.(*net.TCPConn).CloseWrite(); err != nil {
		t.Fatalf("CloseWrite: %v", err)
	}

	// The service should observe EOF on its read side.
	if _, err := io.ReadAll(service); err != nil {
		t.Fatalf("service read to EOF: %v", err)
	}

	// And must still be able to answer.
	reply := []byte("HTTP/1.1 200 OK")
	if _, err := service.Write(reply); err != nil {
		t.Fatalf("service write after half close: %v", err)
	}
	service.Close()

	got, err := io.ReadAll(userSide)
	if err != nil {
		t.Fatalf("user read: %v", err)
	}
	if !bytes.Equal(got, reply) {
		t.Errorf("user got %q after half close, want %q", got, reply)
	}
}

func TestCopyBufferedUsesLargeBuffer(t *testing.T) {
	payload := bytes.Repeat([]byte("x"), 3*CopyBufferSize+17)

	// recordingWriter proves the large pooled buffer reaches the writer rather
	// than io.CopyBuffer falling back to its own 32 KiB one.
	rec := &recordingWriter{}
	n, err := CopyBuffered(rec, bytes.NewReader(payload))
	if err != nil {
		t.Fatalf("CopyBuffered: %v", err)
	}
	if n != int64(len(payload)) {
		t.Errorf("copied %d bytes, want %d", n, len(payload))
	}
	if rec.maxWrite != CopyBufferSize {
		t.Errorf("largest write was %d bytes, want %d — the pooled buffer is not being used",
			rec.maxWrite, CopyBufferSize)
	}
	if rec.total != len(payload) {
		t.Errorf("writer saw %d bytes, want %d", rec.total, len(payload))
	}
}

type recordingWriter struct {
	maxWrite int
	total    int
}

func (w *recordingWriter) Write(p []byte) (int, error) {
	if len(p) > w.maxWrite {
		w.maxWrite = len(p)
	}
	w.total += len(p)
	return len(p), nil
}
