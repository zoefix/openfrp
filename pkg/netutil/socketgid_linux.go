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

const socketGIDSupported = true

func SocketGIDSupported() bool { return socketGIDSupported }

var processGID = syscall.Getgid()

var gidProbe struct {
	sync.Once
	err error
}

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

func SetSocketMark(fd uintptr, mark int) error {
	if mark <= 0 {
		return nil
	}
	return syscall.SetsockoptInt(int(fd), syscall.SOL_SOCKET, syscall.SO_MARK, mark)
}
