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
