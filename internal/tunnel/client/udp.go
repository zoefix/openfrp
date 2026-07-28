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
func (s *session) forwardUDP(ctx context.Context, workConn net.Conn,
	tunnel config.Tunnel, proxyName string, logger interface {
		Warn(string, ...any)
		Debug(string, ...any)
	}) {
	target := net.JoinHostPort(tunnel.LocalIP, strconv.Itoa(tunnel.LocalPort))

	addr, err := net.ResolveUDPAddr("udp", target)
	if err != nil {
		logger.Warn("resolve local udp target", "target", target, "error", err)
		return
	}

	traffic := s.client.traffic
	sockets := &udpSockets{
		conns:    map[string]*net.UDPConn{},
		workConn: workConn,
		target:   addr,
		logger:   logger,
		record: func(bytesOut int64) {
			traffic.RecordTransfer(proxyName, 0, bytesOut, false)
		},
	}
	defer sockets.closeAll()

	go func() {
		<-ctx.Done()
		workConn.Close()
	}()

	scratch := make([]byte, protocol.MaxUDPFrame)
	for {
		addr, payload, err := protocol.ReadUDPPacketInto(workConn, scratch)
		if err != nil {
			return
		}

		conn, err := sockets.get(addr)
		if err != nil {
			logger.Debug("open local udp socket", "error", err)
			continue
		}
		if _, err := conn.Write(payload); err != nil {
			logger.Debug("write to local service", "error", err)
			sockets.drop(string(addr))
			continue
		}

		// UDP never reaches the kernel fast path, so it is recorded as
		// buffered — which is honest, and keeps the spliced fraction on the
		// status page meaning what it claims to.
		s.client.traffic.RecordTransfer(proxyName, int64(len(payload)), 0, false)
	}
}

// trafficSink records bytes travelling back from the local service. The pump
// goroutine outlives the call that started it, so it holds this rather than a
// session it might outlast.
type trafficSink func(bytesOut int64)

// udpSockets keeps one local socket per remote source.
type udpSockets struct {
	mu    sync.Mutex
	conns map[string]*net.UDPConn

	workConn net.Conn
	target   *net.UDPAddr
	record   trafficSink
	logger   interface {
		Warn(string, ...any)
		Debug(string, ...any)
	}
}

// get returns the local socket for a source, dialling one on first sight.
//
// The source arrives as bytes aliasing the read scratch buffer. On the hit
// path — every packet after a source's first — the map lookup uses those
// bytes directly, which Go performs without materialising a string. Only a
// genuinely new source pays for the string copy it needs to keep.
func (s *udpSockets) get(source []byte) (*net.UDPConn, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if conn, ok := s.conns[string(source)]; ok {
		conn.SetReadDeadline(time.Now().Add(localUDPTimeout))
		return conn, nil
	}

	conn, err := net.DialUDP("udp", nil, s.target)
	if err != nil {
		return nil, err
	}
	conn.SetReadDeadline(time.Now().Add(localUDPTimeout))

	owned := string(source)
	s.conns[owned] = conn

	// Each socket reads its own replies and tags them with the source they
	// belong to, so the server can fan them back out correctly.
	go s.pump(owned, conn)

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

		if s.record != nil {
			s.record(int64(n))
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
