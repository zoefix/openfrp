package server

import (
	"context"
	"io"
	"log/slog"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/zoefix/openfrp/internal/tunnel/protocol"
)

// testSession builds a session whose control connection is drained, so that
// asking the client for more work connections neither blocks nor fails.
func testSession(t *testing.T, poolTarget, maxPool int) *Session {
	t.Helper()

	control, peer := net.Pipe()
	t.Cleanup(func() { control.Close(); peer.Close() })
	go io.Copy(io.Discard, peer)

	return newSession(SessionOptions{
		RunID:      "run",
		Conn:       control,
		Codec:      protocol.NewCodec(control),
		Logger:     slog.New(slog.DiscardHandler),
		PoolTarget: poolTarget,
		MaxPool:    maxPool,
	})
}

// deadWorkConn is a connection that died while it was parked in the pool: the
// far end is gone, and the first write to it is where that is discovered.
func deadWorkConn(t *testing.T) net.Conn {
	t.Helper()

	conn, peer := net.Pipe()
	peer.Close()
	t.Cleanup(func() { conn.Close() })
	return conn
}

// liveWorkConn is a connection with a client still on the other end, reading.
// The StartWorkConn it is sent is returned on the channel.
func liveWorkConn(t *testing.T) (net.Conn, <-chan protocol.Message) {
	t.Helper()

	conn, peer := net.Pipe()
	t.Cleanup(func() { conn.Close(); peer.Close() })

	started := make(chan protocol.Message, 1)
	go func() {
		msg, err := protocol.ReadMessage(peer)
		if err == nil {
			started <- msg
		}
		close(started)
	}()
	return conn, started
}

// A pool that has gone stale must cost the visitor nothing. The connections in
// it are idle for exactly as long as nobody visits the site, so on a quiet
// tunnel every one of them is dead by morning — and each one used to be a 502
// for whoever arrived first.
func TestStaleWorkConnectionsAreSkippedNotServed(t *testing.T) {
	session := testSession(t, 4, 8)

	for range 3 {
		session.AddWorkConn(deadWorkConn(t))
	}
	live, started := liveWorkConn(t)
	session.AddWorkConn(live)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	conn, err := session.GetWorkConn(ctx, "site", "203.0.113.9:40000")
	if err != nil {
		t.Fatalf("GetWorkConn: %v", err)
	}
	if conn != live {
		t.Fatal("a dead connection was handed to the visitor")
	}

	select {
	case msg := <-started:
		start, ok := msg.(*protocol.StartWorkConn)
		if !ok {
			t.Fatalf("the client was sent %T, not a StartWorkConn", msg)
		}
		if start.ProxyName != "site" {
			t.Errorf("proxy is %q, want site", start.ProxyName)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the connection was returned without telling the client what it carries")
	}
}

// With every pooled connection dead the visitor still has to be served: the
// client is asked for a fresh one and it is used.
func TestAnEntirelyStalePoolFallsBackToAFreshConnection(t *testing.T) {
	session := testSession(t, 4, 8)

	for range 4 {
		session.AddWorkConn(deadWorkConn(t))
	}

	live, started := liveWorkConn(t)
	// The client answers the request for another connection, a moment late.
	go func() {
		time.Sleep(50 * time.Millisecond)
		session.AddWorkConn(live)
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	conn, err := session.GetWorkConn(ctx, "site", "203.0.113.9:40000")
	if err != nil {
		t.Fatalf("GetWorkConn: %v", err)
	}
	if conn != live {
		t.Fatal("a dead connection was handed to the visitor")
	}
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("the fresh connection was never told what it carries")
	}
}

// A client that has genuinely gone still has to fail, and say how it failed.
// Skipping stale connections must not turn a dead client into a hang.
func TestAPoolThatCannotBeRefilledStillFails(t *testing.T) {
	session := testSession(t, 4, 8)
	session.AddWorkConn(deadWorkConn(t))

	// Shorter than workConnTimeout, so the caller's own deadline is what ends
	// the wait — this is the visitor giving up, not the server.
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()

	conn, err := session.GetWorkConn(ctx, "site", "203.0.113.9:40000")
	if err == nil {
		conn.Close()
		t.Fatal("GetWorkConn succeeded with no client to answer it")
	}
	if !strings.Contains(err.Error(), "stale") {
		t.Errorf("the failure does not mention the stale connections it discarded: %v", err)
	}
}

// TestPoolGrowsWithDemandAndSettlesBack covers the control loop that decides
// how warm the pool stays.
//
// The stake is a stall the benchmarks cannot see: on loopback a missed pool
// costs microseconds, while on a real path the visitor waits for the server
// to ask, the client to dial and the connection to be registered — about two
// round trips before their first byte moves. A pool that does not grow into a
// burst pays that on every connection past its size.
func TestPoolGrowsWithDemandAndSettlesBack(t *testing.T) {
	session := testSession(t, 4, 32)

	if got := session.PoolTarget(); got != 4 {
		t.Fatalf("initial pool target = %d, want the configured 4", got)
	}

	// Twenty visitors find the pool empty. Each miss is one step, so the
	// target lands where the demand actually was rather than at a guess.
	for range 20 {
		session.growPool()
	}
	if got := session.PoolTarget(); got != 24 {
		t.Errorf("after 20 misses the pool target = %d, want 24", got)
	}

	// The ceiling is the server's cap, not the client's request.
	for range 100 {
		session.growPool()
	}
	if got := session.PoolTarget(); got != 32 {
		t.Errorf("pool target = %d, want it clamped to the 32 maximum", got)
	}

	// A quiet interval gives a quarter of the surplus back. Driven directly
	// rather than by waiting out the real ninety second tick.
	session.poolMisses.Store(0)
	session.decayPool()
	if got := session.PoolTarget(); got != 25 {
		t.Errorf("after one quiet interval the pool target = %d, want 25 "+
			"(a quarter of the 28 surplus given back)", got)
	}

	// An interval with any miss in it holds the pool where it is: the burst
	// is still going, and shrinking now would rebuild the stall.
	session.poolMisses.Store(1)
	session.decayPool()
	if got := session.PoolTarget(); got != 25 {
		t.Errorf("pool target = %d after an interval with a miss, want it held at 25", got)
	}

	// Quiet for long enough and it returns exactly to what was configured,
	// never below it.
	for range 50 {
		session.poolMisses.Store(0)
		session.decayPool()
	}
	if got := session.PoolTarget(); got != 4 {
		t.Errorf("pool target settled at %d, want the configured floor of 4", got)
	}
}
