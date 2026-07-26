//go:build linux

package netutil

// spliceSupported is true on Linux, where net.TCPConn.ReadFrom is backed by
// splice(2) and payload bytes move between sockets without a userspace copy.
const spliceSupported = true
