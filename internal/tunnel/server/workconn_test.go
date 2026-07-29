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

func deadWorkConn(t *testing.T) net.Conn {
	t.Helper()

	conn, peer := net.Pipe()
	peer.Close()
	t.Cleanup(func() { conn.Close() })
	return conn
}

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

func TestAnEntirelyStalePoolFallsBackToAFreshConnection(t *testing.T) {
	session := testSession(t, 4, 8)

	for range 4 {
		session.AddWorkConn(deadWorkConn(t))
	}

	live, started := liveWorkConn(t)

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

func TestAPoolThatCannotBeRefilledStillFails(t *testing.T) {
	session := testSession(t, 4, 8)
	session.AddWorkConn(deadWorkConn(t))

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

func TestReplenishDoesNotReRequestConnectionsAlreadyComing(t *testing.T) {
	session := testSession(t, 8, 32)

	session.replenishPool()
	if got := session.poolInFlight.Load(); got != 8 {
		t.Fatalf("in flight after the first replenish = %d, want 8", got)
	}

	for range 50 {
		session.replenishPool()
	}
	if got := session.poolInFlight.Load(); got != 8 {
		t.Errorf("in flight after 50 more visitors = %d, want it still 8 — "+
			"the shortfall is already on its way", got)
	}

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

type stubCarrier struct{ opened int }

func (s *stubCarrier) Open(context.Context) (net.Conn, error) {
	s.opened++
	client, server := net.Pipe()
	go io.Copy(io.Discard, server)
	return client, nil
}
func (s *stubCarrier) Close() error      { return nil }
func (s *stubCarrier) Multiplexed() bool { return true }

func TestCarrierTakesThePoolOffTheVisitorPath(t *testing.T) {
	session := testSession(t, 8, 32)

	session.replenishPool()
	if got := session.poolInFlight.Load(); got != 8 {
		t.Fatalf("with no carrier, in flight after a visitor = %d, want 8", got)
	}

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

	session.topUpPool()
	if got := session.poolInFlight.Load(); got != 8 {
		t.Errorf("the timed refill asked for %d, want 8", got)
	}
}

func TestRefillTargetClimbsOnSuccessAndBacksOffOnStall(t *testing.T) {
	session := testSession(t, 8, 32)

	if got := session.refillTarget.Load(); got != 8 {
		t.Fatalf("initial refill target = %d, want the configured 8", got)
	}

	for range 5 {
		session.poolMisses.Store(1)
		session.adjustRefillTarget(false)
	}
	if got := session.refillTarget.Load(); got != 13 {
		t.Errorf("after five busy intervals the refill target = %d, want 13", got)
	}

	session.poolMisses.Store(0)
	session.adjustRefillTarget(false)
	if got := session.refillTarget.Load(); got != 13 {
		t.Errorf("a quiet interval moved the target to %d, want it held at 13", got)
	}

	session.adjustRefillTarget(true)
	if got := session.refillTarget.Load(); got != 8 {
		t.Errorf("after a stall the refill target = %d, want it halved to 8", got)
	}
	session.adjustRefillTarget(true)
	if got := session.refillTarget.Load(); got != 8 {
		t.Errorf("the refill target fell to %d, want it floored at the "+
			"configured 8", got)
	}

	for range 200 {
		session.poolMisses.Store(1)
		session.adjustRefillTarget(false)
	}
	if got := session.refillTarget.Load(); got != 32 {
		t.Errorf("refill target = %d, want it capped at the 32 maximum", got)
	}
}

type hangingCarrier struct{}

func (hangingCarrier) Open(ctx context.Context) (net.Conn, error) {
	<-ctx.Done()
	return nil, ctx.Err()
}
func (hangingCarrier) Close() error      { return nil }
func (hangingCarrier) Multiplexed() bool { return true }

func TestASaturatedCarrierDoesNotHangTheVisitor(t *testing.T) {
	session := testSession(t, 8, 32)
	session.SetOverflow(hangingCarrier{})

	start := time.Now()
	conn, served := session.overflowConn(context.Background(), "web", "203.0.113.9:1234")
	elapsed := time.Since(start)

	if served {
		conn.Close()
		t.Fatal("a carrier that never answered reported success")
	}
	if elapsed > overflowOpenTimeout*2 {
		t.Errorf("the visitor waited %s on a saturated carrier; it must give "+
			"up near %s and fall back to a dial", elapsed, overflowOpenTimeout)
	}

	if !session.HasOverflow() {
		t.Error("a saturated carrier was discarded; it is busy, not dead")
	}
}

func TestQuotaRefusesOnceSpent(t *testing.T) {
	limits := NewLimits()
	limits.Publish(protocol.ProxySpec{Name: "web", Quota: 1000})

	tunnel := limits.For("web")
	if tunnel.Exhausted() {
		t.Fatal("a fresh quota reports itself already spent")
	}

	tunnel.Spend(600)
	if tunnel.Exhausted() {
		t.Error("600 of 1000 bytes reported as exhausted")
	}

	tunnel.Spend(500)
	if !tunnel.Exhausted() {
		t.Error("1100 of 1000 bytes not reported as exhausted")
	}

	used, quota := tunnel.Usage()
	if used != 1100 || quota != 1000 {
		t.Errorf("usage = %d/%d, want 1100/1000", used, quota)
	}
}

func TestRepublishKeepsWhatWasSpent(t *testing.T) {
	limits := NewLimits()
	spec := protocol.ProxySpec{Name: "web", Quota: 1000}

	limits.Publish(spec)
	limits.For("web").Spend(900)

	limits.Publish(spec)
	if used, _ := limits.For("web").Usage(); used != 900 {
		t.Errorf("after republishing, used = %d, want the 900 already spent", used)
	}
}

func TestNoLimitsMeansNoLimiter(t *testing.T) {
	limits := NewLimits()
	limits.Publish(protocol.ProxySpec{Name: "web"})

	tunnel := limits.For("web")
	if tunnel != nil {
		t.Fatal("a tunnel with no limits was given a limit record")
	}
	if toClient, toVisitor := tunnel.Rates(); toClient != nil || toVisitor != nil {
		t.Error("an unlimited tunnel produced limiters")
	}
	if tunnel.Exhausted() {
		t.Error("an unlimited tunnel reports itself exhausted")
	}
	tunnel.Spend(1 << 30)
}

func TestTunnelLimitsNestUnderTheClientWide(t *testing.T) {
	limits := NewLimits()
	limits.SetClientLimits(1<<20, 1<<20, 100)

	limits.Publish(protocol.ProxySpec{Name: "plain"})
	toClient, toVisitor := limits.For("plain").Rates()
	if toClient == nil || toVisitor == nil {
		t.Error("a tunnel under a client-wide limit was left unlimited")
	}

	if limits.ClientExhausted() {
		t.Fatal("a fresh client cap reports itself spent")
	}
	limits.clientUsed.Add(150)
	if !limits.ClientExhausted() {
		t.Error("150 against a 100 byte client cap not reported as spent")
	}

	bare := NewLimits()
	bare.Publish(protocol.ProxySpec{Name: "plain"})
	if bare.For("plain") != nil {
		t.Error("a tunnel with no limits, under a client with none, was given a record")
	}
}

func TestClientLimitsApplyToTunnelsPublishedFirst(t *testing.T) {
	limits := NewLimits()

	limits.Publish(protocol.ProxySpec{Name: "early", DownRate: 1 << 20})
	limits.SetClientLimits(2<<20, 2<<20, 0)

	toClient, _ := limits.For("early").Rates()
	if toClient == nil {
		t.Fatal("the tunnel lost its limiter")
	}
	if toClient.Rate() == 0 {
		t.Error("the tunnel's own rate was discarded")
	}
}
