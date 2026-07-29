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

// TestReplenishDoesNotReRequestConnectionsAlreadyComing guards the flow
// control that keeps the client's dial rate off the visitor arrival rate.
//
// Every visitor tops the pool up, and a dial takes a round trip to land.
// Without an account of what was already asked for, every visitor arriving
// inside that window computes the same deficit and asks for it again, so a
// burst is requested per visitor rather than once. On a real path that
// exhausted the connection table of the proxy in front of the client — and
// the tunnel then went down not because a visitor failed, but because the
// client could no longer dial at all.
func TestReplenishDoesNotReRequestConnectionsAlreadyComing(t *testing.T) {
	session := testSession(t, 8, 32)

	session.replenishPool()
	if got := session.poolInFlight.Load(); got != 8 {
		t.Fatalf("in flight after the first replenish = %d, want 8", got)
	}

	// Every visitor behind it, inside the round trip, asks for nothing more.
	for range 50 {
		session.replenishPool()
	}
	if got := session.poolInFlight.Load(); got != 8 {
		t.Errorf("in flight after 50 more visitors = %d, want it still 8 — "+
			"the shortfall is already on its way", got)
	}

	// As they arrive the count comes down and the pool stops asking.
	for range 8 {
		conn, _ := liveWorkConn(t)
		session.AddWorkConn(conn)
	}
	if got := session.poolInFlight.Load(); got != 0 {
		t.Errorf("in flight after all 8 arrived = %d, want 0", got)
	}
	session.replenishPool()
	if got := session.poolInFlight.Load(); got != 0 {
		t.Errorf("in flight with a full pool = %d, want 0", got)
	}
}

// stubCarrier stands in for a client's overflow carrier.
type stubCarrier struct{ opened int }

func (s *stubCarrier) Open(context.Context) (net.Conn, error) {
	s.opened++
	client, server := net.Pipe()
	go io.Copy(io.Discard, server)
	return client, nil
}
func (s *stubCarrier) Close() error      { return nil }
func (s *stubCarrier) Multiplexed() bool { return true }

// TestCarrierTakesThePoolOffTheVisitorPath is the second half of the
// architecture, and the half that is easy to forget.
//
// The carrier stops visitors failing. It does not, on its own, stop the
// client dialling: topping the pool up on the visitor path means the dial
// rate still rises with the visitor rate, bounded only by the outstanding
// count. Measured on a real path, a burst the carrier served without a single
// error still exhausted the connection table in front of the client, and the
// tunnel dropped afterwards because the client could no longer dial at all.
//
// With a carrier, visitors are already being served, so the pool refills on a
// timer instead — at a rate that does not rise with load.
func TestCarrierTakesThePoolOffTheVisitorPath(t *testing.T) {
	session := testSession(t, 8, 32)

	// Without a carrier the visitor path must still top up: there is no
	// alternative to dialling, and waiting for a tick would be latency added
	// to somebody's request.
	session.replenishPool()
	if got := session.poolInFlight.Load(); got != 8 {
		t.Fatalf("with no carrier, in flight after a visitor = %d, want 8", got)
	}

	// Drain the accounting and install a carrier.
	session.poolInFlight.Store(0)
	session.SetOverflow(&stubCarrier{})

	for range 100 {
		session.replenishPool()
	}
	if got := session.poolInFlight.Load(); got != 0 {
		t.Errorf("with a carrier, 100 visitors asked for %d connections; want "+
			"none — the carrier is serving them and the dial rate must not "+
			"follow the visitor rate", got)
	}

	// The timer path still refills it, so ordinary traffic returns to direct
	// spliceable connections once the burst passes.
	session.topUpPool()
	if got := session.poolInFlight.Load(); got != 8 {
		t.Errorf("the timed refill asked for %d, want 8", got)
	}
}
