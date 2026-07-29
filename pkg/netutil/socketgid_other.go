//go:build !linux

package netutil

import (
	"fmt"
	"net"
)

const socketGIDSupported = false

func DialWithSocketGID(gid int, dial func() (net.Conn, error)) (net.Conn, error) {
	if gid <= 0 {
		return dial()
	}
	return nil, fmt.Errorf(
		"netutil: choosing a socket's owning group is a Linux facility, "+
			"and this is not Linux; asked for group %d", gid)
}

func SocketGIDSupported() bool { return socketGIDSupported }

func SetSocketMark(_ uintptr, mark int) error {
	if mark <= 0 {
		return nil
	}
	return fmt.Errorf("netutil: marking a socket is a Linux facility, "+
		"and this is not Linux; asked for mark %d", mark)
}
