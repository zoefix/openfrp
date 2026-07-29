package client

import (
	"net"
	"net/netip"
)

// reportEgress says whether the connection went where it was told to.
//
// This is the part that turns a bypass from a hope into a fact. The two keys
// the dialer can present — the socket's owning group, and its fwmark — each
// open one kind of lock, and a proxy that recognises neither intercepts the
// connection anyway. From this side the two outcomes are identical: the
// socket reports the address we asked for either way, because a redirect is
// transparent to the process being redirected.
//
// The server is the only party that can tell them apart, because it sees
// where the connection actually came from. A connection that left through the
// WAN arrives from this router's own public address; one that was redirected
// into a local proxy arrives from wherever that proxy exits, which is
// somewhere else entirely and usually further away.
//
// So the check is comparative rather than absolute — we do not know this
// router's public address, and asking something on the internet for it would
// itself be a request the proxy could intercept. What is reported is the
// address, once, for the operator to recognise. Someone who turned the
// bypass on and sees their proxy's exit node has learnt exactly what they
// needed to.
func (c *Client) reportEgress(observed string) {
	if observed == "" {
		return
	}

	host, _, err := net.SplitHostPort(observed)
	if err != nil {
		host = observed
	}
	addr, err := netip.ParseAddr(host)
	if err != nil {
		return
	}

	bypassing := c.dialer.SocketGID > 0 || c.dialer.SocketMark > 0
	if !bypassing {
		return
	}

	// A private address means something in the middle rewrote the source, and
	// that something is not a route to the internet — it is a proxy on the
	// path. Worth a warning rather than a note.
	if addr.IsPrivate() || addr.IsLoopback() || addr.IsLinkLocalUnicast() {
		c.logger.Warn("the direct-egress switch is on, but the server sees "+
			"this connection arriving from a private address, so something "+
			"local is still relaying it",
			"seen_by_server_as", host)
		return
	}

	c.logger.Info("direct egress is on; check this is this router's own "+
		"public address and not a proxy's exit",
		"seen_by_server_as", host)
}
