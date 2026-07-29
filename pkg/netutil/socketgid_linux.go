//go:build linux

package netutil

import (
	"bufio"
	"fmt"
	"net"
	"os"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"syscall"
)

// socketGIDSupported reports that this platform can create a socket owned by
// a chosen group.
const socketGIDSupported = true

// SocketGIDSupported reports whether a socket's owning group can be chosen.
func SocketGIDSupported() bool { return socketGIDSupported }

// processGID is what the filesystem GID is restored to. Every change here is
// made on a locked thread and undone before it is released, so the value to
// go back to is always the one the process started with.
var processGID = syscall.Getgid()

// gidProbe runs the verification once rather than per connection.
var gidProbe struct {
	sync.Once
	err error
}

// DialWithSocketGID dials with the socket owned by gid rather than by the
// process's own group.
//
// It exists to get out from under a transparent proxy without touching the
// proxy. A router that redirects all outbound TCP into a local proxy has to
// exempt something, or the proxy's own connections would be redirected into
// itself; the usual exemption is the socket's owning group, and OpenClash
// spells it `meta skgid 65534 return` as the first rule of its output chain.
// Creating our sockets with that group is therefore not a trick played on the
// proxy — it is the door the proxy leaves open, used as intended.
//
// The group is fixed at socket creation and cannot be changed afterwards: the
// kernel copies it from the creating thread's filesystem GID into the socket,
// and nothing reads it back. So the switch has to happen around the socket
// call, which is why this wraps the dial rather than adjusting the connection
// once it exists.
//
// The thread is locked for the duration and the GID restored before it is
// released, so no other goroutine can be scheduled onto the thread while it
// is borrowed. Setfsgid is deliberate: Setgid would be broadcast to every
// thread in the process by the Go runtime, which is exactly what must not
// happen here.
func DialWithSocketGID(gid int, dial func() (net.Conn, error)) (net.Conn, error) {
	if gid <= 0 {
		return dial()
	}

	runtime.LockOSThread()
	defer runtime.UnlockOSThread()

	if err := syscall.Setfsgid(gid); err != nil {
		return nil, fmt.Errorf("netutil: take group %d for this socket: %w", gid, err)
	}
	defer syscall.Setfsgid(processGID)

	// Checked once, not per connection.
	//
	// setfsgid is the one syscall that reports failure by succeeding: it
	// returns the previous group and sets no error, so a process without the
	// privilege to change group is told nothing at all. Left unchecked, the
	// switch would appear to work while every connection continued through
	// the proxy it was turned on to avoid — and the two are indistinguishable
	// from this side, which is the whole difficulty.
	//
	// Reading it back costs a /proc parse, so it happens on the first dial
	// and the answer is kept.
	gidProbe.Do(func() {
		actual, err := threadFSGID()
		switch {
		case err != nil:
			gidProbe.err = fmt.Errorf("netutil: could not confirm the socket group: %w", err)
		case actual != gid:
			gidProbe.err = fmt.Errorf(
				"netutil: asked for socket group %d but the thread still has %d; "+
					"the daemon lacks the privilege to change group, so these "+
					"connections would go through the local proxy anyway", gid, actual)
		}
	})
	if gidProbe.err != nil {
		return nil, gidProbe.err
	}

	return dial()
}

// threadFSGID reads the calling thread's filesystem GID.
//
// /proc rather than a syscall, because the syscall that sets it discards the
// value Go could have read, and this is the only other place the kernel
// publishes it. thread-self is the important part: the process-wide file
// would report a different thread's credentials.
func threadFSGID() (int, error) {
	file, err := os.Open("/proc/thread-self/status")
	if err != nil {
		return 0, err
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "Gid:") {
			continue
		}
		// Gid: real effective saved filesystem
		fields := strings.Fields(line)
		if len(fields) < 5 {
			return 0, fmt.Errorf("netutil: unexpected %q", line)
		}
		return strconv.Atoi(fields[4])
	}
	if err := scanner.Err(); err != nil {
		return 0, err
	}
	return 0, fmt.Errorf("netutil: no Gid line in /proc/thread-self/status")
}

// SetSocketMark applies SO_MARK to a socket.
//
// The second of the two keys, and the one that is not OpenClash's. Proxies
// that intercept with TPROXY and policy routing — passwall, sing-box, the
// shadowsocks packages — exempt themselves by fwmark rather than by group, so
// a mark is what gets out from under those. It is also what an operator's own
// `ip rule` can match on if they would rather route this traffic by hand.
//
// Neither key opens every lock, and neither is a guarantee: a proxy that
// exempts neither intercepts this too. What makes the outcome knowable is not
// the key but the check — the server reports the address it saw the
// connection arrive from, so a bypass that did not work says so instead of
// looking exactly like one that did.
func SetSocketMark(fd uintptr, mark int) error {
	if mark <= 0 {
		return nil
	}
	return syscall.SetsockoptInt(int(fd), syscall.SOL_SOCKET, syscall.SO_MARK, mark)
}
