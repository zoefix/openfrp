package protocol

import (
	"encoding/binary"
	"fmt"
	"io"
)

var Magic = [4]byte{'O', 'F', 'R', 'P'}

type Mode uint8

const (
	ModePlain Mode = 0x01

	ModeMux Mode = 0x02
)

var modeNames = map[Mode]string{
	ModePlain: "plain",
	ModeMux:   "mux",
}

func (m Mode) String() string {
	if name, ok := modeNames[m]; ok {
		return name
	}
	return fmt.Sprintf("Mode(%d)", uint8(m))
}

func (m Mode) Valid() bool {
	_, ok := modeNames[m]
	return ok
}

const preambleSize = len(Magic) + 2

type Preamble struct {
	Version uint8
	Mode    Mode
}

func WriteGreeting(w io.Writer, p Preamble, m Message) error {
	frame, err := encode(m)
	if err != nil {
		return err
	}

	buf := make([]byte, 0, preambleSize+len(frame))
	buf = append(buf, Magic[:]...)
	buf = append(buf, p.Version, byte(p.Mode))
	buf = append(buf, frame...)

	if _, err := w.Write(buf); err != nil {
		return fmt.Errorf("protocol: write greeting: %w", err)
	}
	return nil
}

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
