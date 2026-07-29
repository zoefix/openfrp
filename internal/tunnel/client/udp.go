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

const localUDPTimeout = 60 * time.Second

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

		s.client.traffic.RecordTransfer(proxyName, int64(len(payload)), 0, false)
	}
}

type trafficSink func(bytesOut int64)

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
