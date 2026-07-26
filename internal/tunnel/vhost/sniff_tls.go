package vhost

import (
	"encoding/binary"
	"fmt"
	"io"
)

// TLS record and handshake constants from RFC 8446 and its predecessors.
const (
	recordTypeHandshake  = 0x16
	handshakeClientHello = 0x01
	extensionServerName  = 0x0000
	sniTypeHostName      = 0x00

	tlsRecordHeaderSize = 5
	tlsRandomSize       = 32
)

// TLSInfo is what the ClientHello tells us.
type TLSInfo struct {
	// ServerName is the SNI value, normalised. Empty when the client sent no
	// SNI extension, which happens with very old clients and raw-IP TLS.
	ServerName string
	// Consumed holds every byte read while sniffing. The caller must replay
	// these to the upstream connection before relaying; the handshake will
	// fail otherwise, since the server needs the ClientHello.
	Consumed []byte
}

// SniffTLS reads a TLS ClientHello and extracts the SNI without terminating
// the handshake.
//
// The ClientHello is parsed directly rather than by driving crypto/tls with a
// GetConfigForClient callback, the way frp does. Parsing gives exact control
// over which bytes were consumed, which is what allows the caller to replay
// them upstream and then splice two bare sockets. Wrapping the connection to
// replay instead would cost the fast path for the whole transfer.
//
// The server never sees plaintext: it forwards the encrypted stream untouched.
func SniffTLS(r io.Reader) (TLSInfo, error) {
	head := newHeadReader(r, MaxTLSHead)

	if err := head.ensure(tlsRecordHeaderSize); err != nil {
		return TLSInfo{Consumed: head.bytes()},
			fmt.Errorf("vhost: reading TLS record header: %w", err)
	}

	raw := head.bytes()
	if raw[0] != recordTypeHandshake {
		return TLSInfo{Consumed: head.bytes()},
			fmt.Errorf("vhost: not a TLS handshake (record type 0x%02x)", raw[0])
	}

	recordLen := int(binary.BigEndian.Uint16(raw[3:5]))
	if recordLen == 0 {
		return TLSInfo{Consumed: head.bytes()}, fmt.Errorf("vhost: empty TLS record")
	}

	total := tlsRecordHeaderSize + recordLen
	if err := head.ensure(total); err != nil {
		return TLSInfo{Consumed: head.bytes()},
			fmt.Errorf("vhost: reading ClientHello: %w", err)
	}

	info := TLSInfo{Consumed: head.bytes()}

	name, err := parseClientHello(head.bytes()[tlsRecordHeaderSize:total])
	if err != nil {
		return info, err
	}
	if name == "" {
		// Not an error in itself; the caller decides whether a catch-all route
		// can serve a client that sent no SNI.
		return info, nil
	}

	normalised, err := NormaliseHost(name)
	if err != nil {
		return info, fmt.Errorf("vhost: SNI %q: %w", name, err)
	}
	info.ServerName = normalised
	return info, nil
}

// parseClientHello walks the handshake body and returns the SNI host name, or
// an empty string when the extension is absent.
func parseClientHello(body []byte) (string, error) {
	r := &byteReader{buf: body}

	msgType, ok := r.u8()
	if !ok || msgType != handshakeClientHello {
		return "", fmt.Errorf("vhost: expected ClientHello, got handshake type %d", msgType)
	}
	if _, ok := r.u24(); !ok {
		return "", fmt.Errorf("vhost: truncated ClientHello length")
	}
	if _, ok := r.skip(2); !ok { // legacy_version
		return "", fmt.Errorf("vhost: truncated ClientHello version")
	}
	if _, ok := r.skip(tlsRandomSize); !ok {
		return "", fmt.Errorf("vhost: truncated ClientHello random")
	}
	if _, ok := r.vector8(); !ok { // legacy_session_id
		return "", fmt.Errorf("vhost: truncated session id")
	}
	if _, ok := r.vector16(); !ok { // cipher_suites
		return "", fmt.Errorf("vhost: truncated cipher suites")
	}
	if _, ok := r.vector8(); !ok { // legacy_compression_methods
		return "", fmt.Errorf("vhost: truncated compression methods")
	}

	extensions, ok := r.vector16()
	if !ok {
		// A ClientHello without extensions is legal in TLS 1.0-1.2 and simply
		// carries no SNI.
		return "", nil
	}

	ext := &byteReader{buf: extensions}
	for !ext.done() {
		extType, ok := ext.u16()
		if !ok {
			return "", fmt.Errorf("vhost: truncated extension type")
		}
		extData, ok := ext.vector16()
		if !ok {
			return "", fmt.Errorf("vhost: truncated extension body")
		}
		if extType != extensionServerName {
			continue
		}

		names := &byteReader{buf: extData}
		list, ok := names.vector16()
		if !ok {
			return "", fmt.Errorf("vhost: truncated server name list")
		}

		entries := &byteReader{buf: list}
		for !entries.done() {
			nameType, ok := entries.u8()
			if !ok {
				return "", fmt.Errorf("vhost: truncated server name type")
			}
			value, ok := entries.vector16()
			if !ok {
				return "", fmt.Errorf("vhost: truncated server name")
			}
			if nameType == sniTypeHostName {
				return string(value), nil
			}
		}
		return "", nil
	}

	return "", nil
}

// byteReader is a bounds-checked cursor over a byte slice. Every accessor
// reports success rather than panicking, because the input is attacker
// controlled.
type byteReader struct {
	buf []byte
	pos int
}

func (r *byteReader) done() bool { return r.pos >= len(r.buf) }

func (r *byteReader) remaining() int { return len(r.buf) - r.pos }

func (r *byteReader) skip(n int) ([]byte, bool) {
	if n < 0 || r.remaining() < n {
		return nil, false
	}
	out := r.buf[r.pos : r.pos+n]
	r.pos += n
	return out, true
}

func (r *byteReader) u8() (uint8, bool) {
	b, ok := r.skip(1)
	if !ok {
		return 0, false
	}
	return b[0], true
}

func (r *byteReader) u16() (uint16, bool) {
	b, ok := r.skip(2)
	if !ok {
		return 0, false
	}
	return binary.BigEndian.Uint16(b), true
}

func (r *byteReader) u24() (uint32, bool) {
	b, ok := r.skip(3)
	if !ok {
		return 0, false
	}
	return uint32(b[0])<<16 | uint32(b[1])<<8 | uint32(b[2]), true
}

// vector8 reads a length-prefixed block with a one-byte length.
func (r *byteReader) vector8() ([]byte, bool) {
	n, ok := r.u8()
	if !ok {
		return nil, false
	}
	return r.skip(int(n))
}

// vector16 reads a length-prefixed block with a two-byte length.
func (r *byteReader) vector16() ([]byte, bool) {
	n, ok := r.u16()
	if !ok {
		return nil, false
	}
	return r.skip(int(n))
}
