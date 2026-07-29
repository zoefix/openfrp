package netutil

import (
	"context"
	"fmt"
	"io"
	"net"
	"sync"
	"testing"
	"time"
)

func TestListenSingleLoop(t *testing.T) {
	ln, err := Listen(context.Background(), "tcp", "127.0.0.1:0", ListenOptions{}, 1)
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}
	defer ln.Close()

	if got := AcceptLoops(ln); got != 1 {
		t.Errorf("AcceptLoops = %d, want 1", got)
	}
	echoOnce(t, ln)

	conn, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()
	assertEcho(t, conn, "hello")
}

func TestListenFanInSharesOnePort(t *testing.T) {
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

	if got := AcceptLoops(ln); got != loops {
		t.Fatalf("AcceptLoops = %d, want %d — the fan-in did not engage", got, loops)
	}

	addr := ln.Addr().String()
	if _, port, err := net.SplitHostPort(addr); err != nil || port == "0" {
		t.Fatalf("listener address %q is not a concrete port", addr)
	}

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				defer conn.Close()
				io.Copy(conn, conn)
			}()
		}
	}()

	const clients = 32
	var clientWG sync.WaitGroup
	for i := range clients {
		clientWG.Add(1)
		go func() {
			defer clientWG.Done()
			conn, err := net.DialTimeout("tcp", addr, 5*time.Second)
			if err != nil {
				t.Errorf("dial %d: %v", i, err)
				return
			}
			defer conn.Close()
			assertEcho(t, conn, fmt.Sprintf("client-%d", i))
		}()
	}
	clientWG.Wait()

	ln.Close()
	wg.Wait()
}

func TestListenFanInCloseIsIdempotent(t *testing.T) {
	if !reusePortSupported {
		t.Skip("SO_REUSEPORT unavailable on this platform")
	}

	ln, err := Listen(context.Background(), "tcp", "127.0.0.1:0",
		ListenOptions{ReusePort: true}, 3)
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}

	if err := ln.Close(); err != nil {
		t.Errorf("first Close: %v", err)
	}
	if err := ln.Close(); err != nil {
		t.Errorf("second Close should be a no-op, got %v", err)
	}

	if _, err := ln.Accept(); err == nil {
		t.Error("Accept after Close should fail")
	}
}

func TestListenFallsBackWithoutReusePort(t *testing.T) {
	ln, err := Listen(context.Background(), "tcp", "127.0.0.1:0",
		ListenOptions{ReusePort: false}, 8)
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}
	defer ln.Close()

	if got := AcceptLoops(ln); got != 1 {
		t.Errorf("AcceptLoops = %d, want 1 when ReusePort is off", got)
	}
	echoOnce(t, ln)

	conn, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()
	assertEcho(t, conn, "fallback")
}

func echoOnce(t *testing.T, ln net.Listener) {
	t.Helper()
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		io.Copy(conn, conn)
	}()
}

func assertEcho(t *testing.T, conn net.Conn, msg string) {
	t.Helper()

	if err := conn.SetDeadline(time.Now().Add(5 * time.Second)); err != nil {
		t.Fatalf("SetDeadline: %v", err)
	}
	if _, err := conn.Write([]byte(msg)); err != nil {
		t.Fatalf("write: %v", err)
	}
	got := make([]byte, len(msg))
	if _, err := io.ReadFull(conn, got); err != nil {
		t.Fatalf("read: %v", err)
	}
	if string(got) != msg {
		t.Errorf("echo = %q, want %q", got, msg)
	}
}
