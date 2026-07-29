package protocol

import (
	"encoding/binary"
	"fmt"
	"io"
	"sync"
)

const (
	udpHeaderSize = 3

	MaxUDPPayload = 65507

	maxUDPAddr = 255
)

type UDPPacket struct {
	Addr string

	Payload []byte
}

const MaxUDPFrame = udpHeaderSize + maxUDPAddr + MaxUDPPayload

var udpFramePool = sync.Pool{
	New: func() any {
		b := make([]byte, MaxUDPFrame)
		return &b
	},
}

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

func WriteUDPPacket(w io.Writer, packet UDPPacket) error {
	buf := udpFramePool.Get().(*[]byte)
	defer udpFramePool.Put(buf)

	frame, err := AppendUDPPacket((*buf)[:0], packet.Addr, packet.Payload)
	if err != nil {
		return err
	}

	if _, err := w.Write(frame); err != nil {
		return fmt.Errorf("protocol: write udp packet: %w", err)
	}
	return nil
}

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

		packet.Payload = []byte{}
	}

	return packet, nil
}
