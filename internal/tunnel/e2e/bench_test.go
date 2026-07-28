package e2e

import (
	"io"
	"net"
	"strconv"
	"testing"
	"time"
)

// The benchmarks here measure the three costs that decide whether the tunnel
// keeps up with a plain reverse proxy: how fast connections can be set up
// (accept → work connection → relay start), how much latency the relay adds to
// a small round trip, and how fast bulk bytes move once a relay is running.
//
// Run them with a fixed iteration count rather than -benchtime on wall time:
// every tunnelled connection consumes three ephemeral ports (user, work,
// local), and an auto-scaled b.N can exhaust the port range mid-run and
// measure the kernel's TIME_WAIT behaviour instead of the tunnel.
//
//	go test ./internal/tunnel/e2e -bench . -benchtime 2000x

// dialBench opens a connection to the tunnel without the test harness's
// per-connection Cleanup, which would accumulate b.N closures.
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

// roundTrip writes payload and reads it back, failing the benchmark on error.
func roundTrip(b *testing.B, conn net.Conn, payload, scratch []byte) {
	if _, err := conn.Write(payload); err != nil {
		b.Fatalf("write: %v", err)
	}
	if _, err := io.ReadFull(conn, scratch[:len(payload)]); err != nil {
		b.Fatalf("read: %v", err)
	}
}

// BenchmarkTunnelConnect is the connection-setup path: dial, one round trip to
// prove the relay is live, close. This is the path the accept loop, the warm
// pool handoff and the StartWorkConn exchange all sit on.
func BenchmarkTunnelConnect(b *testing.B) {
	h := start(b, false)
	port := h.proxyPort(b, "echo")

	// One throwaway round trip so the pool and the route are warm before the
	// clock starts.
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

// BenchmarkTunnelConnectParallel is the same path under concurrent arrivals,
// which is where accept-queue and pool contention would appear.
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

// BenchmarkTunnelEcho is relay latency: one persistent connection, small round
// trips. Every microsecond here is pure tunnel overhead on top of loopback.
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
	roundTrip(b, conn, payload, scratch) // warm

	b.SetBytes(128) // 64 bytes each way
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		roundTrip(b, conn, payload, scratch)
	}
}

// BenchmarkTunnelThroughput is bulk transfer on one persistent connection:
// write a chunk, read the echo back. Half-duplex by construction, so it is a
// relative number for comparing builds, not a line-rate claim.
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
	roundTrip(b, conn, payload, scratch) // warm

	b.SetBytes(chunk)
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		roundTrip(b, conn, payload, scratch)
	}
}

// BenchmarkTunnelUDPEcho is the framed UDP path: one datagram out, one reply
// back, per op. The per-packet allocation work shows up directly here.
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

	// The first datagram races tunnel publication, so retry until the path is
	// proven live before the clock starts.
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
