package netutil

import (
	"io"
	"net"
	"runtime"
	"testing"
	"time"
)

func TestLimiterPacesToTheRate(t *testing.T) {
	const rate = 1 << 20

	limiter := NewLimiter(rate)
	started := time.Now()

	for range 4 {
		limiter.wait(rate * int64(limiterWindow) / int64(time.Second))
	}

	elapsed := time.Since(started)
	if elapsed < 200*time.Millisecond {
		t.Errorf("four bursts of a %d B/s limit took %s; the limiter is not "+
			"holding the rate", rate, elapsed)
	}
	if elapsed > 2*time.Second {
		t.Errorf("four bursts took %s, far longer than the rate requires", elapsed)
	}
}

func TestNoLimiterWhenRateIsZero(t *testing.T) {
	for _, rate := range []int64{0, -1} {
		if l := NewLimiter(rate); l != nil {
			t.Errorf("NewLimiter(%d) = %v, want nil so the relay stays unpaced", rate, l)
		}
	}
	var none *Limiter
	if none.Rate() != 0 {
		t.Error("a nil limiter should report no rate")
	}
	none.wait(1 << 20)
}

func TestLimitedRelayKeepsTheKernelFastPath(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("splice(2) is Linux-only")
	}

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer ln.Close()

	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		io.Copy(conn, conn)
	}()

	upstream, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatalf("dial upstream: %v", err)
	}
	defer upstream.Close()

	front, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen front: %v", err)
	}
	defer front.Close()

	statsCh := make(chan RelayStats, 1)
	go func() {
		visitor, err := front.Accept()
		if err != nil {
			return
		}
		defer visitor.Close()
		statsCh <- RelayLimited(visitor, upstream,
			NewLimiter(8<<20), NewLimiter(8<<20))
	}()

	client, err := net.Dial("tcp", front.Addr().String())
	if err != nil {
		t.Fatalf("dial front: %v", err)
	}
	defer client.Close()

	payload := make([]byte, 256<<10)
	for i := range payload {
		payload[i] = byte(i)
	}

	go func() {
		client.Write(payload)
		client.(*net.TCPConn).CloseWrite()
	}()

	got := make([]byte, len(payload))
	if _, err := io.ReadFull(client, got); err != nil {
		t.Fatalf("read back: %v", err)
	}
	for i := range got {
		if got[i] != payload[i] {
			t.Fatalf("payload corrupted at byte %d under a rate limit", i)
		}
	}
	client.Close()

	select {
	case stats := <-statsCh:
		if !stats.Spliced {
			t.Error("a rate-limited relay between two TCP connections reported " +
				"itself unspliced; limiting must not cost the kernel fast path")
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the relay did not finish")
	}
}
