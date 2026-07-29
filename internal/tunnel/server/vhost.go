package server

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"html/template"
	"log/slog"
	"net"
	"strconv"
	"sync"
	"time"

	"github.com/zoefix/openfrp/internal/stats"
	"github.com/zoefix/openfrp/internal/tunnel/vhost"
	"github.com/zoefix/openfrp/pkg/netutil"
)

const vhostSniffTimeout = 15 * time.Second

type vhostListener struct {
	scheme   vhost.Scheme
	port     int
	bindAddr string

	router     *vhost.Router
	registry   *Registry
	certs      *CertStore
	stats      *stats.Registry
	challenges *ChallengeStore
	logger     *slog.Logger

	acceptLoops int
	reusePort   bool

	listenOpts netutil.ListenOptions

	mu       sync.Mutex
	listener net.Listener
}

func (v *vhostListener) listen(ctx context.Context) error {
	addr := net.JoinHostPort(v.bindAddr, strconv.Itoa(v.port))

	v.listenOpts = netutil.ListenOptions{
		ReusePort: v.reusePort,
		KeepAlive: 30 * time.Second,

		DeferAccept: 5 * time.Second,
	}

	ln, err := netutil.Listen(ctx, "tcp", addr, v.listenOpts, v.acceptLoops)
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

	err := netutil.Serve(ln, func(conn net.Conn) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			v.handle(ctx, conn)
		}()
	})
	if err != nil {
		if ctx.Err() != nil || errors.Is(err, net.ErrClosed) {
			return nil
		}
		return fmt.Errorf("server: %s vhost accept: %w", v.scheme, err)
	}
	return nil
}

func (v *vhostListener) handle(ctx context.Context, userConn net.Conn) {
	defer userConn.Close()

	if err := netutil.TuneAccepted(userConn, v.listenOpts); err != nil {
		v.logger.Debug("tune vhost connection", "error", err)
	}
	if err := userConn.SetDeadline(time.Now().Add(vhostSniffTimeout)); err != nil {
		return
	}

	source := userConn.RemoteAddr().String()

	host, path, consumed, err := v.sniff(userConn)

	consumedPooled := true
	defer func() {
		if consumedPooled {
			vhost.PutConsumed(consumed)
		}
	}()

	if err != nil {
		v.logger.Debug("could not identify connection",
			"scheme", string(v.scheme), "source", source, "error", err)
		v.reject(userConn, statusBadRequest)
		return
	}

	if v.scheme == vhost.SchemeHTTP && v.challenges != nil {
		if keyAuth, ok := v.challenges.Answer(path); ok {
			v.logger.Info("answered an ACME challenge",
				"host", host, "path", path, "source", source)
			answerChallenge(userConn, keyAuth)
			return
		}
	}

	route, found := v.router.Lookup(host)
	if !found {
		v.logger.Debug("no route for host",
			"scheme", string(v.scheme), "host", host, "source", source)
		v.unclaimed(userConn, host)
		return
	}

	session, err := v.registry.Get(route.RunID)
	if err != nil {
		v.logger.Warn("route points at a disconnected client",
			"host", host, "run_id", route.RunID, "proxy", route.ProxyName)
		v.router.RemoveClient(route.RunID)
		v.reject(userConn, statusBadGateway)
		return
	}

	limits := session.TunnelLimits(route.ProxyName)
	if limits.Exhausted() {
		v.logger.Warn("tunnel has spent its traffic quota",
			"host", host, "proxy", route.ProxyName, "source", source)
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

	if v.scheme == vhost.SchemeHTTPS && terminationRoute(route) {
		consumedPooled = false
		v.terminate(ctx, userConn, workConn, consumed, route, host, source, session, limits)
		return
	}

	if len(consumed) > 0 {
		if _, err := workConn.Write(consumed); err != nil {
			v.logger.Warn("replay request head", "host", host, "error", err)
			return
		}
	}

	consumedPooled = false
	vhost.PutConsumed(consumed)

	if err := userConn.SetDeadline(time.Time{}); err != nil {
		return
	}

	toClient, toVisitor := limits.Rates()
	transferred := netutil.RelayWith(userConn, workConn, netutil.RelayOptions{
		AToBLimit: toClient,
		BToALimit: toVisitor,
		Progress: func(toClient, toVisitor int64) {
			session.SpendTraffic(limits, toClient+toVisitor)
			v.progress(route.ProxyName, toClient, toVisitor)
		},
	})
	v.record(route.ProxyName, transferred)

	if v.logger.Enabled(ctx, slog.LevelDebug) {
		v.logger.Debug("vhost connection closed",
			"scheme", string(v.scheme),
			"host", host,
			"proxy", route.ProxyName,
			"source", source,
			"to_client", transferred.AToB,
			"to_user", transferred.BToA,
			"spliced", transferred.Spliced)
	}
}

func (v *vhostListener) progress(proxyName string, in, out int64) {
	if v.stats == nil {
		return
	}
	v.stats.RecordProgress(proxyName, in, out)
}

func (v *vhostListener) record(proxyName string, transferred netutil.RelayStats) {
	if v.stats == nil {
		return
	}
	v.stats.RecordClose(proxyName, transferred.Spliced)
}

func (v *vhostListener) sniff(conn net.Conn) (host, path string, consumed []byte, err error) {
	switch v.scheme {
	case vhost.SchemeHTTP:
		info, err := vhost.SniffHTTP(conn)
		return info.Host, info.Path, info.Consumed, err

	case vhost.SchemeHTTPS:
		info, err := vhost.SniffTLS(conn)
		if err != nil {
			return "", "", info.Consumed, err
		}
		if info.ServerName == "" {
			return "", "", info.Consumed, nil
		}
		return info.ServerName, "", info.Consumed, nil

	default:
		return "", "", nil, fmt.Errorf("server: unknown vhost scheme %q", v.scheme)
	}
}

const (
	statusBadRequest = "400 Bad Request"
	statusNotFound   = "404 Not Found"
	statusBadGateway = "502 Bad Gateway"
)

func (v *vhostListener) unclaimed(conn net.Conn, host string) {
	if v.scheme != vhost.SchemeHTTP {
		return
	}

	shown := host
	if shown == "" {
		shown = "this address"
	}

	body := fmt.Sprintf(`<!doctype html>
<html lang="en">
<head><meta charset="utf-8"><title>No tunnel for %s</title>
<style>
body{font:16px/1.6 system-ui,sans-serif;margin:0;padding:3em 1.5em;color:#222;background:#fafafa}
main{max-width:34em;margin:0 auto}
h1{font-size:1.3em;margin:0 0 .8em}
code{background:#eee;padding:.1em .35em;border-radius:3px}
ul{padding-left:1.2em}li{margin:.4em 0}
p.f{color:#666;font-size:.85em;margin-top:2em}
</style></head>
<body><main>
<h1>This is an OpenFrp server, but no tunnel is serving %s.</h1>
<p>The request arrived here, so DNS is pointing the right way. What is missing
is a tunnel claiming this name.</p>
<ul>
<li>Add the name to a tunnel's <code>domains</code> on the router, and enable it.</li>
<li>A <code>*</code> covers exactly one label: <code>*.example.com</code> serves
<code>www.example.com</code> but not <code>a.b.example.com</code>.</li>
<li>Check the router is connected — a tunnel whose client is offline claims nothing.</li>
</ul>
<p class="f">OpenFrp</p>
</main></body></html>
`, template.HTMLEscapeString(shown), template.HTMLEscapeString(shown))

	fmt.Fprintf(conn,
		"HTTP/1.1 %s\r\nContent-Type: text/html; charset=utf-8\r\n"+
			"Content-Length: %d\r\nConnection: close\r\n\r\n%s",
		statusNotFound, len(body), body)
}

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

func (v *vhostListener) addr() net.Addr {
	v.mu.Lock()
	defer v.mu.Unlock()

	if v.listener == nil {
		return nil
	}
	return v.listener.Addr()
}

func newVhostListener(scheme vhost.Scheme, port int, cfgBindAddr string,
	router *vhost.Router, registry *Registry, certs *CertStore,
	challenges *ChallengeStore, traffic *stats.Registry,
	logger *slog.Logger, acceptLoops int) *vhostListener {
	return &vhostListener{
		scheme:      scheme,
		port:        port,
		bindAddr:    cfgBindAddr,
		router:      router,
		registry:    registry,
		certs:       certs,
		challenges:  challenges,
		stats:       traffic,
		logger:      logger,
		acceptLoops: acceptLoops,
		reusePort:   acceptLoops != 1,
	}
}

func (v *vhostListener) terminate(ctx context.Context, userConn, workConn net.Conn,
	consumed []byte, route *vhost.Route, host, source string,
	session *Session, limits *TunnelLimits) {
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

	toClient, toVisitor := limits.Rates()
	transferred := netutil.RelayWith(tlsConn, workConn, netutil.RelayOptions{
		AToBLimit: toClient,
		BToALimit: toVisitor,
		Progress: func(toClient, toVisitor int64) {
			session.SpendTraffic(limits, toClient+toVisitor)
			v.progress(route.ProxyName, toClient, toVisitor)
		},
	})
	v.record(route.ProxyName, transferred)

	v.logger.Debug("terminated connection closed",
		"host", host, "proxy", route.ProxyName, "source", source,
		"to_client", transferred.AToB, "to_user", transferred.BToA,
		"spliced", transferred.Spliced)
}

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
