//go:build !(linux || darwin || freebsd || netbsd || openbsd || dragonfly)

package netutil

import "errors"

const reusePortSupported = false

func setReusePort(uintptr) error {
	return errors.New("netutil: SO_REUSEPORT is unavailable on this platform")
}
