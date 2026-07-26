package e2e

import (
	"context"
	"crypto/tls"
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/zoefix/openfrp/internal/config"
	"github.com/zoefix/openfrp/internal/tunnel/client"
	"github.com/zoefix/openfrp/internal/tunnel/server"
	"github.com/zoefix/openfrp/internal/tunnel/vhost"
	"github.com/zoefix/openfrp/pkg/log"
)

// freePort reserves an ephemeral port and releases it, so the caller can bind
// it deliberately. Vhost ports cannot be requested as zero — zero means the
// listener is disabled — so the port has to be chosen up front.
func freePort(t *testing.T) int {
	t.Helper()

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserve port: %v", err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	ln.Close()
	return port
}

// startHTTPService runs a backend that identifies itself in the body, so a
// test can tell which tunnel a request actually reached.
func startHTTPService(t *testing.T, name string) (host string, port int) {
	t.Helper()

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}

	srv := &http.Server{
		Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// Echo the Host back too: it must survive the tunnel untouched,
			// since the backend may itself be doing name-based routing.
			fmt.Fprintf(w, "%s|%s|%s", name, r.Host, r.URL.Path)
		}),
	}
	go srv.Serve(ln)
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		srv.Shutdown(ctx)
	})

	addr := ln.Addr().(*net.TCPAddr)
	return "127.0.0.1", addr.Port
}

// vhostHarness is a server with vhost listeners plus a client publishing
// domain-routed tunnels.
type vhostHarness struct {
	server    *server.Server
	httpPort  int
	httpsPort int
}

func startVhost(t *testing.T, tunnels []config.Tunnel) *vhostHarness {
	t.Helper()

	logger := log.Discard()
	if testing.Verbose() {
		logger, _ = log.Setup(log.Options{Level: "debug"})
	}

	httpPort := freePort(t)
	httpsPort := freePort(t)

	serverCfg := &config.Server{
		BindAddr:       "127.0.0.1",
		Token:          testToken,
		VhostHTTPPort:  httpPort,
		VhostHTTPSPort: httpsPort,
		AcceptLoops:    1,
	}
	serverCfg.ApplyDefaults()
	serverCfg.BindPort = 0
	if err := serverCfg.Validate(); err != nil {
		t.Fatalf("server config: %v", err)
	}

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
		Name:       "vhost-client",
		Transport:  config.Transport{PoolCount: 8},
		Tunnels:    tunnels,
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

	h := &vhostHarness{server: srv, httpPort: httpPort, httpsPort: httpsPort}
	h.waitForRoutes(t, len(tunnels))
	return h
}

// waitForRoutes blocks until every tunnel has claimed its domains.
func (h *vhostHarness) waitForRoutes(t *testing.T, want int) {
	t.Helper()

	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if h.server.Router().Len() >= want {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("only %d routes registered within 10s, want at least %d",
		h.server.Router().Len(), want)
}

// get issues an HTTP request through the vhost listener with an explicit Host.
func (h *vhostHarness) get(t *testing.T, host, path string) (status int, body string) {
	t.Helper()

	addr := net.JoinHostPort("127.0.0.1", strconv.Itoa(h.httpPort))

	// A plain client with no redirect handling and no connection reuse, so
	// each call exercises the routing path from scratch.
	transport := &http.Transport{DisableKeepAlives: true}
	httpClient := &http.Client{Transport: transport, Timeout: 10 * time.Second}

	req, err := http.NewRequest(http.MethodGet, "http://"+addr+path, nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Host = host

	resp, err := httpClient.Do(req)
	if err != nil {
		t.Fatalf("GET %s%s: %v", host, path, err)
	}
	defer resp.Body.Close()

	payload, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	return resp.StatusCode, string(payload)
}

// TestVhostRoutesEveryDomainFormat is the P1 acceptance test. It registers all
// four shapes the project promised — bare, exact subdomain, wildcard, and a
// deeper wildcard — and confirms each request lands on the right tunnel.
func TestVhostRoutesEveryDomainFormat(t *testing.T) {
	type backend struct {
		name    string
		domains []string
	}
	backends := []backend{
		{"bare", []string{"aaa.com"}},
		{"exact", []string{"www.aaa.com"}},
		{"wild", []string{"*.aaa.com"}},
		{"deep", []string{"*.bb.aaa.com"}},
	}

	var tunnels []config.Tunnel
	for _, b := range backends {
		host, port := startHTTPService(t, b.name)
		tunnels = append(tunnels, config.Tunnel{
			Name:      b.name,
			Enabled:   true,
			Type:      config.TunnelHTTP,
			LocalIP:   host,
			LocalPort: port,
			Domains:   b.domains,
		})
	}

	h := startVhost(t, tunnels)

	tests := []struct {
		host string
		want string
	}{
		{"aaa.com", "bare"},
		{"AAA.COM", "bare"},      // case-insensitive
		{"aaa.com.", "bare"},     // trailing dot
		{"www.aaa.com", "exact"}, // exact beats wildcard
		{"other.aaa.com", "wild"},
		{"bb.aaa.com", "wild"},     // one label deep
		{"x.bb.aaa.com", "deep"},   // deeper wildcard wins
		{"www.bb.aaa.com", "deep"}, // depth decides, not the label
	}

	for _, tc := range tests {
		t.Run(tc.host, func(t *testing.T) {
			status, body := h.get(t, tc.host, "/probe")
			if status != http.StatusOK {
				t.Fatalf("status = %d, want 200 (body %q)", status, body)
			}
			parts := strings.Split(body, "|")
			if len(parts) != 3 {
				t.Fatalf("unexpected body %q", body)
			}
			if parts[0] != tc.want {
				t.Errorf("%s reached backend %q, want %q", tc.host, parts[0], tc.want)
			}
			// The Host header must survive untouched: nothing is rewritten.
			if !strings.EqualFold(strings.TrimSuffix(parts[1], "."), strings.TrimSuffix(tc.host, ".")) {
				t.Errorf("backend saw Host %q, want %q unchanged", parts[1], tc.host)
			}
			if parts[2] != "/probe" {
				t.Errorf("backend saw path %q, want /probe", parts[2])
			}
		})
	}
}

// TestVhostWildcardDoesNotCrossLabels is the frp-divergent behaviour, verified
// through the full stack rather than only at the router.
func TestVhostWildcardDoesNotCrossLabels(t *testing.T) {
	host, port := startHTTPService(t, "wild")

	h := startVhost(t, []config.Tunnel{{
		Name:      "wild",
		Enabled:   true,
		Type:      config.TunnelHTTP,
		LocalIP:   host,
		LocalPort: port,
		Domains:   []string{"*.aaa.com"},
	}})

	if status, _ := h.get(t, "one.aaa.com", "/"); status != http.StatusOK {
		t.Errorf("one.aaa.com: status = %d, want 200", status)
	}

	// Two labels deep must NOT match, and with no catch-all it is a 404.
	for _, host := range []string{"x.bb.aaa.com", "a.b.c.aaa.com", "aaa.com"} {
		if status, _ := h.get(t, host, "/"); status != http.StatusNotFound {
			t.Errorf("%s: status = %d, want 404 — a '*' label must not cross levels",
				host, status)
		}
	}
}

func TestVhostUnroutedHostGets404(t *testing.T) {
	host, port := startHTTPService(t, "only")

	h := startVhost(t, []config.Tunnel{{
		Name:      "only",
		Enabled:   true,
		Type:      config.TunnelHTTP,
		LocalIP:   host,
		LocalPort: port,
		Domains:   []string{"aaa.com"},
	}})

	if status, _ := h.get(t, "unrelated.example.org", "/"); status != http.StatusNotFound {
		t.Errorf("status = %d, want 404", status)
	}
}

// TestVhostCatchAllServesEverythingElse confirms the opt-in fallback works
// through the stack.
func TestVhostCatchAllServesEverythingElse(t *testing.T) {
	exactHost, exactPort := startHTTPService(t, "exact")
	anyHost, anyPort := startHTTPService(t, "catchall")

	h := startVhost(t, []config.Tunnel{
		{
			Name: "exact", Enabled: true, Type: config.TunnelHTTP,
			LocalIP: exactHost, LocalPort: exactPort,
			Domains: []string{"aaa.com"},
		},
		{
			Name: "catchall", Enabled: true, Type: config.TunnelHTTP,
			LocalIP: anyHost, LocalPort: anyPort,
			Domains: []string{"*"},
		},
	})

	if _, body := h.get(t, "aaa.com", "/"); !strings.HasPrefix(body, "exact|") {
		t.Errorf("aaa.com body = %q, want the exact backend", body)
	}
	if _, body := h.get(t, "whatever.example.org", "/"); !strings.HasPrefix(body, "catchall|") {
		t.Errorf("unmatched host body = %q, want the catch-all backend", body)
	}
}

// TestVhostBodyRoundTrip pushes a request body and a large response through
// the vhost path, which is where losing sniffed bytes would show up.
func TestVhostBodyRoundTrip(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}

	const responseSize = 512 << 10
	srv := &http.Server{
		Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			body, _ := io.ReadAll(r.Body)
			// Reflect the request body length so a truncated head or body is
			// immediately visible.
			fmt.Fprintf(w, "received=%d\n", len(body))
			w.Write(make([]byte, responseSize))
		}),
	}
	go srv.Serve(ln)
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		srv.Shutdown(ctx)
	})

	addr := ln.Addr().(*net.TCPAddr)
	h := startVhost(t, []config.Tunnel{{
		Name: "upload", Enabled: true, Type: config.TunnelHTTP,
		LocalIP: "127.0.0.1", LocalPort: addr.Port,
		Domains: []string{"upload.aaa.com"},
	}})

	requestBody := strings.Repeat("x", 64<<10)
	req, err := http.NewRequest(http.MethodPost,
		fmt.Sprintf("http://127.0.0.1:%d/", h.httpPort), strings.NewReader(requestBody))
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Host = "upload.aaa.com"

	httpClient := &http.Client{
		Transport: &http.Transport{DisableKeepAlives: true},
		Timeout:   30 * time.Second,
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	defer resp.Body.Close()

	payload, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read response: %v", err)
	}

	wantPrefix := fmt.Sprintf("received=%d\n", len(requestBody))
	if !strings.HasPrefix(string(payload), wantPrefix) {
		t.Errorf("response starts %q, want %q — request body was corrupted",
			payload[:min(len(payload), 40)], wantPrefix)
	}
	if got := len(payload) - len(wantPrefix); got != responseSize {
		t.Errorf("response payload = %d bytes, want %d", got, responseSize)
	}
}

// TestVhostHTTPSPassthroughRoutesBySNI drives a real TLS client through the
// https listener. The server must route on the SNI without decrypting: the
// certificate is presented by the backend and validated end to end by the
// client, which can only succeed if the ciphertext was forwarded untouched.
func TestVhostHTTPSPassthroughRoutesBySNI(t *testing.T) {
	cert, pool := selfSignedCert(t, "secure.aaa.com")

	ln, err := tls.Listen("tcp", "127.0.0.1:0", &tls.Config{
		Certificates: []tls.Certificate{cert},
	})
	if err != nil {
		t.Fatalf("tls listen: %v", err)
	}

	srv := &http.Server{
		Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			fmt.Fprintf(w, "tls-backend|%s", r.Host)
		}),
	}
	go srv.Serve(ln)
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		srv.Shutdown(ctx)
	})

	addr := ln.Addr().(*net.TCPAddr)
	h := startVhost(t, []config.Tunnel{{
		Name: "secure", Enabled: true, Type: config.TunnelHTTPS,
		LocalIP: "127.0.0.1", LocalPort: addr.Port,
		Domains: []string{"secure.aaa.com"},
		TLSMode: config.TLSPassthrough,
	}})

	httpsClient := &http.Client{
		Transport: &http.Transport{
			DisableKeepAlives: true,
			TLSClientConfig:   &tls.Config{RootCAs: pool},
			// Point the connection at the vhost listener while keeping the SNI
			// and certificate validation on the real name.
			DialContext: func(ctx context.Context, network, _ string) (net.Conn, error) {
				d := &net.Dialer{Timeout: 5 * time.Second}
				return d.DialContext(ctx, network,
					net.JoinHostPort("127.0.0.1", strconv.Itoa(h.httpsPort)))
			},
		},
		Timeout: 15 * time.Second,
	}

	resp, err := httpsClient.Get("https://secure.aaa.com/")
	if err != nil {
		t.Fatalf("GET over TLS passthrough: %v", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	if !strings.HasPrefix(string(body), "tls-backend|") {
		t.Errorf("body = %q, want the TLS backend's response", body)
	}
}

// TestVhostRejectsHTTPTunnelWithoutListener confirms the failure is explained
// rather than silent when no vhost port is configured.
func TestVhostRejectsHTTPTunnelWithoutListener(t *testing.T) {
	host, port := startHTTPService(t, "orphan")

	logger := log.Discard()
	serverCfg := &config.Server{BindAddr: "127.0.0.1", Token: testToken, AcceptLoops: 1}
	serverCfg.ApplyDefaults()
	serverCfg.BindPort = 0

	srv, err := server.New(serverCfg, logger, "test")
	if err != nil {
		t.Fatalf("server.New: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := srv.Listen(ctx); err != nil {
		t.Fatalf("Listen: %v", err)
	}
	go srv.Serve(ctx)

	clientCfg := &config.Client{
		ServerAddr: "127.0.0.1",
		ServerPort: srv.Addr().(*net.TCPAddr).Port,
		Token:      testToken,
		Tunnels: []config.Tunnel{{
			Name: "orphan", Enabled: true, Type: config.TunnelHTTP,
			LocalIP: host, LocalPort: port, Domains: []string{"aaa.com"},
		}},
	}
	clientCfg.ApplyDefaults()

	cli, err := client.New(clientCfg, logger, "test")
	if err != nil {
		t.Fatalf("client.New: %v", err)
	}
	runCtx, runCancel := context.WithTimeout(ctx, 3*time.Second)
	defer runCancel()
	go cli.Run(runCtx)
	<-runCtx.Done()

	if srv.Router().Len() != 0 {
		t.Errorf("router holds %d routes, want 0 with no vhost listener",
			srv.Router().Len())
	}
	if addr := srv.VhostAddr(vhost.SchemeHTTP); addr != nil {
		t.Errorf("http vhost listener bound at %s despite no port configured", addr)
	}
}
