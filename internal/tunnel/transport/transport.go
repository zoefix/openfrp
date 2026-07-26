// Package transport carries control and work connections between the client
// and the server.
//
// The central decision lives here. By default every work connection is its own
// TCP connection (StreamSource is direct), which is what makes the splice(2)
// fast path reachable and gives each tunnel an independent congestion window.
// Multiplexing is available but opt-in, because it trades all of that away for
// a lower socket count.
package transport

import (
	"context"
	"net"
)

// StreamSource yields connections that carry tunnel payload.
//
// Direct returns a fresh TCP connection per stream. Mux returns yamux streams
// over one shared connection. Callers do not care which they hold, except that
// only the direct one can be spliced — and netutil.CanSplice answers that
// without the caller needing to know the difference.
type StreamSource interface {
	// Open returns the next payload-carrying connection.
	Open(ctx context.Context) (net.Conn, error)
	// Close releases the source and any shared underlying connection.
	Close() error
	// Multiplexed reports whether streams share one underlying connection.
	Multiplexed() bool
}

// StreamAcceptor is the server-side mirror of StreamSource.
type StreamAcceptor interface {
	// Accept returns the next payload-carrying connection.
	Accept() (net.Conn, error)
	// Close releases the acceptor.
	Close() error
	// Multiplexed reports whether streams share one underlying connection.
	Multiplexed() bool
}
