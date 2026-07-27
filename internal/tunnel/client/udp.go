package client

import (
	"context"
	"net"
	"strconv"
	"sync"
	"time"

	"github.com/zoefix/openfrp/internal/config"
	"github.com/zoefix/openfrp/internal/tunnel/protocol"
)

// localUDPTimeout expires an idle socket to the LAN target.
const localUDPTimeout = 60 * time.Second

// forwardUDP carries datagrams between the work connection and the local
// service.
//
// One socket per remote source, so replies from the local service can be
// attributed back to whoever sent the request. A single shared socket would
// work only for services that never reply, which is almost none of them.
func (s *session) forwardUDP(ctx context.Context, workConn net.Conn, tunnel config.Tunnel, logger interface {
	Warn(string, ...any)
	Debug(string, ...any)
}) {
	target := net.JoinHostPort(tunnel.LocalIP, strconv.Itoa(tunnel.LocalPort))

	addr, err := net.ResolveUDPAddr("udp", target)
	if err != nil {
		logger.Warn("resolve local udp target", "target", target, "error", err)
		return
	}

	sockets := &udpSockets{
		conns:    map[string]*net.UDPConn{},
		workConn: workConn,
		target:   addr,
		logger:   logger,
	}
	defer sockets.closeAll()

	go func() {
		<-ctx.Done()
		workConn.Close()
	}()

	for {
		packet, err := protocol.ReadUDPPacket(workConn)
		if err != nil {
			return
		}

		conn, err := sockets.get(packet.Addr)
		if err != nil {
			logger.Debug("open local udp socket", "error", err)
			continue
		}
		if _, err := conn.Write(packet.Payload); err != nil {
			logger.Debug("write to local service", "error", err)
			sockets.drop(packet.Addr)
		}
	}
}

// udpSockets keeps one local socket per remote source.
type udpSockets struct {
	mu    sync.Mutex
	conns map[string]*net.UDPConn

	workConn net.Conn
	target   *net.UDPAddr
	logger   interface {
		Warn(string, ...any)
		Debug(string, ...any)
	}
}

func (s *udpSockets) get(source string) (*net.UDPConn, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if conn, ok := s.conns[source]; ok {
		conn.SetReadDeadline(time.Now().Add(localUDPTimeout))
		return conn, nil
	}

	conn, err := net.DialUDP("udp", nil, s.target)
	if err != nil {
		return nil, err
	}
	conn.SetReadDeadline(time.Now().Add(localUDPTimeout))
	s.conns[source] = conn

	// Each socket reads its own replies and tags them with the source they
	// belong to, so the server can fan them back out correctly.
	go s.pump(source, conn)

	return conn, nil
}

func (s *udpSockets) pump(source string, conn *net.UDPConn) {
	buf := make([]byte, protocol.MaxUDPPayload)

	for {
		n, err := conn.Read(buf)
		if err != nil {
			s.drop(source)
			return
		}
		conn.SetReadDeadline(time.Now().Add(localUDPTimeout))

		if err := protocol.WriteUDPPacket(s.workConn, protocol.UDPPacket{
			Addr:    source,
			Payload: buf[:n],
		}); err != nil {
			s.drop(source)
			return
		}
	}
}

func (s *udpSockets) drop(source string) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if conn, ok := s.conns[source]; ok {
		conn.Close()
		delete(s.conns, source)
	}
}

func (s *udpSockets) closeAll() {
	s.mu.Lock()
	defer s.mu.Unlock()

	for source, conn := range s.conns {
		conn.Close()
		delete(s.conns, source)
	}
}
