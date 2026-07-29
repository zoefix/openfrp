package transport

import (
	"context"
	"net"
)

type StreamSource interface {
	Open(ctx context.Context) (net.Conn, error)

	Close() error

	Multiplexed() bool
}

type StreamAcceptor interface {
	Accept() (net.Conn, error)

	Close() error

	Multiplexed() bool
}
