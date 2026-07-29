package vhost

import (
	"errors"
	"fmt"
	"io"
	"sync"
)

const (
	MaxHTTPHead = 16 << 10

	MaxTLSHead = (16 << 10) + 5
)

var (
	ErrHeadTooLarge = errors.New("vhost: head exceeds the maximum size")

	ErrNoHost = errors.New("vhost: no host name found")
)

type sniffResult struct {
	Host     string
	Consumed []byte
}

const headBufSize = 4096

var headBufPool = sync.Pool{
	New: func() any {
		b := make([]byte, 0, headBufSize)
		return &b
	},
}

func PutConsumed(buf []byte) {
	if cap(buf) < headBufSize {
		return
	}
	b := buf[:0]
	headBufPool.Put(&b)
}

type headReader struct {
	r   io.Reader
	buf []byte
	max int
}

func newHeadReader(r io.Reader, max int) *headReader {
	return &headReader{r: r, buf: (*headBufPool.Get().(*[]byte))[:0], max: max}
}

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

func (h *headReader) bytes() []byte { return h.buf }
