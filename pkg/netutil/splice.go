// Package netutil holds the data-plane primitives shared by the server and the
// client. Everything here sits on the hot path, so the invariants documented
// below are load-bearing rather than stylistic.
package netutil

import (
	"io"
	"net"
	"sync/atomic"
)

// CloseWriter is implemented by connections that support a half close.
//
// Shutting down only the write side lets the peer observe EOF and finish its
// own response, which is what protocols like HTTP require. Closing the whole
// connection instead truncates in-flight replies.
type CloseWriter interface {
	CloseWrite() error
}

// Unwrapper lets a wrapper expose the connection underneath it.
//
// Implement this ONLY when the wrapper does not transform the byte stream. A
// traffic counter or a deadline shim may implement it; a TLS or compression
// layer must not, because handing the raw socket to splice(2) would bypass the
// transformation entirely.
type Unwrapper interface {
	Unwrap() net.Conn
}

// maxUnwrapDepth bounds the unwrap walk so a cyclic wrapper cannot hang us.
const maxUnwrapDepth = 8

// Relay path counters.
//
// These exist so the fast path is observable in production, not just in a unit
// test. A deployment where buffered relays outnumber spliced ones has lost the
// main throughput advantage this design is built around, and that should be
// visible in the status view rather than something you have to profile for.
var (
	splicedRelays  atomic.Int64
	bufferedRelays atomic.Int64
)

// RelayCounts reports how many relays took each path since process start.
func RelayCounts() (spliced, buffered int64) {
	return splicedRelays.Load(), bufferedRelays.Load()
}

// ResetRelayCounts zeroes the counters. Intended for tests.
func ResetRelayCounts() {
	splicedRelays.Store(0)
	bufferedRelays.Store(0)
}

// RelayStats reports the outcome of a Relay.
type RelayStats struct {
	// AToB and BToA count bytes moved in each direction.
	AToB int64
	BToA int64
	// Spliced records whether the kernel fast path was eligible. It is the
	// signal the benchmark harness and the fast-path tests assert on.
	Spliced bool
}

// Relay copies bidirectionally between a and b until both directions finish,
// then returns the byte counts.
//
// When both sides are TCP connections on a platform with splice(2), payload
// bytes move kernel-to-kernel and never enter this process's address space.
// That is the single largest throughput win we hold over frp, whose default
// multiplexing makes the fast path structurally impossible.
func Relay(a, b net.Conn) RelayStats {
	stats := RelayStats{Spliced: CanSplice(a, b)}

	if stats.Spliced {
		splicedRelays.Add(1)
	} else {
		bufferedRelays.Add(1)
	}

	// One direction runs on the calling goroutine. The caller was going to
	// block in Wait anyway, so spawning a second goroutine bought nothing but
	// a stack — and at tens of thousands of concurrent relays, those stacks
	// are the difference between fitting in a small router's memory and not.
	done := make(chan struct{})
	go func() {
		defer close(done)
		stats.AToB = copyAndHalfClose(b, a)
	}()
	stats.BToA = copyAndHalfClose(a, b)
	<-done

	return stats
}

// copyAndHalfClose drains src into dst, then signals EOF to dst without
// disturbing the opposite direction.
func copyAndHalfClose(dst, src net.Conn) int64 {
	n, _ := copyStream(dst, src)

	if cw, ok := unwrap(dst).(CloseWriter); ok {
		_ = cw.CloseWrite()
	} else {
		// No half close available, so a full close is the only way to make the
		// peer see EOF. The other direction's copy will unblock with an error.
		_ = dst.Close()
	}
	return n
}

// copyStream moves src into dst by the fastest route available.
func copyStream(dst, src net.Conn) (int64, error) {
	rawDst, rawSrc := unwrap(dst), unwrap(src)

	if tcpDst, ok := rawDst.(*net.TCPConn); ok {
		if _, ok := rawSrc.(*net.TCPConn); ok {
			// TCPConn.ReadFrom reaches for splice(2) on Linux and degrades to a
			// plain copy elsewhere. Calling it directly, rather than going
			// through io.Copy, keeps the dispatch explicit and greppable.
			return tcpDst.ReadFrom(rawSrc)
		}
	}

	return CopyBuffered(dst, src)
}

// CanSplice reports whether a relay between a and b is eligible for the kernel
// zero-copy path.
//
// This is what the fast-path regression test asserts on: wrapping a connection
// in a type that does not implement Unwrapper silently costs us splice, and a
// silent performance regression is the failure mode this guards against.
func CanSplice(a, b net.Conn) bool {
	if !spliceSupported {
		return false
	}
	_, aOK := unwrap(a).(*net.TCPConn)
	_, bOK := unwrap(b).(*net.TCPConn)
	return aOK && bOK
}

// unwrap walks Unwrapper layers down to the underlying connection.
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

// Compile-time proof that the standard TCP connection still satisfies the
// half-close contract we depend on.
var _ CloseWriter = (*net.TCPConn)(nil)

var _ io.ReaderFrom = (*net.TCPConn)(nil)
