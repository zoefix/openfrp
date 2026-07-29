//go:build darwin || freebsd || netbsd || openbsd || dragonfly

package netutil

import "syscall"

const soReusePort = 0x0200

const reusePortSupported = true

func setReusePort(fd uintptr) error {
	return syscall.SetsockoptInt(int(fd), syscall.SOL_SOCKET, soReusePort, 1)
}

const deferAcceptSupported = false

func setDeferAccept(uintptr, int) error { return nil }

const acceptedInheritsOptions = false

func tuneListenerFD(uintptr, TCPOptions) error { return nil }
