package server

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"strconv"
	"sync"
	"time"

	"github.com/zoefix/openfrp/internal/tunnel/vhost"
	"github.com/zoefix/openfrp/pkg/netutil"
)

// vhostSniffTimeout bounds how long we will wait for a client to send enough
// to identify itself. It must be cleared before relaying begins.
const vhostSniffTimeout = 15 * time.Second

// vhostListener serves one shared domain-routed port.
//
// One of these covers every http tunnel across every client, and another
// covers every https tunnel. Routing happens per connection against the
// atomic route table, so publishing or withdrawing a tunnel never disturbs
// traffic already in flight and never requires a restart.
type vhostListener struct {
	scheme   vhost.Scheme
	port     int
	bindAddr string

	router   *vhost.Router
	registry *Registry
	certs    *CertStore
	logger   *slog.Logger

	acceptLoops int
	reusePort   bool

	mu       sync.Mutex
	listener net.Listener
}

func (v *vhostListener) listen(ctx context.Context) error {
	addr := net.JoinHostPort(v.bindAddr, strconv.Itoa(v.port))

	ln, err := netutil.Listen(ctx, "tcp", addr, netutil.ListenOptions{
		ReusePort: v.reusePort,
		KeepAlive: 30 * time.Second,
	}, v.acceptLoops)
	if err != nil {
		return fmt.Errorf("server: %s vhost listener: %w", v.scheme, err)
	}

	v.mu.Lock()
	v.listener = ln
	v.mu.Unlock()

	v.logger.Info("vhost listener started",
		"scheme", string(v.scheme),
		"addr", ln.Addr().String(),
		"accept_loops", netutil.AcceptLoops(ln))
	return nil
}

func (v *vhostListener) serve(ctx context.Context) error {
	v.mu.Lock()
	ln := v.listener
	v.mu.Unlock()

	if ln == nil {
		return fmt.Errorf("server: %s vhost listener was not bound", v.scheme)
	}

	go func() {
		<-ctx.Done()
		v.close()
	}()

	var wg sync.WaitGroup
	defer wg.Wait()

	for {
		conn, err := ln.Accept()
		if err != nil {
			if ctx.Err() != nil || errors.Is(err, net.ErrClosed) {
				return nil
			}
			return fmt.Errorf("server: %s vhost accept: %w", v.scheme, err)
		}

		wg.Add(1)
		go func() {
			defer wg.Done()
			v.handle(ctx, conn)
		}()
	}
}

// handle identifies, routes and relays one inbound connection.
func (v *vhostListener) handle(ctx context.Context, userConn net.Conn) {
	defer userConn.Close()

	if err := netutil.TuneConn(userConn, netutil.DefaultTCPOptions()); err != nil {
		v.logger.Debug("tune vhost connection", "error", err)
	}
	if err := userConn.SetDeadline(time.Now().Add(vhostSniffTimeout)); err != nil {
		return
	}

	source := userConn.RemoteAddr().String()

	host, consumed, err := v.sniff(userConn)
	if err != nil {
		v.logger.Debug("could not identify connection",
			"scheme", string(v.scheme), "source", source, "error", err)
		v.reject(userConn, statusBadRequest)
		return
	}

	route, found := v.router.Lookup(host)
	if !found {
		v.logger.Debug("no route for host",
			"scheme", string(v.scheme), "host", host, "source", source)
		v.reject(userConn, statusNotFound)
		return
	}

	session, err := v.registry.Get(route.RunID)
	if err != nil {
		// The route outlived its client by a moment. Withdraw it so the next
		// request does not repeat the work.
		v.logger.Warn("route points at a disconnected client",
			"host", host, "run_id", route.RunID, "proxy", route.ProxyName)
		v.router.RemoveClient(route.RunID)
		v.reject(userConn, statusBadGateway)
		return
	}

	workConn, err := session.GetWorkConn(ctx, route.ProxyName, source)
	if err != nil {
		v.logger.Warn("no work connection for vhost request",
			"host", host, "proxy", route.ProxyName, "error", err)
		v.reject(userConn, statusBadGateway)
		return
	}
	defer workConn.Close()

	// Edge termination is a different shape entirely: instead of forwarding
	// ciphertext, decrypt here and send plaintext down the tunnel. It costs
	// splice(2) for this connection — a tls.Conn is not a *net.TCPConn — which
	// is exactly why passthrough remains the default and this is opt-in.
	if v.scheme == vhost.SchemeHTTPS && terminationRoute(route) {
		v.terminate(ctx, userConn, workConn, consumed, route, host, source)
		return
	}

	// Replay what the sniffer consumed. This is the step that lets both sides
	// stay bare sockets: the alternative — wrapping userConn in a reader that
	// replays — would hide the *net.TCPConn and cost splice(2) for every byte
	// of the transfer, not just the head.
	if len(consumed) > 0 {
		if _, err := workConn.Write(consumed); err != nil {
			v.logger.Warn("replay request head", "host", host, "error", err)
			return
		}
	}

	// The transfer may be long lived, so the sniff deadline has to go.
	if err := userConn.SetDeadline(time.Time{}); err != nil {
		return
	}

	stats := netutil.Relay(userConn, workConn)

	v.logger.Debug("vhost connection closed",
		"scheme", string(v.scheme),
		"host", host,
		"proxy", route.ProxyName,
		"source", source,
		"to_client", stats.AToB,
		"to_user", stats.BToA,
		"spliced", stats.Spliced)
}

// sniff recovers the target host and the bytes consumed doing so.
func (v *vhostListener) sniff(conn net.Conn) (host string, consumed []byte, err error) {
	switch v.scheme {
	case vhost.SchemeHTTP:
		info, err := vhost.SniffHTTP(conn)
		return info.Host, info.Consumed, err

	case vhost.SchemeHTTPS:
		info, err := vhost.SniffTLS(conn)
		if err != nil {
			return "", info.Consumed, err
		}
		if info.ServerName == "" {
			// No SNI. Only a catch-all route can serve this, and Lookup will
			// find it if one exists.
			return "", info.Consumed, nil
		}
		return info.ServerName, info.Consumed, nil

	default:
		return "", nil, fmt.Errorf("server: unknown vhost scheme %q", v.scheme)
	}
}

// Minimal responses for the failure paths. Kept deliberately terse: this is an
// edge proxy, and a verbose error page would leak topology to anyone probing.
const (
	statusBadRequest = "400 Bad Request"
	statusNotFound   = "404 Not Found"
	statusBadGateway = "502 Bad Gateway"
)

// reject answers a failed request. Only HTTP gets a response body — speaking
// HTTP over a port the client opened for TLS would just produce a confusing
// protocol error, so HTTPS connections are simply closed.
func (v *vhostListener) reject(conn net.Conn, status string) {
	if v.scheme != vhost.SchemeHTTP {
		return
	}
	fmt.Fprintf(conn, "HTTP/1.1 %s\r\nContent-Length: 0\r\nConnection: close\r\n\r\n", status)
}

func (v *vhostListener) close() error {
	v.mu.Lock()
	defer v.mu.Unlock()

	if v.listener == nil {
		return nil
	}
	err := v.listener.Close()
	v.listener = nil
	return err
}

// addr reports the bound address, or nil before listen.
func (v *vhostListener) addr() net.Addr {
	v.mu.Lock()
	defer v.mu.Unlock()

	if v.listener == nil {
		return nil
	}
	return v.listener.Addr()
}

// newVhostListener builds a listener for one scheme.
func newVhostListener(scheme vhost.Scheme, port int, cfgBindAddr string,
	router *vhost.Router, registry *Registry, certs *CertStore,
	logger *slog.Logger, acceptLoops int) *vhostListener {

	return &vhostListener{
		scheme:      scheme,
		port:        port,
		bindAddr:    cfgBindAddr,
		router:      router,
		registry:    registry,
		certs:       certs,
		logger:      logger,
		acceptLoops: acceptLoops,
		reusePort:   acceptLoops != 1,
	}
}

// terminate decrypts at the edge and forwards plaintext to the tunnel.
//
// The ClientHello the sniffer already consumed has to be replayed into the TLS
// server, since the handshake cannot start without it. That is what replayConn
// exists for — and note it deliberately does NOT implement netutil.Unwrapper,
// because handing the raw socket to splice would bypass the decryption this
// whole path exists to perform.
func (v *vhostListener) terminate(ctx context.Context, userConn, workConn net.Conn,
	consumed []byte, route *vhost.Route, host, source string) {

	if v.certs == nil || !v.certs.Has(host) {
		v.logger.Warn("edge termination requested but no certificate covers the host",
			"host", host, "proxy", route.ProxyName)
		return
	}

	tlsConn := tls.Server(&replayConn{Conn: userConn, replay: consumed}, v.certs.TLSConfig())

	if err := tlsConn.SetDeadline(time.Now().Add(vhostSniffTimeout)); err != nil {
		return
	}
	if err := tlsConn.HandshakeContext(ctx); err != nil {
		v.logger.Debug("TLS handshake failed", "host", host, "error", err)
		return
	}
	if err := tlsConn.SetDeadline(time.Time{}); err != nil {
		return
	}

	stats := netutil.Relay(tlsConn, workConn)

	v.logger.Debug("terminated connection closed",
		"host", host, "proxy", route.ProxyName, "source", source,
		"to_client", stats.AToB, "to_user", stats.BToA, "spliced", stats.Spliced)
}

// replayConn re-serves bytes already read from the connection.
//
// It intentionally omits netutil.Unwrapper. A transparent wrapper may expose
// the socket underneath so the relay can splice it; this one transforms the
// stream by replaying a prefix, and the TLS layer above it transforms it
// further, so exposing the raw socket would skip both.
type replayConn struct {
	net.Conn
	replay []byte
}

func (c *replayConn) Read(p []byte) (int, error) {
	if len(c.replay) > 0 {
		n := copy(p, c.replay)
		c.replay = c.replay[n:]
		return n, nil
	}
	return c.Conn.Read(p)
}
