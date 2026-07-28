//go:build linux

package netutil

import "syscall"

// soReusePort is SO_REUSEPORT from <asm-generic/socket.h>. It is spelled out
// here rather than taken from the syscall package so the value is visible at
// the point of use and does not depend on which constants a given Go release
// happens to export.
const soReusePort = 0x0F

const reusePortSupported = true

// setReusePort enables SO_REUSEPORT so several listeners can bind the same
// address and the kernel spreads incoming connections across them.
func setReusePort(fd uintptr) error {
	return syscall.SetsockoptInt(int(fd), syscall.SOL_SOCKET, soReusePort, 1)
}

const deferAcceptSupported = true

// setDeferAccept enables TCP_DEFER_ACCEPT: accept(2) completes only once the
// peer has sent data, up to the given patience in seconds.
//
// On a listener whose protocol is strictly client-first — our control
// preamble, an HTTP request, a TLS ClientHello — this removes one wakeup per
// connection, and it means a peer that connects and says nothing costs the
// kernel a queue slot rather than costing us a goroutine parked on a read
// deadline. It must never be set where the tunnelled protocol might speak
// server-first; ssh behind a tcp proxy would deadlock.
func setDeferAccept(fd uintptr, seconds int) error {
	return syscall.SetsockoptInt(int(fd), syscall.IPPROTO_TCP, syscall.TCP_DEFER_ACCEPT, seconds)
}
