package client

import (
	"net"
	"net/netip"
)

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
