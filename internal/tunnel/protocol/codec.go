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

// Frame layout on the wire:
//
//	+--------+------------------+---------------------+
//	| type   | payload length   | payload             |
//	| 1 byte | 4 bytes, big end | length bytes, JSON  |
//	+--------+------------------+---------------------+
const (
	headerSize = 5

	// MaxMessageSize bounds a single control frame. A certificate push with a
	// long chain is the largest legitimate message, and it lands far below
	// this. The limit exists so a corrupt or hostile length prefix cannot make
	// us allocate arbitrarily.
	MaxMessageSize = 256 << 10

	// readBufferSize is small on purpose: control traffic is sparse, and one
	// buffer is held per connected client for the life of the connection.
	readBufferSize = 4 << 10
)

// WriteMessage writes exactly one frame to w.
//
// Use this on a connection that will carry raw payload afterwards. It holds no
// state, so nothing is left buffered behind it.
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

// ReadMessage reads exactly one frame from r and consumes not a byte more.
//
// This matters on work connections. Those carry a single handshake message and
// then raw tunnel payload, so reading them through a buffered reader would
// swallow the first chunk of payload into a buffer that is about to be thrown
// away. Use Codec only where the stream carries nothing but framed messages.
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

// ReadMessageExpect reads exactly one frame and asserts its type.
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

// Codec reads and writes control messages over a stream.
//
// It buffers reads, which is right for a control connection that carries only
// framed messages and wrong for anything that later switches to raw payload.
//
// Writes are serialised internally because the control loop legitimately
// writes from several goroutines at once — a heartbeat and a proxy response
// can race. Reads are not serialised: exactly one goroutine owns reading.
type Codec struct {
	r *bufio.Reader
	w io.Writer

	// deadliner is the underlying connection, when the stream has one, so a
	// write can be bounded. See WriteTimeout.
	deadliner interface{ SetWriteDeadline(time.Time) error }

	writeMu sync.Mutex
	hdr     [headerSize]byte
}

// WriteTimeout bounds a single control-message write.
//
// Without it a congested control connection is a session killer, and not
// only for the message that blocked. Every writer shares the mutex above, so
// one write stuck in a full socket buffer also holds up the heartbeat reply
// behind it — and the peer, seeing no traffic within its heartbeat timeout,
// concludes the connection is dead and tears the whole session down. Every
// tunnel that session published goes with it, including tunnels on other
// servers served by the same client process.
//
// Failing the write instead turns a silent, mysterious disconnect into a
// reported error on the message that actually caused it.
const WriteTimeout = 15 * time.Second

// NewCodec wraps a stream. The caller retains ownership of rw and is
// responsible for closing it and for setting any read deadlines.
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

// encode renders a message as a complete frame.
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

// decodeBody reads and decodes the payload following an already-read header.
func decodeBody(r io.Reader, hdr [headerSize]byte) (Message, error) {
	msgType := Type(hdr[0])
	length := binary.BigEndian.Uint32(hdr[1:headerSize])

	if length > MaxMessageSize {
		return nil, fmt.Errorf("protocol: %w: %d bytes for %s",
			ErrMessageTooLarge, length, msgType)
	}

	newMessage, known := factories[msgType]
	if !known {
		// Drain the payload so the stream stays framed. That lets a newer peer
		// send a message this build predates without desynchronising us.
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

// Write encodes and sends one message.
func (c *Codec) Write(m Message) error {
	frame, err := encode(m)
	if err != nil {
		return err
	}

	c.writeMu.Lock()
	defer c.writeMu.Unlock()

	// Bounded, so a blocked write cannot hold this mutex — and with it the
	// heartbeat — until the peer gives up on the whole session.
	if c.deadliner != nil {
		c.deadliner.SetWriteDeadline(time.Now().Add(WriteTimeout))
		defer c.deadliner.SetWriteDeadline(time.Time{})
	}

	// One Write for the whole frame: two writes would let a concurrent writer
	// interleave between header and payload if the mutex were ever dropped,
	// and it saves a syscall besides.
	if _, err := c.w.Write(frame); err != nil {
		return fmt.Errorf("protocol: write %s: %w", m.Type(), err)
	}
	return nil
}

// Read decodes the next message from the stream.
//
// It returns io.EOF unwrapped when the peer closes cleanly at a frame
// boundary, so callers can distinguish an orderly shutdown from a truncated
// frame with errors.Is.
func (c *Codec) Read() (Message, error) {
	if _, err := io.ReadFull(c.r, c.hdr[:]); err != nil {
		if err == io.EOF {
			return nil, io.EOF
		}
		return nil, fmt.Errorf("protocol: read header: %w", err)
	}
	return decodeBody(c.r, c.hdr)
}

// ReadExpect reads the next message and asserts its type, which is the common
// shape during a handshake where exactly one message is legal.
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
