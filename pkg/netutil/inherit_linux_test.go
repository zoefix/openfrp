//go:build linux

package netutil

import (
	"net"
	"syscall"
	"testing"
)

// TestAcceptedSocketsInheritListenerOptions establishes the fact the
// per-connection tuning is allowed to rely on.
//
// Linux clones an accepted socket from the listening socket, and the clone
// carries the options that were set on the listener. That is what lets the
// data plane set TCP_NODELAY and the keepalive parameters once, at bind time,
// instead of paying three setsockopt syscalls on every connection that
// arrives — which at a hundred thousand connections is three hundred thousand
// syscalls spent re-stating something the kernel already knew.
//
// It is asserted rather than assumed because the whole optimisation rests on
// it, POSIX does not require it, and the failure mode is silent: a tunnel that
// still works but has Nagle switched back on, adding tens of milliseconds to
// every interactive round trip with nothing in any log to say why.
func TestAcceptedSocketsInheritListenerOptions(t *testing.T) {
	const (
		keepIdle  = 47 // seconds; distinctive, so a default cannot pass for it
		keepIntvl = 11
	)

	// KeepAlive is negative on purpose, and it is half the finding.
	//
	// Go treats zero as "on, with my default", and implements that by calling
	// setsockopt on every accepted connection — which both costs the syscalls
	// this test exists to remove and overwrites whatever the listener was
	// configured with. Measured here before the flag was set: an inherited
	// idle of 47 came back as 15, Go's default, not the kernel's 7200.
	//
	// Negative turns that off, leaving the accepted socket with exactly what
	// it was cloned with.
	cfg := NewListenConfig(ListenOptions{KeepAlive: -1})
	cfg.Control = func(_, _ string, rc syscall.RawConn) error {
		var err error
		rc.Control(func(fd uintptr) {
			if err = syscall.SetsockoptInt(int(fd), syscall.IPPROTO_TCP,
				syscall.TCP_NODELAY, 1); err != nil {
				return
			}
			if err = syscall.SetsockoptInt(int(fd), syscall.SOL_SOCKET,
				syscall.SO_KEEPALIVE, 1); err != nil {
				return
			}
			if err = syscall.SetsockoptInt(int(fd), syscall.IPPROTO_TCP,
				syscall.TCP_KEEPIDLE, keepIdle); err != nil {
				return
			}
			err = syscall.SetsockoptInt(int(fd), syscall.IPPROTO_TCP,
				syscall.TCP_KEEPINTVL, keepIntvl)
		})
		return err
	}

	ln, err := cfg.Listen(t.Context(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer ln.Close()

	go func() {
		conn, err := net.Dial("tcp", ln.Addr().String())
		if err == nil {
			defer conn.Close()
			// Hold it open until the assertions below have run.
			buf := make([]byte, 1)
			conn.Read(buf)
		}
	}()

	accepted, err := ln.Accept()
	if err != nil {
		t.Fatalf("accept: %v", err)
	}
	defer accepted.Close()

	raw, err := accepted.(*net.TCPConn).SyscallConn()
	if err != nil {
		t.Fatalf("SyscallConn: %v", err)
	}

	read := func(level, opt int) int {
		var value int
		var readErr error
		if err := raw.Control(func(fd uintptr) {
			value, readErr = syscall.GetsockoptInt(int(fd), level, opt)
		}); err != nil {
			t.Fatalf("control: %v", err)
		}
		if readErr != nil {
			t.Fatalf("getsockopt: %v", readErr)
		}
		return value
	}

	if got := read(syscall.IPPROTO_TCP, syscall.TCP_NODELAY); got != 1 {
		t.Errorf("TCP_NODELAY on the accepted socket = %d, want 1 inherited "+
			"from the listener; the per-connection tuning cannot be skipped", got)
	}
	if got := read(syscall.SOL_SOCKET, syscall.SO_KEEPALIVE); got != 1 {
		t.Errorf("SO_KEEPALIVE = %d, want 1 inherited", got)
	}
	if got := read(syscall.IPPROTO_TCP, syscall.TCP_KEEPIDLE); got != keepIdle {
		t.Errorf("TCP_KEEPIDLE = %d, want %d inherited", got, keepIdle)
	}
	if got := read(syscall.IPPROTO_TCP, syscall.TCP_KEEPINTVL); got != keepIntvl {
		t.Errorf("TCP_KEEPINTVL = %d, want %d inherited", got, keepIntvl)
	}
}
