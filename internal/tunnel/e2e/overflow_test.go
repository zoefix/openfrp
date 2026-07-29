package e2e

import (
	"bytes"
	"io"
	"runtime"
	"testing"
	"time"

	"github.com/zoefix/openfrp/pkg/netutil"
)

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

func TestOverflowServesAnEmptyPool(t *testing.T) {
	h := start(t, false)
	port := h.proxyPort(t, "echo")
	waitForOverflow(t, h)

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

func TestOverflowIsNotTheDefaultPath(t *testing.T) {
	h := start(t, false)
	port := h.proxyPort(t, "echo")
	waitForOverflow(t, h)

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
