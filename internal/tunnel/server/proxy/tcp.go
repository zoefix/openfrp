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

type tcpProxy struct {
	name   string
	source WorkConnSource
	logger *slog.Logger

	bindAddr    string
	port        int
	acceptLoops int
	reusePort   bool
	recorder    Recorder
	limits      Limits

	listenOpts netutil.ListenOptions

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
		recorder:    opts.Recorder,
		limits:      opts.Limits,
	}, nil
}

func (p *tcpProxy) Name() string { return p.name }

func (p *tcpProxy) RemotePort() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.port
}

func (p *tcpProxy) Listen(ctx context.Context) error {
	addr := net.JoinHostPort(p.bindAddr, strconv.Itoa(p.port))

	p.listenOpts = netutil.ListenOptions{ReusePort: p.reusePort}

	ln, err := netutil.Listen(ctx, "tcp", addr, p.listenOpts, p.acceptLoops)
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

	go func() {
		<-ctx.Done()
		p.Close()
	}()

	var wg sync.WaitGroup
	defer wg.Wait()

	err := netutil.Serve(ln, func(userConn net.Conn) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			p.handle(ctx, userConn)
		}()
	})
	if err != nil {
		if ctx.Err() != nil || errors.Is(err, net.ErrClosed) {
			return nil
		}
		return fmt.Errorf("proxy %q: accept: %w", p.name, err)
	}
	return nil
}

func (p *tcpProxy) handle(ctx context.Context, userConn net.Conn) {
	defer userConn.Close()

	source := userConn.RemoteAddr().String()

	// Checked before a work connection is spent: a visitor turned away after
	// the handoff has already cost the client a dial for nothing.
	if p.limits != nil && p.limits.Exhausted(p.name) {
		p.logger.Warn("tunnel has spent its traffic quota", "source", source)
		return
	}

	workConn, err := p.source.GetWorkConn(ctx, p.name, source)
	if err != nil {
		p.logger.Warn("no work connection available", "source", source, "error", err)
		return
	}
	defer workConn.Close()

	if err := netutil.TuneAccepted(userConn, p.listenOpts); err != nil {
		p.logger.Debug("tune user connection", "error", err)
	}

	var toClient, toVisitor *netutil.Limiter
	if p.limits != nil {
		toClient, toVisitor = p.limits.Rates(p.name)
	}
	transferred := netutil.RelayLimited(userConn, workConn, toClient, toVisitor)

	if p.limits != nil {
		p.limits.Spend(p.name, transferred.AToB+transferred.BToA)
	}

	if p.recorder != nil {
		p.recorder.RecordTransfer(p.name,
			transferred.AToB, transferred.BToA, transferred.Spliced)
	}

	if p.logger.Enabled(ctx, slog.LevelDebug) {
		p.logger.Debug("connection closed",
			"source", source,
			"to_client", transferred.AToB,
			"to_user", transferred.BToA,
			"spliced", transferred.Spliced)
	}
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

func (p *tcpProxy) Listener() net.Listener {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.listener
}

type binder interface {
	Listen(ctx context.Context) error
}

var _ binder = (*tcpProxy)(nil)

func Bind(ctx context.Context, p Proxy) error {
	if b, ok := p.(binder); ok {
		return b.Listen(ctx)
	}
	return nil
}
