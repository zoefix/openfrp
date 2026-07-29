//go:build !linux

package netutil

import (
	"fmt"
	"net"
)

// socketGIDSupported reports that this platform cannot create a socket owned
// by a chosen group.
const socketGIDSupported = false

// DialWithSocketGID refuses rather than dialling.
//
// Silently ignoring the request would be worse than failing it: the caller
// asked to get out from under a transparent proxy, and a connection that
// quietly went through the proxy anyway would look identical to one that did
// not, right up until someone wondered why the traffic was still slow.
func DialWithSocketGID(gid int, dial func() (net.Conn, error)) (net.Conn, error) {
	if gid <= 0 {
		return dial()
	}
	return nil, fmt.Errorf(
		"netutil: choosing a socket's owning group is a Linux facility, "+
			"and this is not Linux; asked for group %d", gid)
}

// SocketGIDSupported reports whether a socket's owning group can be chosen.
func SocketGIDSupported() bool { return socketGIDSupported }

// SetSocketMark refuses, for the same reason as above: a mark that was not
// applied leaves traffic going through a proxy while looking as though it
// does not.
func SetSocketMark(_ uintptr, mark int) error {
	if mark <= 0 {
		return nil
	}
	return fmt.Errorf("netutil: marking a socket is a Linux facility, "+
		"and this is not Linux; asked for mark %d", mark)
}
