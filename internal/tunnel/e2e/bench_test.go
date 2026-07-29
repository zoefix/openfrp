package e2e

import (
	"io"
	"net"
	"strconv"
	"testing"
	"time"
)

func dialBench(b *testing.B, port int) net.Conn {
	addr := net.JoinHostPort("127.0.0.1", strconv.Itoa(port))

	var lastErr error
	for range 50 {
		conn, err := net.DialTimeout("tcp", addr, 2*time.Second)
		if err == nil {
			return conn
		}
		lastErr = err
		time.Sleep(20 * time.Millisecond)
	}
	b.Fatalf("dial tunnel %s: %v", addr, lastErr)
	return nil
}

func roundTrip(b *testing.B, conn net.Conn, payload, scratch []byte) {
	if _, err := conn.Write(payload); err != nil {
		b.Fatalf("write: %v", err)
	}
	if _, err := io.ReadFull(conn, scratch[:len(payload)]); err != nil {
		b.Fatalf("read: %v", err)
	}
}

func BenchmarkTunnelConnect(b *testing.B) {
	h := start(b, false)
	port := h.proxyPort(b, "echo")

	warm := dialBench(b, port)
	roundTrip(b, warm, []byte("w"), make([]byte, 1))
	warm.Close()

	payload := []byte("x")
	scratch := make([]byte, 1)

	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		conn := dialBench(b, port)
		if err := conn.SetDeadline(time.Now().Add(10 * time.Second)); err != nil {
			b.Fatalf("deadline: %v", err)
		}
		roundTrip(b, conn, payload, scratch)
		conn.Close()
	}
}

func BenchmarkTunnelConnectParallel(b *testing.B) {
	h := start(b, false)
	port := h.proxyPort(b, "echo")

	warm := dialBench(b, port)
	roundTrip(b, warm, []byte("w"), make([]byte, 1))
	warm.Close()

	b.ReportAllocs()
	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		payload := []byte("x")
		scratch := make([]byte, 1)
		for pb.Next() {
			conn := dialBench(b, port)
			if err := conn.SetDeadline(time.Now().Add(10 * time.Second)); err != nil {
				b.Errorf("deadline: %v", err)
				return
			}
			roundTrip(b, conn, payload, scratch)
			conn.Close()
		}
	})
}

func BenchmarkTunnelEcho(b *testing.B) {
	h := start(b, false)
	port := h.proxyPort(b, "echo")

	conn := dialBench(b, port)
	defer conn.Close()
	if err := conn.SetDeadline(time.Now().Add(10 * time.Minute)); err != nil {
		b.Fatalf("deadline: %v", err)
	}

	payload := make([]byte, 64)
	scratch := make([]byte, 64)
	roundTrip(b, conn, payload, scratch)

	b.SetBytes(128)
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		roundTrip(b, conn, payload, scratch)
	}
}

func BenchmarkTunnelThroughput(b *testing.B) {
	h := start(b, false)
	port := h.proxyPort(b, "echo")

	conn := dialBench(b, port)
	defer conn.Close()
	if err := conn.SetDeadline(time.Now().Add(10 * time.Minute)); err != nil {
		b.Fatalf("deadline: %v", err)
	}

	const chunk = 256 << 10
	payload := make([]byte, chunk)
	scratch := make([]byte, chunk)
	roundTrip(b, conn, payload, scratch)

	b.SetBytes(chunk)
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		roundTrip(b, conn, payload, scratch)
	}
}

func BenchmarkTunnelUDPEcho(b *testing.B) {
	remotePort := startUDPTunnel(b)

	conn, err := net.Dial("udp",
		net.JoinHostPort("127.0.0.1", strconv.Itoa(remotePort)))
	if err != nil {
		b.Fatalf("dial udp: %v", err)
	}
	defer conn.Close()

	payload := []byte("benchmark-datagram")
	want := len("echo:") + len(payload)
	scratch := make([]byte, 2048)

	warmed := false
	for attempt := range 50 {
		conn.SetDeadline(time.Now().Add(200 * time.Millisecond))
		if _, err := conn.Write(payload); err != nil {
			b.Fatalf("warm write: %v", err)
		}
		if n, err := conn.Read(scratch); err == nil && n == want {
			warmed = true
			break
		}
		_ = attempt
	}
	if !warmed {
		b.Fatal("udp tunnel did not answer within 50 attempts")
	}

	b.SetBytes(int64(len(payload) + want))
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		if err := conn.SetDeadline(time.Now().Add(5 * time.Second)); err != nil {
			b.Fatalf("deadline: %v", err)
		}
		if _, err := conn.Write(payload); err != nil {
			b.Fatalf("write: %v", err)
		}
		if _, err := io.ReadFull(conn, scratch[:want]); err != nil {
			b.Fatalf("read: %v", err)
		}
	}
}
