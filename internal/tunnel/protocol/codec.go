package protocol

import (
	"bufio"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"sync"
	"time"
)

const (
	headerSize = 5

	MaxMessageSize = 256 << 10

	readBufferSize = 4 << 10
)

func WriteMessage(w io.Writer, m Message) error {
	frame, err := encode(m)
	if err != nil {
		return err
	}
	if _, err := w.Write(frame); err != nil {
		return fmt.Errorf("protocol: write %s: %w", m.Type(), err)
	}
	return nil
}

func ReadMessage(r io.Reader) (Message, error) {
	var hdr [headerSize]byte
	if _, err := io.ReadFull(r, hdr[:]); err != nil {
		if err == io.EOF {
			return nil, io.EOF
		}
		return nil, fmt.Errorf("protocol: read header: %w", err)
	}
	return decodeBody(r, hdr)
}

func ReadMessageExpect(r io.Reader, want Type) (Message, error) {
	msg, err := ReadMessage(r)
	if err != nil {
		return nil, err
	}
	if msg.Type() != want {
		return nil, fmt.Errorf("%w: got %s, want %s", ErrUnexpectedMessage, msg.Type(), want)
	}
	return msg, nil
}

type Codec struct {
	r *bufio.Reader
	w io.Writer

	deadliner interface{ SetWriteDeadline(time.Time) error }

	writeMu sync.Mutex
	hdr     [headerSize]byte
}

const WriteTimeout = 15 * time.Second

func NewCodec(rw io.ReadWriter) *Codec {
	c := &Codec{
		r: bufio.NewReaderSize(rw, readBufferSize),
		w: rw,
	}
	if d, ok := rw.(interface{ SetWriteDeadline(time.Time) error }); ok {
		c.deadliner = d
	}
	return c
}

func encode(m Message) ([]byte, error) {
	payload, err := json.Marshal(m)
	if err != nil {
		return nil, fmt.Errorf("protocol: encode %s: %w", m.Type(), err)
	}
	if len(payload) > MaxMessageSize {
		return nil, fmt.Errorf("protocol: encode %s: %w (%d bytes)",
			m.Type(), ErrMessageTooLarge, len(payload))
	}

	frame := make([]byte, headerSize+len(payload))
	frame[0] = byte(m.Type())
	binary.BigEndian.PutUint32(frame[1:headerSize], uint32(len(payload)))
	copy(frame[headerSize:], payload)
	return frame, nil
}

func decodeBody(r io.Reader, hdr [headerSize]byte) (Message, error) {
	msgType := Type(hdr[0])
	length := binary.BigEndian.Uint32(hdr[1:headerSize])

	if length > MaxMessageSize {
		return nil, fmt.Errorf("protocol: %w: %d bytes for %s",
			ErrMessageTooLarge, length, msgType)
	}

	newMessage, known := factories[msgType]
	if !known {

		if _, err := io.CopyN(io.Discard, r, int64(length)); err != nil {
			return nil, fmt.Errorf("protocol: discard unknown %s: %w", msgType, err)
		}
		return nil, fmt.Errorf("%w: %s", ErrUnknownType, msgType)
	}

	payload := make([]byte, length)
	if _, err := io.ReadFull(r, payload); err != nil {
		return nil, fmt.Errorf("protocol: read %s payload: %w", msgType, err)
	}

	msg := newMessage()
	if err := json.Unmarshal(payload, msg); err != nil {
		return nil, fmt.Errorf("protocol: decode %s: %w", msgType, err)
	}
	return msg, nil
}

func (c *Codec) Write(m Message) error {
	frame, err := encode(m)
	if err != nil {
		return err
	}

	c.writeMu.Lock()
	defer c.writeMu.Unlock()

	if c.deadliner != nil {
		c.deadliner.SetWriteDeadline(time.Now().Add(WriteTimeout))
		defer c.deadliner.SetWriteDeadline(time.Time{})
	}

	if _, err := c.w.Write(frame); err != nil {
		return fmt.Errorf("protocol: write %s: %w", m.Type(), err)
	}
	return nil
}

func (c *Codec) Read() (Message, error) {
	if _, err := io.ReadFull(c.r, c.hdr[:]); err != nil {
		if err == io.EOF {
			return nil, io.EOF
		}
		return nil, fmt.Errorf("protocol: read header: %w", err)
	}
	return decodeBody(c.r, c.hdr)
}

func (c *Codec) ReadExpect(want Type) (Message, error) {
	msg, err := c.Read()
	if err != nil {
		return nil, err
	}
	if msg.Type() != want {
		return nil, fmt.Errorf("%w: got %s, want %s", ErrUnexpectedMessage, msg.Type(), want)
	}
	return msg, nil
}
