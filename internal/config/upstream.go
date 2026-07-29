package config

import (
	"fmt"
	"strings"
)

type Upstream struct {
	Name string `json:"name"`

	Kind string `json:"kind,omitempty"`

	Zone string `json:"zone,omitempty"`

	TunnelID string `json:"tunnel_id,omitempty"`

	Addr  string `json:"addr"`
	Port  int    `json:"port,omitempty"`
	Token string `json:"token,omitempty"`

	ClientName string `json:"client_name,omitempty"`

	SocketGID  int `json:"socket_gid,omitempty"`
	SocketMark int `json:"socket_mark,omitempty"`

	Transport Transport `json:"transport,omitempty"`
}

const KindCloudflare = "cloudflare"

func (u Upstream) IsCloudflare() bool { return u.Kind == KindCloudflare }

func (u *Upstream) ApplyDefaults() {
	if u.Port == 0 {
		u.Port = 7000
	}
	u.Transport.ApplyDefaults()
}

func (u *Upstream) Validate() error {
	if strings.TrimSpace(u.Name) == "" {
		return fmt.Errorf("config: a server needs a name")
	}

	if u.IsCloudflare() {
		if strings.TrimSpace(u.TunnelID) == "" {
			return fmt.Errorf("config: server %q has no Cloudflare tunnel yet; "+
				"finish setting it up", u.Name)
		}
		return nil
	}

	if strings.TrimSpace(u.Addr) == "" {
		return fmt.Errorf("config: server %q has no address", u.Name)
	}
	if u.Port < 1 || u.Port > 65535 {
		return fmt.Errorf("config: server %q has port %d out of range", u.Name, u.Port)
	}
	return u.Transport.Validate()
}

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

const DefaultUpstreamName = "server"

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
