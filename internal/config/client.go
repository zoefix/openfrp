package config

import (
	"fmt"
	"strconv"
	"time"
)

const (
	DefaultPoolCount    = 8
	DefaultMaxPoolCount = 64

	DefaultHeartbeatInterval = 20 * time.Second
	DefaultHeartbeatTimeout  = 90 * time.Second
	DefaultDialTimeout       = 10 * time.Second

	DefaultMuxStreamWindow = 8 << 20
)

type Protocol string

const (
	ProtocolTCP       Protocol = "tcp"
	ProtocolKCP       Protocol = "kcp"
	ProtocolQUIC      Protocol = "quic"
	ProtocolWebsocket Protocol = "websocket"
)

func (p Protocol) Valid() bool {
	switch p {
	case ProtocolTCP, ProtocolKCP, ProtocolQUIC, ProtocolWebsocket:
		return true
	}
	return false
}

func (p Protocol) Implemented() bool { return p == "" || p == ProtocolTCP }

type Transport struct {
	Protocol Protocol `json:"protocol,omitempty"`

	Mux bool `json:"mux,omitempty"`

	MuxStreamWindow int `json:"mux_stream_window,omitempty"`

	PoolCount int `json:"pool_count,omitempty"`

	TLSEnable bool `json:"tls_enable,omitempty"`

	HeartbeatInterval Duration `json:"heartbeat_interval,omitempty"`
	HeartbeatTimeout  Duration `json:"heartbeat_timeout,omitempty"`
	DialTimeout       Duration `json:"dial_timeout,omitempty"`
}

type Client struct {
	Servers []Upstream `json:"servers,omitempty"`

	ServerAddr string `json:"server_addr,omitempty"`
	ServerPort int    `json:"server_port,omitempty"`
	Token      string `json:"token,omitempty"`

	Name string `json:"name,omitempty"`

	Transport Transport `json:"transport,omitempty"`

	SocketGID  int `json:"socket_gid,omitempty"`
	SocketMark int `json:"socket_mark,omitempty"`

	DownRate int64 `json:"down_rate,omitempty"`
	UpRate   int64 `json:"up_rate,omitempty"`
	Quota    int64 `json:"quota,omitempty"`

	Tunnels []Tunnel `json:"tunnels,omitempty"`
	Log     Log      `json:"log,omitempty"`
}

func LoadClient(path string) (*Client, error) {
	var cfg Client
	if err := loadJSON(path, &cfg); err != nil {
		return nil, err
	}
	cfg.ApplyDefaults()
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	return &cfg, nil
}

func (c *Client) ApplyDefaults() {
	if c.ServerPort == 0 {
		c.ServerPort = DefaultBindPort
	}
	c.Transport.ApplyDefaults()

	for i := range c.Servers {
		c.Servers[i].ApplyDefaults()
	}
	for i := range c.Tunnels {
		c.Tunnels[i].applyDefaults()
	}
}

func (t *Transport) ApplyDefaults() {
	if t.Protocol == "" {
		t.Protocol = ProtocolTCP
	}
	if t.PoolCount == 0 {
		t.PoolCount = DefaultPoolCount
	}
	if t.MuxStreamWindow == 0 {
		t.MuxStreamWindow = DefaultMuxStreamWindow
	}
	if t.HeartbeatInterval == 0 {
		t.HeartbeatInterval = Duration(DefaultHeartbeatInterval)
	}
	if t.HeartbeatTimeout == 0 {
		t.HeartbeatTimeout = Duration(DefaultHeartbeatTimeout)
	}
	if t.DialTimeout == 0 {
		t.DialTimeout = Duration(DefaultDialTimeout)
	}
}

func (t Transport) Validate() error {
	if !t.Protocol.Valid() {
		return fmt.Errorf("transport.protocol: unknown value %q", t.Protocol)
	}
	if t.PoolCount < 1 {
		return fmt.Errorf("transport.pool_count must be at least 1")
	}
	if t.PoolCount > DefaultMaxPoolCount {
		return fmt.Errorf("transport.pool_count %d exceeds the maximum of %d",
			t.PoolCount, DefaultMaxPoolCount)
	}
	if t.Mux && t.MuxStreamWindow < 64<<10 {
		return fmt.Errorf("transport.mux_stream_window %d is too small; "+
			"below 64KiB it throttles every stream", t.MuxStreamWindow)
	}
	if t.HeartbeatTimeout <= t.HeartbeatInterval {
		return fmt.Errorf("transport.heartbeat_timeout must exceed heartbeat_interval")
	}
	return nil
}

func (c *Client) Validate() error {
	servers := c.Upstreams()
	if len(servers) == 0 {
		return fmt.Errorf("no servers are configured")
	}

	names := make(map[string]struct{}, len(servers))
	for _, server := range servers {
		if err := server.Validate(); err != nil {
			return err
		}
		if _, dup := names[server.Name]; dup {
			return fmt.Errorf("duplicate server name %q", server.Name)
		}
		names[server.Name] = struct{}{}
	}

	for _, tunnel := range c.OrphanTunnels() {
		return fmt.Errorf("tunnel %q names server %q, which does not exist",
			tunnel.Name, tunnel.Server)
	}

	seen := make(map[string]struct{}, len(c.Tunnels))
	portsByServer := make(map[string]string)
	for i := range c.Tunnels {
		tun := &c.Tunnels[i]
		if err := tun.Validate(); err != nil {
			return err
		}
		if _, dup := seen[tun.Name]; dup {
			return fmt.Errorf("duplicate tunnel name %q", tun.Name)
		}
		seen[tun.Name] = struct{}{}

		key := tun.Server + "/" + strconv.Itoa(tun.RemotePort)
		if tun.Enabled && tun.Type.NeedsRemotePort() && tun.RemotePort != 0 {
			if other, clash := portsByServer[key]; clash {
				return fmt.Errorf("tunnels %q and %q both request remote_port %d "+
					"on the same server", other, tun.Name, tun.RemotePort)
			}
			portsByServer[key] = tun.Name
		}
	}
	return nil
}

func (c *Client) EnabledTunnels() []Tunnel {
	out := make([]Tunnel, 0, len(c.Tunnels))
	for _, t := range c.Tunnels {
		if t.Enabled {
			out = append(out, t)
		}
	}
	return out
}
