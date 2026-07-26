package protocol

import (
	"encoding/binary"
	"fmt"
	"io"
)

// Magic prefixes every connection to the server.
//
// It costs four bytes and buys two things: a port scanner or stray HTTP
// request is rejected immediately instead of occupying a goroutine until it
// times out, and a version mismatch surfaces as a clear error rather than as a
// confusing JSON decode failure further in.
var Magic = [4]byte{'O', 'F', 'R', 'P'}

// Mode declares what a freshly opened connection is for. It is the first thing
// the server learns, because the answer decides whether the connection is
// framed at all.
type Mode uint8

const (
	// ModePlain means this connection speaks the control protocol directly.
	// Whether it is a control connection or a work connection is settled by
	// the first message on it.
	//
	// This is the default, and the one that keeps splice(2) reachable: a work
	// connection in this mode is a bare TCP socket once its opening message is
	// consumed.
	ModePlain Mode = 0x01

	// ModeMux means the connection carries a yamux session, and every stream
	// inside it behaves as if it were a ModePlain connection.
	//
	// Opt in only when socket count matters more than throughput. Everything
	// inside a mux session shares one congestion window and one retransmission
	// queue, and no stream can ever be spliced.
	ModeMux Mode = 0x02
)

var modeNames = map[Mode]string{
	ModePlain: "plain",
	ModeMux:   "mux",
}

// String implements fmt.Stringer.
func (m Mode) String() string {
	if name, ok := modeNames[m]; ok {
		return name
	}
	return fmt.Sprintf("Mode(%d)", uint8(m))
}

// Valid reports whether m is a recognised mode.
func (m Mode) Valid() bool {
	_, ok := modeNames[m]
	return ok
}

// preambleSize is magic + version + mode.
const preambleSize = len(Magic) + 2

// Preamble is the fixed-size greeting opening every connection.
type Preamble struct {
	Version uint8
	Mode    Mode
}

// WritePreamble sends the greeting.
func WritePreamble(w io.Writer, p Preamble) error {
	var buf [preambleSize]byte
	copy(buf[:], Magic[:])
	buf[len(Magic)] = p.Version
	buf[len(Magic)+1] = byte(p.Mode)

	if _, err := w.Write(buf[:]); err != nil {
		return fmt.Errorf("protocol: write preamble: %w", err)
	}
	return nil
}

// ReadPreamble consumes and validates the greeting.
func ReadPreamble(r io.Reader) (Preamble, error) {
	var buf [preambleSize]byte
	if _, err := io.ReadFull(r, buf[:]); err != nil {
		return Preamble{}, fmt.Errorf("protocol: read preamble: %w", err)
	}

	if [4]byte(buf[:len(Magic)]) != Magic {
		return Preamble{}, fmt.Errorf("protocol: bad magic %#x: not an OpenFrp connection",
			binary.BigEndian.Uint32(buf[:len(Magic)]))
	}

	p := Preamble{
		Version: buf[len(Magic)],
		Mode:    Mode(buf[len(Magic)+1]),
	}
	if err := CheckVersion(int(p.Version)); err != nil {
		return Preamble{}, err
	}
	if !p.Mode.Valid() {
		return Preamble{}, fmt.Errorf("protocol: unknown connection mode %d", uint8(p.Mode))
	}
	return p, nil
}
