package netutil

import (
	"io"
	"sync"
)

// CopyBufferSize is the buffer used when the splice fast path is unavailable —
// multiplexed streams, TLS-terminated connections, and non-Linux platforms.
//
// Go's io.Copy default is 32 KiB, which throttles a single stream on a
// high bandwidth-delay-product link. 256 KiB costs more resident memory per
// concurrently active copy, but memory is bounded by concurrency rather than
// by connection count: idle tunnels hold no buffer, and sync.Pool hands the
// same buffers back out instead of allocating per copy.
const CopyBufferSize = 256 << 10 // 256 KiB

var bufPool = sync.Pool{
	New: func() any {
		b := make([]byte, CopyBufferSize)
		return &b
	},
}

// CopyBuffered copies src into dst using a pooled large buffer.
//
// It deliberately hides any io.ReaderFrom on dst and io.WriterTo on src.
// io.CopyBuffer documents that it ignores the supplied buffer when either
// interface is present, and net.TCPConn.ReadFrom falls back to its own 32 KiB
// internal buffer when it cannot splice. Masking the interfaces is what
// guarantees the large buffer is actually the one in use.
func CopyBuffered(dst io.Writer, src io.Reader) (int64, error) {
	buf := bufPool.Get().(*[]byte)
	defer bufPool.Put(buf)

	return io.CopyBuffer(writerOnly{dst}, readerOnly{src}, *buf)
}

// readerOnly masks every method of the wrapped value except Read.
type readerOnly struct{ io.Reader }

// writerOnly masks every method of the wrapped value except Write.
type writerOnly struct{ io.Writer }
