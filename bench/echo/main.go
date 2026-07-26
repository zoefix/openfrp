// Command echo is the LAN service behind the tunnel in the benchmark.
//
// It echoes bytes with io.Copy, which on Linux means the backend itself is
// spliced and contributes as little as possible to the measurement. The point
// of the benchmark is to compare tunnels, not backends.
package main

import (
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"os"
)

func main() {
	addr := flag.String("addr", ":9000", "listen address")
	flag.Parse()

	ln, err := net.Listen("tcp", *addr)
	if err != nil {
		fmt.Fprintf(os.Stderr, "echo: listen %s: %v\n", *addr, err)
		os.Exit(1)
	}
	log.Printf("echo listening on %s", ln.Addr())

	for {
		conn, err := ln.Accept()
		if err != nil {
			log.Printf("accept: %v", err)
			return
		}
		go func() {
			defer conn.Close()
			if tcp, ok := conn.(*net.TCPConn); ok {
				tcp.SetNoDelay(true)
			}
			io.Copy(conn, conn)
		}()
	}
}
