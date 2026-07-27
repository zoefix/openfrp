package config

import (
	"fmt"
	"strings"
)

// Upstream is one server this router connects to.
//
// Called Upstream rather than Server because the package already has a Server
// for the daemon's own configuration; this is the thing at the other end of
// the control connection. The UI calls it a server, which is what it is to an
// operator.
type Upstream struct {
	// Name identifies the server within this router's configuration, and is
	// what a tunnel points at. It is local — the server never sees it.
	Name string `json:"name"`

	Addr  string `json:"addr"`
	Port  int    `json:"port,omitempty"`
	Token string `json:"token,omitempty"`

	// ClientName identifies this router to the server. Empty lets the server
	// assign one, which is fine until several routers share a server.
	ClientName string `json:"client_name,omitempty"`

	Transport Transport `json:"transport,omitempty"`
}

// ApplyDefaults fills in what was left out.
func (u *Upstream) ApplyDefaults() {
	if u.Port == 0 {
		u.Port = 7000
	}
	u.Transport.ApplyDefaults()
}

// Validate reports what would stop this server being usable.
func (u *Upstream) Validate() error {
	if strings.TrimSpace(u.Name) == "" {
		return fmt.Errorf("config: a server needs a name")
	}
	if strings.TrimSpace(u.Addr) == "" {
		return fmt.Errorf("config: server %q has no address", u.Name)
	}
	if u.Port < 1 || u.Port > 65535 {
		return fmt.Errorf("config: server %q has port %d out of range", u.Name, u.Port)
	}
	return u.Transport.Validate()
}

// Upstreams returns the servers to connect to.
//
// A configuration written before this router could hold more than one server
// carries the single set of fields instead of a list. Rather than migrate the
// file, the old shape is read as a list of one — an upgrade should not require
// the operator to do anything, and a half-migrated file is worse than either
// shape.
func (c *Client) Upstreams() []Upstream {
	if len(c.Servers) > 0 {
		return c.Servers
	}
	if c.ServerAddr == "" {
		return nil
	}

	return []Upstream{{
		Name:       DefaultUpstreamName,
		Addr:       c.ServerAddr,
		Port:       c.ServerPort,
		Token:      c.Token,
		ClientName: c.Name,
		Transport:  c.Transport,
	}}
}

// DefaultUpstreamName is the name given to a server carried in the old
// single-server fields, and the one a tunnel means when it names none.
const DefaultUpstreamName = "server"

// Upstream finds a server by name. An empty name means the first, so a
// configuration with one server needs no cross-references at all.
func (c *Client) Upstream(name string) (Upstream, bool) {
	servers := c.Upstreams()
	if len(servers) == 0 {
		return Upstream{}, false
	}
	if name == "" {
		return servers[0], true
	}

	for _, server := range servers {
		if server.Name == name {
			return server, true
		}
	}
	return Upstream{}, false
}

// TunnelsFor returns the enabled tunnels belonging to a server.
//
// A tunnel that names no server belongs to the first one. That keeps the
// common case — one server — free of bookkeeping, and it is what an existing
// configuration means when it was written before servers had names.
func (c *Client) TunnelsFor(server string) []Tunnel {
	servers := c.Upstreams()
	first := ""
	if len(servers) > 0 {
		first = servers[0].Name
	}

	out := make([]Tunnel, 0, len(c.Tunnels))
	for _, tunnel := range c.Tunnels {
		if !tunnel.Enabled {
			continue
		}

		owner := tunnel.Server
		if owner == "" {
			owner = first
		}
		if owner == server {
			out = append(out, tunnel)
		}
	}
	return out
}

// OrphanTunnels returns enabled tunnels naming a server that does not exist.
//
// Reported rather than silently dropped: a tunnel that stopped working because
// its server was renamed should say so, not disappear.
func (c *Client) OrphanTunnels() []Tunnel {
	known := map[string]bool{}
	for _, server := range c.Upstreams() {
		known[server.Name] = true
	}

	var out []Tunnel
	for _, tunnel := range c.Tunnels {
		if tunnel.Enabled && tunnel.Server != "" && !known[tunnel.Server] {
			out = append(out, tunnel)
		}
	}
	return out
}
