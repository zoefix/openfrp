package protocol

import (
	"encoding/binary"
	"fmt"
	"io"
	"sync"
)

// UDP packets travel over a TCP work connection, so they need framing that TCP
// does not provide. Each datagram is length-prefixed and tagged with the
// address it came from, which is what lets one work connection multiplex every
// client of a UDP tunnel:
//
//	+----------------+--------------+----------+---------+
//	| payload length | addr length  | address  | payload |
//	| 2 bytes        | 1 byte       | N bytes  | M bytes |
//	+----------------+--------------+----------+---------+
//
// Datagram boundaries are preserved exactly. A UDP application that receives
// two datagrams merged into one, or one split into two, is broken in ways that
// are extremely hard to diagnose from the far end.
const (
	udpHeaderSize = 3

	// MaxUDPPayload bounds one datagram.
	//
	// 65507 is the largest a UDP payload can be over IPv4. Anything arriving
	// larger than this cannot have come from a real socket, so it is a framing
	// error rather than a big packet.
	MaxUDPPayload = 65507

	// maxUDPAddr bounds the source address string. An IPv6 address with a zone
	// and a port fits comfortably.
	maxUDPAddr = 255
)

// UDPPacket is one datagram with its origin.
type UDPPacket struct {
	// Addr is the remote address the datagram came from, as host:port. The
	// server uses it to route the reply back to the right client.
	Addr string
	// Payload is the datagram body.
	Payload []byte
}

// MaxUDPFrame is the largest frame the format can produce, and therefore the
// size of every pooled and scratch buffer on the framed paths.
const MaxUDPFrame = udpHeaderSize + maxUDPAddr + MaxUDPPayload

// udpFramePool recycles frame buffers so the per-packet paths allocate
// nothing at steady state. UDP moves one frame per datagram — at tens of
// thousands of packets a second, a make per frame was the single largest
// source of garbage in the whole data plane.
var udpFramePool = sync.Pool{
	New: func() any {
		b := make([]byte, MaxUDPFrame)
		return &b
	},
}

// AppendUDPPacket frames one datagram onto dst and returns the extended
// slice. It is the allocation-free core of WriteUDPPacket.
func AppendUDPPacket(dst []byte, addr string, payload []byte) ([]byte, error) {
	if len(payload) > MaxUDPPayload {
		return dst, fmt.Errorf("protocol: udp payload of %d bytes exceeds the %d byte maximum",
			len(payload), MaxUDPPayload)
	}
	if len(addr) > maxUDPAddr {
		return dst, fmt.Errorf("protocol: udp source address is too long (%d bytes)", len(addr))
	}

	var header [udpHeaderSize]byte
	binary.BigEndian.PutUint16(header[0:2], uint16(len(payload)))
	header[2] = byte(len(addr))

	dst = append(dst, header[:]...)
	dst = append(dst, addr...)
	dst = append(dst, payload...)
	return dst, nil
}

// WriteUDPPacket frames one datagram onto w.
func WriteUDPPacket(w io.Writer, packet UDPPacket) error {
	buf := udpFramePool.Get().(*[]byte)
	defer udpFramePool.Put(buf)

	frame, err := AppendUDPPacket((*buf)[:0], packet.Addr, packet.Payload)
	if err != nil {
		return err
	}

	// One write for the whole frame: a partial write followed by a concurrent
	// writer would interleave and corrupt the stream.
	if _, err := w.Write(frame); err != nil {
		return fmt.Errorf("protocol: write udp packet: %w", err)
	}
	return nil
}

// ReadUDPPacketInto reads one framed datagram into scratch, which must be at
// least MaxUDPFrame bytes. The returned addr and payload slices alias scratch
// and are valid only until the next call that reuses it.
//
// This is the allocation-free read path: one long-lived scratch buffer per
// work connection instead of three allocations per packet.
func ReadUDPPacketInto(r io.Reader, scratch []byte) (addr, payload []byte, err error) {
	if len(scratch) < MaxUDPFrame {
		return nil, nil, fmt.Errorf("protocol: udp scratch buffer of %d bytes is smaller than a maximum frame (%d)",
			len(scratch), MaxUDPFrame)
	}

	if _, err := io.ReadFull(r, scratch[:udpHeaderSize]); err != nil {
		if err == io.EOF {
			return nil, nil, io.EOF
		}
		return nil, nil, fmt.Errorf("protocol: read udp header: %w", err)
	}

	payloadLen := int(binary.BigEndian.Uint16(scratch[0:2]))
	addrLen := int(scratch[2])

	if payloadLen > MaxUDPPayload {
		return nil, nil, fmt.Errorf(
			"protocol: udp frame claims %d bytes, above the %d byte maximum",
			payloadLen, MaxUDPPayload)
	}

	body := scratch[udpHeaderSize : udpHeaderSize+addrLen+payloadLen]
	if _, err := io.ReadFull(r, body); err != nil {
		return nil, nil, fmt.Errorf("protocol: read udp frame body: %w", err)
	}

	return body[:addrLen], body[addrLen:], nil
}

// ReadUDPPacket reads one framed datagram from r.
func ReadUDPPacket(r io.Reader) (UDPPacket, error) {
	var header [udpHeaderSize]byte
	if _, err := io.ReadFull(r, header[:]); err != nil {
		if err == io.EOF {
			return UDPPacket{}, io.EOF
		}
		return UDPPacket{}, fmt.Errorf("protocol: read udp header: %w", err)
	}

	payloadLen := int(binary.BigEndian.Uint16(header[0:2]))
	addrLen := int(header[2])

	if payloadLen > MaxUDPPayload {
		return UDPPacket{}, fmt.Errorf(
			"protocol: udp frame claims %d bytes, above the %d byte maximum",
			payloadLen, MaxUDPPayload)
	}

	packet := UDPPacket{}

	if addrLen > 0 {
		addr := make([]byte, addrLen)
		if _, err := io.ReadFull(r, addr); err != nil {
			return UDPPacket{}, fmt.Errorf("protocol: read udp source address: %w", err)
		}
		packet.Addr = string(addr)
	}

	if payloadLen > 0 {
		packet.Payload = make([]byte, payloadLen)
		if _, err := io.ReadFull(r, packet.Payload); err != nil {
			return UDPPacket{}, fmt.Errorf("protocol: read udp payload: %w", err)
		}
	} else {
		// A zero-length datagram is legal UDP and must survive the round trip
		// as a zero-length datagram, not as nothing at all.
		packet.Payload = []byte{}
	}

	return packet, nil
}
