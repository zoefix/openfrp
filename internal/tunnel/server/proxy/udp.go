package proxy

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"strconv"
	"sync"
	"time"

	"github.com/zoefix/openfrp/internal/tunnel/protocol"
)

func init() {
	Register("udp", newUDPProxy)
}

// udpSessionTimeout is how long a source address is remembered after its last
// packet.
//
// UDP has no connection to close, so the mapping has to expire on its own.
// Sixty seconds comfortably covers DNS and game traffic while keeping the
// table bounded against a source-address flood.
const udpSessionTimeout = 60 * time.Second

// udpProxy binds a UDP port and carries datagrams over one work connection.
//
// Unlike TCP, every client of a UDP tunnel shares a single work connection:
// there is no per-connection state to give each its own, and opening one work
// connection per source address would be trivially floodable. The framing in
// the protocol package is what keeps them apart.
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

// Listen binds the UDP socket, separately from Run so the allocated port can
// be reported before serving starts.
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

	// One work connection carries every client of this tunnel. It is acquired
	// lazily so an idle UDP tunnel costs nothing, and re-acquired if it drops.
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

// serveOnce runs one work connection until it fails.
func (p *udpProxy) serveOnce(ctx context.Context, conn *net.UDPConn) error {
	// Wait for the first datagram before spending a work connection, so an
	// unused UDP tunnel holds nothing open.
	buf := make([]byte, protocol.MaxUDPPayload)
	n, from, err := conn.ReadFromUDP(buf)
	if err != nil {
		return err
	}

	workConn, err := p.source.GetWorkConn(ctx, p.name, from.String())
	if err != nil {
		return fmt.Errorf("no work connection: %w", err)
	}
	defer workConn.Close()

	sessions := &udpSessions{addrs: map[string]*net.UDPAddr{}}
	sessions.remember(from)

	if err := protocol.WriteUDPPacket(workConn, protocol.UDPPacket{
		Addr:    from.String(),
		Payload: buf[:n],
	}); err != nil {
		return err
	}

	var wg sync.WaitGroup
	wg.Add(1)

	// Replies from the client, fanned back out to the right source.
	go func() {
		defer wg.Done()
		defer workConn.Close()

		for {
			packet, err := protocol.ReadUDPPacket(workConn)
			if err != nil {
				return
			}
			target := sessions.lookup(packet.Addr)
			if target == nil {
				// The source expired or was never seen. Dropping is correct:
				// there is nowhere to send it, and UDP permits loss.
				p.logger.Debug("dropping reply for an unknown source",
					"addr", packet.Addr)
				continue
			}
			if _, err := conn.WriteToUDP(packet.Payload, target); err != nil {
				return
			}
		}
	}()

	// Inbound datagrams, forwarded over the work connection.
	for {
		n, from, err := conn.ReadFromUDP(buf)
		if err != nil {
			workConn.Close()
			wg.Wait()
			return err
		}

		sessions.remember(from)

		if err := protocol.WriteUDPPacket(workConn, protocol.UDPPacket{
			Addr:    from.String(),
			Payload: buf[:n],
		}); err != nil {
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

// udpSessions maps a source address string back to the address to reply to,
// expiring entries that have gone quiet.
type udpSessions struct {
	mu    sync.Mutex
	addrs map[string]*net.UDPAddr
	seen  map[string]time.Time
}

func (s *udpSessions) remember(addr *net.UDPAddr) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.seen == nil {
		s.seen = map[string]time.Time{}
	}
	key := addr.String()
	s.addrs[key] = addr
	s.seen[key] = time.Now()

	// Sweep opportunistically rather than on a timer: the table only grows
	// when packets arrive, so that is the only moment it needs pruning.
	if len(s.addrs) > 64 {
		cutoff := time.Now().Add(-udpSessionTimeout)
		for candidate, last := range s.seen {
			if last.Before(cutoff) {
				delete(s.addrs, candidate)
				delete(s.seen, candidate)
			}
		}
	}
}

func (s *udpSessions) lookup(key string) *net.UDPAddr {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.addrs[key]
}

var _ binder = (*udpProxy)(nil)
