// Package e2e drives a real server and a real client over loopback sockets.
//
// These tests are the acceptance gate for P0: they prove a user connection is
// carried end to end, and that the relay reaches the kernel fast path on the
// platform we actually ship to.
package e2e

import (
	"bytes"
	"context"
	"crypto/rand"
	"fmt"
	"io"
	"net"
	"runtime"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/zoefix/openfrp/internal/config"
	"github.com/zoefix/openfrp/internal/tunnel/client"
	"github.com/zoefix/openfrp/internal/tunnel/server"
	"github.com/zoefix/openfrp/pkg/log"
	"github.com/zoefix/openfrp/pkg/netutil"
)

const testToken = "integration-token"

// harness is a running server, a running client, and a LAN service behind it.
type harness struct {
	server     *server.Server
	serverPort int
	localAddr  string
}

// startEchoService stands in for the LAN service behind the router.
func startEchoService(t testing.TB) string {
	t.Helper()

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { ln.Close() })

	go func() {
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

	return ln.Addr().String()
}

// start brings up a server and a client wired to one tcp tunnel.
func start(t testing.TB, mux bool) *harness {
	t.Helper()

	localAddr := startEchoService(t)
	localHost, localPortStr, err := net.SplitHostPort(localAddr)
	if err != nil {
		t.Fatalf("split local addr: %v", err)
	}
	localPort, _ := strconv.Atoi(localPortStr)

	logger := log.Discard()
	if testing.Verbose() {
		logger, _ = log.Setup(log.Options{Level: "debug"})
	}

	serverCfg := &config.Server{
		BindAddr: "127.0.0.1",
		BindPort: 0, // let the kernel choose
		Token:    testToken,
		// AcceptLoops stays at the default (one per CPU, SO_REUSEPORT) so
		// these tests and benchmarks exercise the accept path production runs.
	}
	serverCfg.ApplyDefaults()
	// ApplyDefaults would substitute the standard port for zero, so restore
	// the ephemeral request afterwards.
	serverCfg.BindPort = 0

	srv, err := server.New(serverCfg, logger, "test")
	if err != nil {
		t.Fatalf("server.New: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	if err := srv.Listen(ctx); err != nil {
		t.Fatalf("server.Listen: %v", err)
	}
	serverPort := srv.Addr().(*net.TCPAddr).Port

	serveDone := make(chan struct{})
	go func() {
		defer close(serveDone)
		srv.Serve(ctx)
	}()
	t.Cleanup(func() {
		cancel()
		select {
		case <-serveDone:
		case <-time.After(5 * time.Second):
			t.Error("server did not shut down within 5s")
		}
	})

	clientCfg := &config.Client{
		ServerAddr: "127.0.0.1",
		ServerPort: serverPort,
		Token:      testToken,
		Name:       "test-router",
		Transport: config.Transport{
			Mux:       mux,
			PoolCount: 4,
		},
		Tunnels: []config.Tunnel{{
			Name:       "echo",
			Enabled:    true,
			Type:       config.TunnelTCP,
			LocalIP:    localHost,
			LocalPort:  localPort,
			RemotePort: 0, // ask the server to allocate
		}},
	}
	clientCfg.ApplyDefaults()
	if err := clientCfg.Validate(); err != nil {
		t.Fatalf("client config: %v", err)
	}

	cli, err := client.New(clientCfg, logger, "test")
	if err != nil {
		t.Fatalf("client.New: %v", err)
	}

	clientDone := make(chan struct{})
	go func() {
		defer close(clientDone)
		cli.Run(ctx)
	}()
	t.Cleanup(func() {
		cancel()
		select {
		case <-clientDone:
		case <-time.After(5 * time.Second):
			t.Error("client did not shut down within 5s")
		}
	})

	return &harness{
		server:     srv,
		serverPort: serverPort,
		localAddr:  localAddr,
	}
}

// proxyPort waits for the tunnel to be published and returns its public port.
func (h *harness) proxyPort(t testing.TB, name string) int {
	t.Helper()

	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		for _, session := range h.server.Registry().Sessions() {
			if port, ok := session.ProxyPort(name); ok && port != 0 {
				return port
			}
		}
		time.Sleep(20 * time.Millisecond)
	}

	t.Fatalf("tunnel %q was not published within 10s", name)
	return 0
}

// dialTunnel opens a connection to the published tunnel.
func (h *harness) dialTunnel(t testing.TB, port int) net.Conn {
	t.Helper()

	addr := net.JoinHostPort("127.0.0.1", strconv.Itoa(port))

	// The proxy listener is up before the warm pool necessarily is, so allow a
	// couple of attempts rather than flaking on a cold start.
	var lastErr error
	for range 50 {
		conn, err := net.DialTimeout("tcp", addr, 2*time.Second)
		if err == nil {
			t.Cleanup(func() { conn.Close() })
			return conn
		}
		lastErr = err
		time.Sleep(50 * time.Millisecond)
	}

	t.Fatalf("dial tunnel %s: %v", addr, lastErr)
	return nil
}

// TestTunnelCarriesTraffic is the P0 acceptance test.
func TestTunnelCarriesTraffic(t *testing.T) {
	h := start(t, false)
	port := h.proxyPort(t, "echo")

	conn := h.dialTunnel(t, port)
	if err := conn.SetDeadline(time.Now().Add(10 * time.Second)); err != nil {
		t.Fatalf("SetDeadline: %v", err)
	}

	payload := []byte("through the tunnel and back")
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

// TestTunnelCarriesBulkPayload pushes enough data to exercise the relay past
// any single buffer, which is where an off-by-one in the copy path shows up.
func TestTunnelCarriesBulkPayload(t *testing.T) {
	h := start(t, false)
	port := h.proxyPort(t, "echo")

	conn := h.dialTunnel(t, port)
	if err := conn.SetDeadline(time.Now().Add(30 * time.Second)); err != nil {
		t.Fatalf("SetDeadline: %v", err)
	}

	payload := make([]byte, 4<<20) // 4 MiB
	if _, err := rand.Read(payload); err != nil {
		t.Fatalf("rand: %v", err)
	}

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		if _, err := conn.Write(payload); err != nil {
			t.Errorf("write: %v", err)
		}
	}()

	got := make([]byte, len(payload))
	if _, err := io.ReadFull(conn, got); err != nil {
		t.Fatalf("read: %v", err)
	}
	wg.Wait()

	if !bytes.Equal(got, payload) {
		t.Error("bulk payload came back corrupted")
	}
}

// TestTunnelHandlesConcurrentConnections proves the warm pool refills under
// load rather than serving the first few connections and then stalling.
func TestTunnelHandlesConcurrentConnections(t *testing.T) {
	h := start(t, false)
	port := h.proxyPort(t, "echo")

	const clients = 24

	var wg sync.WaitGroup
	errs := make(chan error, clients)

	for i := range clients {
		wg.Add(1)
		go func() {
			defer wg.Done()

			conn := h.dialTunnel(t, port)
			if err := conn.SetDeadline(time.Now().Add(20 * time.Second)); err != nil {
				errs <- err
				return
			}

			msg := []byte(fmt.Sprintf("client-%02d", i))
			if _, err := conn.Write(msg); err != nil {
				errs <- fmt.Errorf("client %d write: %w", i, err)
				return
			}
			got := make([]byte, len(msg))
			if _, err := io.ReadFull(conn, got); err != nil {
				errs <- fmt.Errorf("client %d read: %w", i, err)
				return
			}
			if !bytes.Equal(got, msg) {
				errs <- fmt.Errorf("client %d got %q, want %q", i, got, msg)
			}
		}()
	}

	wg.Wait()
	close(errs)

	for err := range errs {
		t.Error(err)
	}
}

// TestTunnelOverMuxTransport covers the opt-in multiplexed path. It is the
// slow path by design, but it still has to be correct.
func TestTunnelOverMuxTransport(t *testing.T) {
	h := start(t, true)
	port := h.proxyPort(t, "echo")

	conn := h.dialTunnel(t, port)
	if err := conn.SetDeadline(time.Now().Add(10 * time.Second)); err != nil {
		t.Fatalf("SetDeadline: %v", err)
	}

	payload := []byte("multiplexed round trip")
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

// TestServerRejectsBadToken confirms authentication is actually enforced.
func TestServerRejectsBadToken(t *testing.T) {
	h := start(t, false)
	h.proxyPort(t, "echo") // wait until the good client is established

	logger := log.Discard()
	badCfg := &config.Client{
		ServerAddr: "127.0.0.1",
		ServerPort: h.serverPort,
		Token:      "wrong-token",
		Name:       "impostor",
		Tunnels: []config.Tunnel{{
			Name: "evil", Enabled: true, Type: config.TunnelTCP,
			LocalIP: "127.0.0.1", LocalPort: 9, RemotePort: 0,
		}},
	}
	badCfg.ApplyDefaults()

	cli, err := client.New(badCfg, logger, "test")
	if err != nil {
		t.Fatalf("client.New: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	go cli.Run(ctx)
	<-ctx.Done()

	// The impostor must never appear as a session, and the legitimate client
	// must still be connected.
	for _, session := range h.server.Registry().Sessions() {
		if session.Name() == "impostor" {
			t.Fatal("a client with the wrong token established a session")
		}
	}
	if h.server.Registry().Len() != 1 {
		t.Errorf("registry has %d sessions, want just the legitimate client",
			h.server.Registry().Len())
	}
}

// TestEndToEndUsesKernelFastPath is the performance regression guard at the
// integration level. The unit test in netutil proves the eligibility rules;
// this proves the assembled server and client actually satisfy them, which is
// what a refactor would silently break.
func TestEndToEndUsesKernelFastPath(t *testing.T) {
	if !netutil.ReusePortSupported() {
		t.Skip("platform without the socket features this exercises")
	}

	netutil.ResetRelayCounts()

	h := start(t, false)
	port := h.proxyPort(t, "echo")

	conn := h.dialTunnel(t, port)
	if err := conn.SetDeadline(time.Now().Add(10 * time.Second)); err != nil {
		t.Fatalf("SetDeadline: %v", err)
	}
	payload := []byte("fast path")
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

	// Two relays serve one tunnelled connection: the server joins user to work
	// connection, the client joins work connection to the local service.
	if runtime.GOOS != "linux" {
		t.Logf("splice(2) is Linux-only; recorded %d buffered relays here", buffered)
		return
	}
	if buffered != 0 {
		t.Errorf("%d relay(s) fell back to a userspace copy on Linux; "+
			"every hop of a plain TCP tunnel must be spliced", buffered)
	}
	if spliced < 2 {
		t.Errorf("recorded %d spliced relays, want at least 2 "+
			"(server hop and client hop)", spliced)
	}
}
