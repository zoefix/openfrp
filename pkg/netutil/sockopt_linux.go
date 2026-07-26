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
