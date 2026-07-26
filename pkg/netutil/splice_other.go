//go:build !linux

package netutil

// spliceSupported is false everywhere except Linux. Relay still works, it just
// falls back to a buffered userspace copy.
//
// This matters for local development on macOS: CanSplice reports false there,
// so the fast-path assertions are skipped rather than failing spuriously. The
// server ships to Linux, which is where the guarantee has to hold.
const spliceSupported = false
