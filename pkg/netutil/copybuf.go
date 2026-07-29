package netutil

import (
	"io"
	"sync"
)

const CopyBufferSize = 256 << 10

var bufPool = sync.Pool{
	New: func() any {
		b := make([]byte, CopyBufferSize)
		return &b
	},
}

func CopyBuffered(dst io.Writer, src io.Reader) (int64, error) {
	buf := bufPool.Get().(*[]byte)
	defer bufPool.Put(buf)

	return io.CopyBuffer(writerOnly{dst}, readerOnly{src}, *buf)
}

type readerOnly struct{ io.Reader }

type writerOnly struct{ io.Writer }
