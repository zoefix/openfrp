package protocol

import (
	"encoding/binary"
	"fmt"
	"io"
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

// WriteUDPPacket frames one datagram onto w.
func WriteUDPPacket(w io.Writer, packet UDPPacket) error {
	if len(packet.Payload) > MaxUDPPayload {
		return fmt.Errorf("protocol: udp payload of %d bytes exceeds the %d byte maximum",
			len(packet.Payload), MaxUDPPayload)
	}
	if len(packet.Addr) > maxUDPAddr {
		return fmt.Errorf("protocol: udp source address is too long (%d bytes)", len(packet.Addr))
	}

	frame := make([]byte, udpHeaderSize+len(packet.Addr)+len(packet.Payload))
	binary.BigEndian.PutUint16(frame[0:2], uint16(len(packet.Payload)))
	frame[2] = byte(len(packet.Addr))
	copy(frame[udpHeaderSize:], packet.Addr)
	copy(frame[udpHeaderSize+len(packet.Addr):], packet.Payload)

	// One write for the whole frame: a partial write followed by a concurrent
	// writer would interleave and corrupt the stream.
	if _, err := w.Write(frame); err != nil {
		return fmt.Errorf("protocol: write udp packet: %w", err)
	}
	return nil
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
