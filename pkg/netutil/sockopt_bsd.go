//go:build darwin || freebsd || netbsd || openbsd || dragonfly

package netutil

import "syscall"

// soReusePort is SO_REUSEPORT on the BSD socket stacks, which use a different
// value from Linux. This path exists mainly so macOS development mirrors
// production behaviour instead of silently skipping the option.
const soReusePort = 0x0200

const reusePortSupported = true

// setReusePort enables SO_REUSEPORT so several listeners can bind the same
// address and the kernel spreads incoming connections across them.
func setReusePort(fd uintptr) error {
	return syscall.SetsockoptInt(int(fd), syscall.SOL_SOCKET, soReusePort, 1)
}
