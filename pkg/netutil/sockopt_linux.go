//go:build linux

package netutil

import (
	"syscall"
	"time"
)

const soReusePort = 0x0F

const reusePortSupported = true

func setReusePort(fd uintptr) error {
	return syscall.SetsockoptInt(int(fd), syscall.SOL_SOCKET, soReusePort, 1)
}

const acceptedInheritsOptions = true

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

	secs := int((opts.KeepAlive + time.Second - 1) / time.Second)
	if err := syscall.SetsockoptInt(int(fd), syscall.IPPROTO_TCP,
		syscall.TCP_KEEPIDLE, secs); err != nil {
		return err
	}
	return syscall.SetsockoptInt(int(fd), syscall.IPPROTO_TCP,
		syscall.TCP_KEEPINTVL, secs)
}

const deferAcceptSupported = true

func setDeferAccept(fd uintptr, seconds int) error {
	return syscall.SetsockoptInt(int(fd), syscall.IPPROTO_TCP, syscall.TCP_DEFER_ACCEPT, seconds)
}
