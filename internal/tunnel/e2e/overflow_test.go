package e2e

import (
	"bytes"
	"io"
	"runtime"
	"testing"
	"time"

	"github.com/zoefix/openfrp/pkg/netutil"
)

// waitForOverflow blocks until the client has offered a carrier, which
// happens a moment after the tunnels are published.
func waitForOverflow(t testing.TB, h *harness) {
	t.Helper()

	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		for _, session := range h.server.Registry().Sessions() {
			if session.HasOverflow() {
				return
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("the client never offered an overflow carrier")
}

// TestOverflowServesAnEmptyPool is the acceptance test for the architecture
// change.
//
// The pool is drained deliberately and then a visitor arrives. Before the
// carrier existed, that visitor's only option was to wait for the server to
// ask the client for a connection and for the client to dial one back —
// roughly two round trips on a real path, and a fresh entry in whatever NAT
// or proxy sits in front of the client, which is the resource that actually
// runs out under a burst.
//
// What is asserted is that the visitor is served at all with the pool empty
// and no new connection permitted to arrive in time to help.
func TestOverflowServesAnEmptyPool(t *testing.T) {
	h := start(t, false)
	port := h.proxyPort(t, "echo")
	waitForOverflow(t, h)

	// Empty the pool from under the proxy, so the next visitor finds nothing
	// warm — the exact state a burst produces.
	var drained int
	for _, session := range h.server.Registry().Sessions() {
		drained += session.DrainPool()
	}
	if drained == 0 {
		t.Fatal("the pool was already empty, so this proves nothing")
	}

	conn := h.dialTunnel(t, port)
	if err := conn.SetDeadline(time.Now().Add(10 * time.Second)); err != nil {
		t.Fatalf("SetDeadline: %v", err)
	}

	payload := []byte("served from the overflow carrier")
	if _, err := conn.Write(payload); err != nil {
		t.Fatalf("write: %v", err)
	}
	got := make([]byte, len(payload))
	if _, err := io.ReadFull(conn, got); err != nil {
		t.Fatalf("read: %v", err)
	}
	if !bytes.Equal(got, payload) {
		t.Errorf("got %q, want %q", got, payload)
	}
}

// TestOverflowIsNotTheDefaultPath guards the property the whole design rests
// on: a warm pool must still be served by a direct connection, because only
// those can be spliced.
//
// Without this, a change that quietly routed everything through the carrier
// would pass every functional test while giving up the kernel fast path — the
// single largest advantage this data plane has, and one nothing else would
// report as lost.
func TestOverflowIsNotTheDefaultPath(t *testing.T) {
	h := start(t, false)
	port := h.proxyPort(t, "echo")
	waitForOverflow(t, h)

	// Let the pool settle so the visitor below certainly finds one warm.
	time.Sleep(200 * time.Millisecond)
	netutil.ResetRelayCounts()

	conn := h.dialTunnel(t, port)
	if err := conn.SetDeadline(time.Now().Add(10 * time.Second)); err != nil {
		t.Fatalf("SetDeadline: %v", err)
	}
	payload := []byte("warm pool")
	if _, err := conn.Write(payload); err != nil {
		t.Fatalf("write: %v", err)
	}
	got := make([]byte, len(payload))
	if _, err := io.ReadFull(conn, got); err != nil {
		t.Fatalf("read: %v", err)
	}

	spliced, buffered := netutil.RelayCounts()
	if spliced+buffered == 0 {
		t.Fatal("no relays were recorded")
	}
	if runtime.GOOS != "linux" {
		// splice(2) is Linux-only, so everywhere else every relay is
		// buffered and the distinction this test rests on does not exist.
		t.Logf("splice is Linux-only; recorded %d buffered relays here", buffered)
		return
	}
	if spliced == 0 {
		t.Errorf("a visitor served from a warm pool produced %d spliced and "+
			"%d buffered relays; with connections warm the direct path must "+
			"still be taken, or the kernel fast path has been given up",
			spliced, buffered)
	}
}
