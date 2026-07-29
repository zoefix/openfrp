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
	stats := RelayStats{Spliced: CanSplice(a, b)}

	if stats.Spliced {
		splicedRelays.Add(1)
	} else {
		bufferedRelays.Add(1)
	}

	done := make(chan struct{})
	go func() {
		defer close(done)
		stats.AToB = copyAndHalfClose(b, a)
	}()
	stats.BToA = copyAndHalfClose(a, b)
	<-done

	return stats
}

func copyAndHalfClose(dst, src net.Conn) int64 {
	n, _ := copyStream(dst, src)

	if cw, ok := unwrap(dst).(CloseWriter); ok {
		_ = cw.CloseWrite()
	} else {

		_ = dst.Close()
	}
	return n
}

func copyStream(dst, src net.Conn) (int64, error) {
	rawDst, rawSrc := unwrap(dst), unwrap(src)

	if tcpDst, ok := rawDst.(*net.TCPConn); ok {
		if _, ok := rawSrc.(*net.TCPConn); ok {

			return tcpDst.ReadFrom(rawSrc)
		}
	}

	return CopyBuffered(dst, src)
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
