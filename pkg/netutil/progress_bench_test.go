package netutil

import (
	"io"
	"net"
	"sync/atomic"
	"testing"
)

func BenchmarkRelay(b *testing.B) {
	const payload = 8 << 20

	for _, watched := range []bool{false, true} {
		name := "unwatched"
		if watched {
			name = "watched"
		}
		b.Run(name, func(b *testing.B) {
			b.SetBytes(payload)
			b.ReportAllocs()

			for b.Loop() {
				benchRelayOnce(b, payload, watched)
			}
		})
	}
}

func benchRelayOnce(b *testing.B, size int64, watched bool) {
	b.Helper()

	visitor, service, stop := benchPair(b)
	defer stop()

	go func() {
		io.Copy(io.Discard, service.near)
		service.near.Close()
	}()

	var counted atomic.Int64
	opts := RelayOptions{}
	if watched {
		opts.Progress = func(a, c int64) { counted.Add(a + c) }
	}

	done := make(chan struct{})
	go func() {
		defer close(done)
		RelayWith(visitor.far, service.far, opts)
	}()

	buf := make([]byte, 256<<10)
	var sent int64
	for sent < size {
		n, err := visitor.near.Write(buf)
		if err != nil {
			break
		}
		sent += int64(n)
	}
	visitor.near.(*net.TCPConn).CloseWrite()
	<-done
}

func benchPair(b *testing.B) (visitor, service connPair, stop func()) {
	b.Helper()

	make1 := func() connPair {
		ln, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			b.Fatalf("listen: %v", err)
		}
		defer ln.Close()

		accepted := make(chan net.Conn, 1)
		go func() {
			c, err := ln.Accept()
			if err == nil {
				accepted <- c
			}
		}()
		near, err := net.Dial("tcp", ln.Addr().String())
		if err != nil {
			b.Fatalf("dial: %v", err)
		}
		return connPair{near: near, far: <-accepted}
	}

	visitor, service = make1(), make1()
	return visitor, service, func() {
		visitor.near.Close()
		visitor.far.Close()
		service.near.Close()
		service.far.Close()
	}
}
