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

// TCP_DEFER_ACCEPT is Linux-only; the BSD accept filters are a different
// mechanism with different semantics, so this is a silent no-op here and the
// listener behaves classically.
const deferAcceptSupported = false

func setDeferAccept(uintptr, int) error { return nil }

// The BSDs are not credited with option inheritance. Only Linux has been
// measured, and claiming it elsewhere would trade a few syscalls for a
// silently untuned connection — Nagle back on, and nothing to say so.
const acceptedInheritsOptions = false

func tuneListenerFD(uintptr, TCPOptions) error { return nil }
