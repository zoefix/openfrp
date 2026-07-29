package netutil

import (
	"io"
	"net"
	"sync/atomic"
)

type CloseWriter interface {
	CloseWrite() error
}

type Unwrapper interface {
	Unwrap() net.Conn
}

const maxUnwrapDepth = 8

var (
	splicedRelays  atomic.Int64
	bufferedRelays atomic.Int64
)

func RelayCounts() (spliced, buffered int64) {
	return splicedRelays.Load(), bufferedRelays.Load()
}

func ResetRelayCounts() {
	splicedRelays.Store(0)
	bufferedRelays.Store(0)
}

type RelayStats struct {
	AToB int64
	BToA int64

	Spliced bool
}

func Relay(a, b net.Conn) RelayStats {
	return RelayLimited(a, b, nil, nil)
}

func RelayLimited(a, b net.Conn, aToB, bToA *Limiter) RelayStats {
	stats := RelayStats{Spliced: CanSplice(a, b)}

	if stats.Spliced {
		splicedRelays.Add(1)
	} else {
		bufferedRelays.Add(1)
	}

	done := make(chan struct{})
	go func() {
		defer close(done)
		stats.AToB = copyAndHalfClose(b, a, aToB)
	}()
	stats.BToA = copyAndHalfClose(a, b, bToA)
	<-done

	return stats
}

func copyAndHalfClose(dst, src net.Conn, limit *Limiter) int64 {
	n, _ := copyStream(dst, src, limit)

	if cw, ok := unwrap(dst).(CloseWriter); ok {
		_ = cw.CloseWrite()
	} else {

		_ = dst.Close()
	}
	return n
}

func copyStream(dst, src net.Conn, limit *Limiter) (int64, error) {
	rawDst, rawSrc := unwrap(dst), unwrap(src)

	tcpDst, dstIsTCP := rawDst.(*net.TCPConn)
	_, srcIsTCP := rawSrc.(*net.TCPConn)
	spliceable := dstIsTCP && srcIsTCP

	if limit == nil {
		if spliceable {
			return tcpDst.ReadFrom(rawSrc)
		}
		return CopyBuffered(dst, src)
	}

	// Paced, and still spliced. The kernel call takes a byte count, and Go
	// passes one through when the source is an io.LimitedReader wrapping a
	// TCP connection — so a rate limit costs a bounded chunk per round and a
	// sleep between rounds, not a copy through userspace. Limiting by reading
	// into our own buffer would have cost the whole zero-copy path, which is
	// the one property this data plane is built around.
	chunk := limit.chunk()

	var total int64
	for {
		var (
			n   int64
			err error
		)
		if spliceable {
			n, err = tcpDst.ReadFrom(&io.LimitedReader{R: rawSrc, N: chunk})
		} else {
			n, err = CopyBuffered(dst, &io.LimitedReader{R: src, N: chunk})
		}
		total += n
		limit.wait(n)

		if err != nil {
			return total, err
		}
		// A LimitedReader reports EOF both when its budget runs out and when
		// the connection really ends. Short of the budget means the second.
		if n < chunk {
			return total, nil
		}
	}
}

func CanSplice(a, b net.Conn) bool {
	if !spliceSupported {
		return false
	}
	_, aOK := unwrap(a).(*net.TCPConn)
	_, bOK := unwrap(b).(*net.TCPConn)
	return aOK && bOK
}

func unwrap(c net.Conn) net.Conn {
	for range maxUnwrapDepth {
		u, ok := c.(Unwrapper)
		if !ok {
			return c
		}
		next := u.Unwrap()
		if next == nil || next == c {
			return c
		}
		c = next
	}
	return c
}

var _ CloseWriter = (*net.TCPConn)(nil)

var _ io.ReaderFrom = (*net.TCPConn)(nil)
