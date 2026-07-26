package proxy

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"strconv"
	"sync"

	"github.com/zoefix/openfrp/pkg/netutil"
)

func init() {
	Register("tcp", newTCPProxy)
}

// tcpProxy binds a dedicated port and relays each accepted connection to the
// client over a work connection.
type tcpProxy struct {
	name   string
	source WorkConnSource
	logger *slog.Logger

	bindAddr    string
	port        int
	acceptLoops int
	reusePort   bool

	mu       sync.Mutex
	listener net.Listener
	closed   bool
}

func newTCPProxy(opts Options) (Proxy, error) {
	if opts.Spec.RemotePort < 0 || opts.Spec.RemotePort > 65535 {
		return nil, fmt.Errorf("proxy %q: remote port %d out of range",
			opts.Spec.Name, opts.Spec.RemotePort)
	}

	bindAddr := opts.BindAddr
	if bindAddr == "" {
		bindAddr = "0.0.0.0"
	}

	return &tcpProxy{
		name:        opts.Spec.Name,
		source:      opts.Source,
		logger:      opts.Logger.With("proxy", opts.Spec.Name, "kind", "tcp"),
		bindAddr:    bindAddr,
		port:        opts.Spec.RemotePort,
		acceptLoops: opts.AcceptLoops,
		reusePort:   opts.ReusePort,
	}, nil
}

func (p *tcpProxy) Name() string { return p.name }

// RemotePort reports the bound port. It is only meaningful after Listen, since
// a spec asking for port 0 has the kernel choose one.
func (p *tcpProxy) RemotePort() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.port
}

// Listen binds the port. It is separate from Run so the server can report the
// allocated port in its NewProxyResp before serving begins.
func (p *tcpProxy) Listen(ctx context.Context) error {
	addr := net.JoinHostPort(p.bindAddr, strconv.Itoa(p.port))

	ln, err := netutil.Listen(ctx, "tcp", addr, netutil.ListenOptions{
		ReusePort: p.reusePort,
	}, p.acceptLoops)
	if err != nil {
		return fmt.Errorf("proxy %q: %w", p.name, err)
	}

	bound, ok := ln.Addr().(*net.TCPAddr)
	if !ok {
		ln.Close()
		return fmt.Errorf("proxy %q: unexpected listener address %T", p.name, ln.Addr())
	}

	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		ln.Close()
		return net.ErrClosed
	}
	p.listener = ln
	p.port = bound.Port
	p.mu.Unlock()

	p.logger.Info("tcp proxy listening",
		"addr", ln.Addr().String(),
		"accept_loops", netutil.AcceptLoops(ln))
	return nil
}

func (p *tcpProxy) Run(ctx context.Context) error {
	p.mu.Lock()
	ln := p.listener
	p.mu.Unlock()

	if ln == nil {
		if err := p.Listen(ctx); err != nil {
			return err
		}
		p.mu.Lock()
		ln = p.listener
		p.mu.Unlock()
	}

	// Close the listener on cancellation so the blocking Accept returns.
	go func() {
		<-ctx.Done()
		p.Close()
	}()

	var wg sync.WaitGroup
	defer wg.Wait()

	for {
		userConn, err := ln.Accept()
		if err != nil {
			if ctx.Err() != nil || errors.Is(err, net.ErrClosed) {
				return nil
			}
			return fmt.Errorf("proxy %q: accept: %w", p.name, err)
		}

		wg.Add(1)
		go func() {
			defer wg.Done()
			p.handle(ctx, userConn)
		}()
	}
}

// handle joins one user connection to a work connection.
func (p *tcpProxy) handle(ctx context.Context, userConn net.Conn) {
	defer userConn.Close()

	source := userConn.RemoteAddr().String()

	workConn, err := p.source.GetWorkConn(ctx, p.name, source)
	if err != nil {
		p.logger.Warn("no work connection available", "source", source, "error", err)
		return
	}
	defer workConn.Close()

	if err := netutil.TuneConn(userConn, netutil.DefaultTCPOptions()); err != nil {
		p.logger.Debug("tune user connection", "error", err)
	}

	stats := netutil.Relay(userConn, workConn)

	p.logger.Debug("connection closed",
		"source", source,
		"to_client", stats.AToB,
		"to_user", stats.BToA,
		"spliced", stats.Spliced)
}

func (p *tcpProxy) Close() error {
	p.mu.Lock()
	defer p.mu.Unlock()

	p.closed = true
	if p.listener == nil {
		return nil
	}
	err := p.listener.Close()
	p.listener = nil
	return err
}

// Listener exposes the bound listener for tests.
func (p *tcpProxy) Listener() net.Listener {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.listener
}

// binder is implemented by proxies that must bind before serving, so the
// server can allocate and report a port ahead of Run.
type binder interface {
	Listen(ctx context.Context) error
}

var _ binder = (*tcpProxy)(nil)

// Bind binds p if its kind needs a port reserved before serving.
func Bind(ctx context.Context, p Proxy) error {
	if b, ok := p.(binder); ok {
		return b.Listen(ctx)
	}
	return nil
}
