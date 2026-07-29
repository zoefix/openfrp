package e2e

import (
	"context"
	"net"
	"net/http"
	"strconv"
	"testing"
	"time"

	"golang.org/x/net/websocket"

	"github.com/zoefix/openfrp/internal/config"
)

const wsIdleGap = 4 * time.Second

func startWebSocketService(t *testing.T) (host string, port int) {
	t.Helper()

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}

	mux := http.NewServeMux()
	mux.Handle("/echo", websocket.Handler(func(ws *websocket.Conn) {
		for {
			var msg string
			if err := websocket.Message.Receive(ws, &msg); err != nil {
				return
			}
			if msg == "ping-after-idle" {
				websocket.Message.Send(ws, "still-here")
				continue
			}
			if err := websocket.Message.Send(ws, "echo:"+msg); err != nil {
				return
			}
		}
	}))

	srv := &http.Server{Handler: mux}
	go srv.Serve(ln)
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		srv.Shutdown(ctx)
	})

	addr := ln.Addr().(*net.TCPAddr)
	return "127.0.0.1", addr.Port
}

func (h *vhostHarness) dialWebSocket(t *testing.T, domain, path string) *websocket.Conn {
	t.Helper()

	cfg, err := websocket.NewConfig("ws://"+domain+path, "http://"+domain)
	if err != nil {
		t.Fatalf("websocket config: %v", err)
	}

	raw, err := net.Dial("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(h.httpPort)))
	if err != nil {
		t.Fatalf("dial vhost: %v", err)
	}

	ws, err := websocket.NewClient(cfg, raw)
	if err != nil {
		raw.Close()
		t.Fatalf("websocket handshake through the tunnel: %v", err)
	}
	t.Cleanup(func() { ws.Close() })
	return ws
}

func TestHTTPTunnelCarriesWebSocket(t *testing.T) {
	host, port := startWebSocketService(t)

	h := startVhost(t, []config.Tunnel{{
		Name: "ws", Type: config.TunnelHTTP, Enabled: true,
		LocalIP: host, LocalPort: port,
		Domains: []string{"ws.example.com"},
	}})

	ws := h.dialWebSocket(t, "ws.example.com", "/echo")

	if err := websocket.Message.Send(ws, "hello"); err != nil {
		t.Fatalf("send: %v", err)
	}
	var reply string
	ws.SetReadDeadline(time.Now().Add(10 * time.Second))
	if err := websocket.Message.Receive(ws, &reply); err != nil {
		t.Fatalf("receive: %v", err)
	}
	if reply != "echo:hello" {
		t.Errorf("got %q through the tunnel, want %q", reply, "echo:hello")
	}
}

func TestWebSocketSurvivesAnIdleGap(t *testing.T) {
	host, port := startWebSocketService(t)

	h := startVhost(t, []config.Tunnel{{
		Name: "ws", Type: config.TunnelHTTP, Enabled: true,
		LocalIP: host, LocalPort: port,
		Domains: []string{"ws.example.com"},
	}})

	ws := h.dialWebSocket(t, "ws.example.com", "/echo")

	if err := websocket.Message.Send(ws, "first"); err != nil {
		t.Fatalf("send: %v", err)
	}
	var reply string
	ws.SetReadDeadline(time.Now().Add(10 * time.Second))
	if err := websocket.Message.Receive(ws, &reply); err != nil {
		t.Fatalf("receive: %v", err)
	}

	time.Sleep(wsIdleGap)

	if err := websocket.Message.Send(ws, "ping-after-idle"); err != nil {
		t.Fatalf("a websocket idle for %s could not be written to: %v; the "+
			"relay must not close a connection that is merely quiet", wsIdleGap, err)
	}
	ws.SetReadDeadline(time.Now().Add(10 * time.Second))
	if err := websocket.Message.Receive(ws, &reply); err != nil {
		t.Fatalf("a websocket idle for %s stopped carrying traffic: %v; the "+
			"progress deadline bounds a round, it must not end the connection",
			wsIdleGap, err)
	}
	if reply != "still-here" {
		t.Errorf("after %s idle got %q, want %q", wsIdleGap, reply, "still-here")
	}
}

func TestWebSocketCarriesManyFrames(t *testing.T) {
	host, port := startWebSocketService(t)

	h := startVhost(t, []config.Tunnel{{
		Name: "ws", Type: config.TunnelHTTP, Enabled: true,
		LocalIP: host, LocalPort: port,
		Domains: []string{"ws.example.com"},
	}})

	ws := h.dialWebSocket(t, "ws.example.com", "/echo")
	ws.SetReadDeadline(time.Now().Add(30 * time.Second))

	for i := range 200 {
		want := "msg-" + strconv.Itoa(i)
		if err := websocket.Message.Send(ws, want); err != nil {
			t.Fatalf("send %d: %v", i, err)
		}
		var reply string
		if err := websocket.Message.Receive(ws, &reply); err != nil {
			t.Fatalf("receive %d: %v", i, err)
		}
		if reply != "echo:"+want {
			t.Fatalf("frame %d came back as %q, want %q", i, reply, "echo:"+want)
		}
	}
}
