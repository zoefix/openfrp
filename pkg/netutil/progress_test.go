package netutil

import (
	"bytes"
	"io"
	"net"
	"runtime"
	"sync/atomic"
	"testing"
	"time"
)

func TestProgressReportsWhileTheConnectionIsOpen(t *testing.T) {
	visitor, service, stop := relayPair(t)
	defer stop()

	var toService, toVisitor atomic.Int64
	go func() {
		RelayWith(visitor.far, service.far, RelayOptions{
			Progress: func(a, b int64) {
				toService.Add(a)
				toVisitor.Add(b)
			},
		})
	}()

	request := []byte("GET / HTTP/1.1\r\nHost: x\r\n\r\n")
	if _, err := visitor.near.Write(request); err != nil {
		t.Fatalf("write request: %v", err)
	}

	got := make([]byte, len(request))
	if _, err := io.ReadFull(service.near, got); err != nil {
		t.Fatalf("service read: %v", err)
	}

	response := bytes.Repeat([]byte("x"), 4096)
	if _, err := service.near.Write(response); err != nil {
		t.Fatalf("write response: %v", err)
	}
	if _, err := io.ReadFull(visitor.near, make([]byte, len(response))); err != nil {
		t.Fatalf("visitor read: %v", err)
	}

	deadline := time.Now().Add(4 * progressInterval)
	for time.Now().Before(deadline) {
		if toService.Load() == int64(len(request)) && toVisitor.Load() == int64(len(response)) {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}

	t.Errorf("while the connection was still open, progress reported %d to the "+
		"service and %d to the visitor; want %d and %d — a connection held "+
		"open must be counted before it closes",
		toService.Load(), toVisitor.Load(), len(request), len(response))
}

func TestProgressKeepsTheKernelFastPath(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("splice(2) is Linux-only")
	}

	visitor, service, stop := relayPair(t)
	defer stop()

	go func() {
		defer service.near.Close()
		io.Copy(service.near, service.near)
	}()

	statsCh := make(chan RelayStats, 1)
	var counted atomic.Int64
	go func() {
		statsCh <- RelayWith(visitor.far, service.far, RelayOptions{
			Progress: func(a, b int64) { counted.Add(a + b) },
		})
	}()

	payload := make([]byte, 256<<10)
	for i := range payload {
		payload[i] = byte(i)
	}
	go func() {
		visitor.near.Write(payload)
		visitor.near.(*net.TCPConn).CloseWrite()
	}()

	got := make([]byte, len(payload))
	if _, err := io.ReadFull(visitor.near, got); err != nil {
		t.Fatalf("read back: %v", err)
	}
	if !bytes.Equal(got, payload) {
		t.Fatal("payload corrupted across progress rounds")
	}
	visitor.near.Close()

	select {
	case stats := <-statsCh:
		if !stats.Spliced {
			t.Error("a watched relay reported itself unspliced; counting bytes " +
				"must not cost the kernel fast path")
		}

		if want := stats.AToB + stats.BToA; counted.Load() != want {
			t.Errorf("progress reported %d bytes, the finished relay %d; the "+
				"two must agree or the live figure drifts from the total",
				counted.Load(), want)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("the relay did not finish")
	}
}

type connPair struct {
	near net.Conn
	far  net.Conn
}

func relayPair(t *testing.T) (visitor, service connPair, stop func()) {
	t.Helper()

	make1 := func() connPair {
		ln, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatalf("listen: %v", err)
		}
		defer ln.Close()

		accepted := make(chan net.Conn, 1)
		go func() {
			c, err := ln.Accept()
			if err == nil {
				accepted <- c
			}
		}()

		near, err := net.Dial("tcp", ln.Addr().String())
		if err != nil {
			t.Fatalf("dial: %v", err)
		}
		return connPair{near: near, far: <-accepted}
	}

	visitor, service = make1(), make1()
	return visitor, service, func() {
		visitor.near.Close()
		visitor.far.Close()
		service.near.Close()
		service.far.Close()
	}
}
