//go:build linux

package netutil

import (
	"syscall"
	"time"
)

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

// acceptedInheritsOptions records that Linux clones an accepted socket from
// the listening one, options included.
//
// Verified rather than assumed — see TestAcceptedSocketsInheritListenerOptions,
// which sets distinctive values on a listener and reads them back off an
// accepted connection. It is what lets the data plane tune once at bind time
// instead of spending three setsockopt syscalls on every arriving connection.
const acceptedInheritsOptions = true

// tuneListenerFD applies the connection options to the listening socket, so
// every socket accepted from it starts out already tuned.
func tuneListenerFD(fd uintptr, opts TCPOptions) error {
	nodelay := 0
	if opts.NoDelay {
		nodelay = 1
	}
	if err := syscall.SetsockoptInt(int(fd), syscall.IPPROTO_TCP,
		syscall.TCP_NODELAY, nodelay); err != nil {
		return err
	}

	if opts.KeepAlive <= 0 {
		return syscall.SetsockoptInt(int(fd), syscall.SOL_SOCKET,
			syscall.SO_KEEPALIVE, 0)
	}

	if err := syscall.SetsockoptInt(int(fd), syscall.SOL_SOCKET,
		syscall.SO_KEEPALIVE, 1); err != nil {
		return err
	}

	// Round up: a sub-second keepalive would truncate to zero, which the
	// kernel reads as "use the default" — two hours, silently.
	secs := int((opts.KeepAlive + time.Second - 1) / time.Second)
	if err := syscall.SetsockoptInt(int(fd), syscall.IPPROTO_TCP,
		syscall.TCP_KEEPIDLE, secs); err != nil {
		return err
	}
	return syscall.SetsockoptInt(int(fd), syscall.IPPROTO_TCP,
		syscall.TCP_KEEPINTVL, secs)
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
