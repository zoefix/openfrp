package proxy

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/netip"
	"strconv"
	"sync"
	"time"

	"github.com/zoefix/openfrp/internal/tunnel/protocol"
)

func init() {
	Register("udp", newUDPProxy)
}

const udpSessionTimeout = 60 * time.Second

type udpProxy struct {
	name   string
	source WorkConnSource
	logger *slog.Logger

	bindAddr string
	port     int

	mu     sync.Mutex
	conn   *net.UDPConn
	closed bool
}

func newUDPProxy(opts Options) (Proxy, error) {
	if opts.Spec.RemotePort < 0 || opts.Spec.RemotePort > 65535 {
		return nil, fmt.Errorf("proxy %q: remote port %d out of range",
			opts.Spec.Name, opts.Spec.RemotePort)
	}

	bindAddr := opts.BindAddr
	if bindAddr == "" {
		bindAddr = "0.0.0.0"
	}

	return &udpProxy{
		name:     opts.Spec.Name,
		source:   opts.Source,
		logger:   opts.Logger.With("proxy", opts.Spec.Name, "kind", "udp"),
		bindAddr: bindAddr,
		port:     opts.Spec.RemotePort,
	}, nil
}

func (p *udpProxy) Name() string { return p.name }

func (p *udpProxy) RemotePort() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.port
}

func (p *udpProxy) Listen(context.Context) error {
	addr, err := net.ResolveUDPAddr("udp",
		net.JoinHostPort(p.bindAddr, strconv.Itoa(p.port)))
	if err != nil {
		return fmt.Errorf("proxy %q: resolve udp address: %w", p.name, err)
	}

	conn, err := net.ListenUDP("udp", addr)
	if err != nil {
		return fmt.Errorf("proxy %q: listen udp: %w", p.name, err)
	}

	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		conn.Close()
		return net.ErrClosed
	}
	p.conn = conn
	p.port = conn.LocalAddr().(*net.UDPAddr).Port
	p.mu.Unlock()

	p.logger.Info("udp proxy listening", "addr", conn.LocalAddr().String())
	return nil
}

func (p *udpProxy) Run(ctx context.Context) error {
	p.mu.Lock()
	conn := p.conn
	p.mu.Unlock()

	if conn == nil {
		if err := p.Listen(ctx); err != nil {
			return err
		}
		p.mu.Lock()
		conn = p.conn
		p.mu.Unlock()
	}

	go func() {
		<-ctx.Done()
		p.Close()
	}()

	for ctx.Err() == nil {
		if err := p.serveOnce(ctx, conn); err != nil {
			if ctx.Err() != nil || errors.Is(err, net.ErrClosed) {
				return nil
			}
			p.logger.Warn("udp session ended, reconnecting", "error", err)

			select {
			case <-ctx.Done():
				return nil
			case <-time.After(time.Second):
			}
		}
	}
	return nil
}

func (p *udpProxy) serveOnce(ctx context.Context, conn *net.UDPConn) error {

	buf := make([]byte, protocol.MaxUDPPayload)
	n, from, err := conn.ReadFromUDPAddrPort(buf)
	if err != nil {
		return err
	}

	sessions := newUDPSessions()
	firstAddr := sessions.remember(from)

	workConn, err := p.source.GetWorkConn(ctx, p.name, firstAddr)
	if err != nil {
		return fmt.Errorf("no work connection: %w", err)
	}
	defer workConn.Close()

	frame := make([]byte, 0, protocol.MaxUDPFrame)
	writeInbound := func(addr string, payload []byte) error {
		framed, err := protocol.AppendUDPPacket(frame[:0], addr, payload)
		if err != nil {
			return err
		}
		_, err = workConn.Write(framed)
		return err
	}

	if err := writeInbound(firstAddr, buf[:n]); err != nil {
		return err
	}

	var wg sync.WaitGroup
	wg.Add(1)

	go func() {
		defer wg.Done()
		defer workConn.Close()

		scratch := make([]byte, protocol.MaxUDPFrame)
		for {
			addr, payload, err := protocol.ReadUDPPacketInto(workConn, scratch)
			if err != nil {
				return
			}
			target, ok := sessions.lookup(addr)
			if !ok {

				p.logger.Debug("dropping reply for an unknown source",
					"addr", string(addr))
				continue
			}
			if _, err := conn.WriteToUDPAddrPort(payload, target); err != nil {
				return
			}
		}
	}()

	for {
		n, from, err := conn.ReadFromUDPAddrPort(buf)
		if err != nil {
			workConn.Close()
			wg.Wait()
			return err
		}

		if err := writeInbound(sessions.remember(from), buf[:n]); err != nil {
			wg.Wait()
			return err
		}
	}
}

func (p *udpProxy) Close() error {
	p.mu.Lock()
	defer p.mu.Unlock()

	p.closed = true
	if p.conn == nil {
		return nil
	}
	err := p.conn.Close()
	p.conn = nil
	return err
}

type udpSessions struct {
	mu     sync.Mutex
	byAddr map[netip.AddrPort]*udpPeer
	byStr  map[string]*udpPeer
}

type udpPeer struct {
	addr netip.AddrPort
	str  string
	last time.Time
}

func newUDPSessions() *udpSessions {
	return &udpSessions{
		byAddr: map[netip.AddrPort]*udpPeer{},
		byStr:  map[string]*udpPeer{},
	}
}

func (s *udpSessions) remember(addr netip.AddrPort) string {

	if addr.Addr().Is4In6() {
		addr = netip.AddrPortFrom(addr.Addr().Unmap(), addr.Port())
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	if peer, ok := s.byAddr[addr]; ok {
		peer.last = time.Now()
		return peer.str
	}

	peer := &udpPeer{addr: addr, str: addr.String(), last: time.Now()}
	s.byAddr[addr] = peer
	s.byStr[peer.str] = peer

	if len(s.byAddr) > 64 {
		cutoff := time.Now().Add(-udpSessionTimeout)
		for _, candidate := range s.byAddr {
			if candidate.last.Before(cutoff) {
				delete(s.byAddr, candidate.addr)
				delete(s.byStr, candidate.str)
			}
		}
	}
	return peer.str
}

func (s *udpSessions) lookup(key []byte) (netip.AddrPort, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()

	peer, ok := s.byStr[string(key)]
	if !ok {
		return netip.AddrPort{}, false
	}
	return peer.addr, true
}

var _ binder = (*udpProxy)(nil)
