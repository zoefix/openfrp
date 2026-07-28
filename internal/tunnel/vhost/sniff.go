package vhost

import (
	"errors"
	"fmt"
	"io"
	"sync"
)

// Head size limits. Anything legitimate fits far inside these; the limits are
// here so a peer that opens a connection and dribbles bytes cannot make us
// buffer without bound.
const (
	// MaxHTTPHead bounds the request line plus headers we will read looking
	// for Host. Nginx's default large_client_header_buffers is 8 KiB.
	MaxHTTPHead = 16 << 10
	// MaxTLSHead bounds one ClientHello record. The record layer caps a
	// fragment at 16 KiB, plus the 5-byte header.
	MaxTLSHead = (16 << 10) + 5
)

var (
	// ErrHeadTooLarge means the peer sent more preamble than we will buffer.
	ErrHeadTooLarge = errors.New("vhost: head exceeds the maximum size")
	// ErrNoHost means the request carried no usable host name.
	ErrNoHost = errors.New("vhost: no host name found")
)

// sniffResult is what a sniffer recovers from the head of a connection.
//
// Consumed is the crucial field. Rather than wrapping the connection in a
// replaying reader — which would hide the *net.TCPConn and cost us splice(2)
// for the entire transfer — the caller writes Consumed to the far side first
// and then relays the two bare sockets. The peek is paid for once, in bytes,
// instead of forever, in a userspace copy of every byte that follows.
type sniffResult struct {
	Host     string
	Consumed []byte
}

// headBufSize is the starting capacity of a pooled sniff buffer. It covers a
// typical HTTP request head and a typical ClientHello — post-quantum key
// shares included — so most connections never grow it.
const headBufSize = 4096

// headBufPool recycles sniff buffers across connections. The sniffed head is
// needed only until it has been replayed to the other side, which happens
// long before the relay ends; returning the buffer then lets it serve another
// arriving connection instead of sitting as garbage.
var headBufPool = sync.Pool{
	New: func() any {
		b := make([]byte, 0, headBufSize)
		return &b
	},
}

// PutConsumed returns a Consumed buffer to the sniff pool.
//
// Callers do this once the head has been fully replayed and nothing aliases
// it any more. Skipping the call is always safe — the buffer just falls to
// the garbage collector — which is exactly what the edge-termination path
// does, since its TLS wrapper may keep serving surplus sniffed bytes for the
// life of the connection.
func PutConsumed(buf []byte) {
	if cap(buf) < headBufSize {
		return
	}
	b := buf[:0]
	headBufPool.Put(&b)
}

// headReader accumulates bytes from r, remembering everything it has read.
type headReader struct {
	r   io.Reader
	buf []byte
	max int
}

func newHeadReader(r io.Reader, max int) *headReader {
	return &headReader{r: r, buf: (*headBufPool.Get().(*[]byte))[:0], max: max}
}

// ensure reads until at least n bytes are buffered.
func (h *headReader) ensure(n int) error {
	if n > h.max {
		return fmt.Errorf("%w: wanted %d bytes, limit is %d", ErrHeadTooLarge, n, h.max)
	}
	for len(h.buf) < n {
		if cap(h.buf) < n {
			grown := make([]byte, len(h.buf), max(n, cap(h.buf)*2))
			copy(grown, h.buf)
			old := h.buf
			h.buf = grown
			PutConsumed(old)
		}
		read, err := h.r.Read(h.buf[len(h.buf):cap(h.buf)])
		h.buf = h.buf[:len(h.buf)+read]
		if err != nil {
			if err == io.EOF && len(h.buf) >= n {
				return nil
			}
			return err
		}
	}
	return nil
}

// readMore pulls at least one more byte into the buffer.
func (h *headReader) readMore() error {
	if len(h.buf) >= h.max {
		return fmt.Errorf("%w: %d bytes", ErrHeadTooLarge, len(h.buf))
	}
	if len(h.buf) == cap(h.buf) {
		grown := make([]byte, len(h.buf), min(max(cap(h.buf)*2, 1024), h.max))
		copy(grown, h.buf)
		old := h.buf
		h.buf = grown
		PutConsumed(old)
	}
	read, err := h.r.Read(h.buf[len(h.buf):cap(h.buf)])
	h.buf = h.buf[:len(h.buf)+read]
	if err != nil {
		return err
	}
	if read == 0 {
		return io.ErrNoProgress
	}
	return nil
}

// bytes returns everything consumed so far. The caller must replay these.
func (h *headReader) bytes() []byte { return h.buf }
