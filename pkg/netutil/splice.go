package netutil

import (
	"errors"
	"io"
	"net"
	"sync/atomic"
	"time"
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

const progressInterval = time.Second

const trackedChunk = 1 << 30

type RelayOptions struct {
	AToBLimit *Limiter
	BToALimit *Limiter

	Progress func(aToB, bToA int64)
}

func Relay(a, b net.Conn) RelayStats {
	return RelayWith(a, b, RelayOptions{})
}

func RelayLimited(a, b net.Conn, aToB, bToA *Limiter) RelayStats {
	return RelayWith(a, b, RelayOptions{AToBLimit: aToB, BToALimit: bToA})
}

func RelayWith(a, b net.Conn, opts RelayOptions) RelayStats {
	stats := RelayStats{Spliced: CanSplice(a, b)}

	if stats.Spliced {
		splicedRelays.Add(1)
	} else {
		bufferedRelays.Add(1)
	}

	var aToBProgress, bToAProgress func(int64)
	if opts.Progress != nil {
		aToBProgress = func(n int64) { opts.Progress(n, 0) }
		bToAProgress = func(n int64) { opts.Progress(0, n) }
	}

	done := make(chan struct{})
	go func() {
		defer close(done)
		stats.AToB = copyAndHalfClose(b, a, opts.AToBLimit, aToBProgress)
	}()
	stats.BToA = copyAndHalfClose(a, b, opts.BToALimit, bToAProgress)
	<-done

	return stats
}

func copyAndHalfClose(dst, src net.Conn, limit *Limiter, progress func(int64)) int64 {
	n, _ := copyStream(dst, src, limit, progress)

	if cw, ok := unwrap(dst).(CloseWriter); ok {
		_ = cw.CloseWrite()
	} else {
		_ = dst.Close()
	}
	return n
}

func copyStream(dst, src net.Conn, limit *Limiter, progress func(int64)) (int64, error) {
	rawDst, rawSrc := unwrap(dst), unwrap(src)

	tcpDst, dstIsTCP := rawDst.(*net.TCPConn)
	_, srcIsTCP := rawSrc.(*net.TCPConn)
	spliceable := dstIsTCP && srcIsTCP

	if limit == nil && progress == nil {
		if spliceable {
			return tcpDst.ReadFrom(rawSrc)
		}
		return CopyBuffered(dst, src)
	}

	chunk := int64(trackedChunk)
	if limit != nil {
		chunk = limit.chunk()
	}

	if progress != nil {
		defer src.SetReadDeadline(time.Time{})
	}

	var total int64
	for {
		if progress != nil {
			_ = src.SetReadDeadline(time.Now().Add(progressInterval))
		}

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
		if progress != nil && n > 0 {
			progress(n)
		}

		if err != nil {
			if progress != nil && isTimeout(err) {
				continue
			}
			return total, err
		}

		if n < chunk {
			return total, nil
		}
	}
}

func isTimeout(err error) bool {
	var netErr net.Error
	return errors.As(err, &netErr) && netErr.Timeout()
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
