package e2e

import (
	"bytes"
	"context"
	"fmt"
	"net"
	"strconv"
	"testing"
	"time"

	"github.com/zoefix/openfrp/internal/config"
	"github.com/zoefix/openfrp/internal/tunnel/client"
	"github.com/zoefix/openfrp/internal/tunnel/server"
	"github.com/zoefix/openfrp/pkg/log"
)

func startUDPEcho(t testing.TB) (host string, port int) {
	t.Helper()

	conn, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatalf("listen udp: %v", err)
	}
	t.Cleanup(func() { conn.Close() })

	go func() {
		buf := make([]byte, 65535)
		for {
			n, from, err := conn.ReadFromUDP(buf)
			if err != nil {
				return
			}
			reply := append([]byte("echo:"), buf[:n]...)
			conn.WriteToUDP(reply, from)
		}
	}()

	addr := conn.LocalAddr().(*net.UDPAddr)
	return "127.0.0.1", addr.Port
}

func startUDPTunnel(t testing.TB) int {
	t.Helper()

	localHost, localPort := startUDPEcho(t)
	remotePort := freePort(t)

	logger := log.Discard()
	if testing.Verbose() {
		logger, _ = log.Setup(log.Options{Level: "debug"})
	}

	serverCfg := &config.Server{BindAddr: "127.0.0.1", Token: testToken, AcceptLoops: 1}
	serverCfg.ApplyDefaults()
	serverCfg.BindPort = 0

	srv, err := server.New(serverCfg, logger, "test")
	if err != nil {
		t.Fatalf("server.New: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	if err := srv.Listen(ctx); err != nil {
		t.Fatalf("Listen: %v", err)
	}
	go srv.Serve(ctx)

	clientCfg := &config.Client{
		ServerAddr: "127.0.0.1",
		ServerPort: srv.Addr().(*net.TCPAddr).Port,
		Token:      testToken,
		Name:       "udp-client",
		Transport:  config.Transport{PoolCount: 4},
		Tunnels: []config.Tunnel{{
			Name: "echo-udp", Enabled: true, Type: config.TunnelUDP,
			LocalIP: localHost, LocalPort: localPort, RemotePort: remotePort,
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
	go cli.Run(ctx)

	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		for _, session := range srv.Registry().Sessions() {
			if port, ok := session.ProxyPort("echo-udp"); ok && port != 0 {
				return port
			}
		}
		time.Sleep(20 * time.Millisecond)
	}

	t.Fatal("the udp tunnel was not published within 10s")
	return 0
}

func TestUDPTunnelCarriesDatagrams(t *testing.T) {
	port := startUDPTunnel(t)

	conn, err := net.Dial("udp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)))
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()

	if err := conn.SetDeadline(time.Now().Add(15 * time.Second)); err != nil {
		t.Fatalf("SetDeadline: %v", err)
	}

	payload := []byte("through the udp tunnel")
	if _, err := conn.Write(payload); err != nil {
		t.Fatalf("write: %v", err)
	}

	buf := make([]byte, 65535)
	n, err := conn.Read(buf)
	if err != nil {
		t.Fatalf("read: %v", err)
	}

	want := append([]byte("echo:"), payload...)
	if !bytes.Equal(buf[:n], want) {
		t.Errorf("got %q, want %q", buf[:n], want)
	}
}

func TestUDPTunnelKeepsClientsApart(t *testing.T) {
	port := startUDPTunnel(t)
	addr := net.JoinHostPort("127.0.0.1", strconv.Itoa(port))

	const clients = 8
	type result struct {
		index int
		body  string
		err   error
	}
	results := make(chan result, clients)

	for i := range clients {
		go func() {
			conn, err := net.Dial("udp", addr)
			if err != nil {
				results <- result{i, "", err}
				return
			}
			defer conn.Close()
			conn.SetDeadline(time.Now().Add(15 * time.Second))

			payload := fmt.Sprintf("client-%02d", i)
			if _, err := conn.Write([]byte(payload)); err != nil {
				results <- result{i, "", err}
				return
			}

			buf := make([]byte, 65535)
			n, err := conn.Read(buf)
			if err != nil {
				results <- result{i, "", err}
				return
			}
			results <- result{i, string(buf[:n]), nil}
		}()
	}

	for range clients {
		got := <-results
		if got.err != nil {
			t.Errorf("client %d: %v", got.index, got.err)
			continue
		}
		want := fmt.Sprintf("echo:client-%02d", got.index)
		if got.body != want {
			t.Errorf("client %d received %q, want %q — replies are crossing between clients",
				got.index, got.body, want)
		}
	}
}
