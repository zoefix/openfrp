package config

import (
	"fmt"
	"strconv"
	"time"
)

// Transport defaults.
//
// DefaultPoolCount is deliberately larger than frp's 5. Because we do not
// multiplex by default, each pooled connection is an independent TCP flow with
// its own congestion window, so a larger pool directly raises aggregate
// throughput on high bandwidth-delay-product links.
const (
	DefaultPoolCount    = 8
	DefaultMaxPoolCount = 64

	DefaultHeartbeatInterval = 20 * time.Second
	DefaultHeartbeatTimeout  = 90 * time.Second
	DefaultDialTimeout       = 10 * time.Second

	// DefaultMuxStreamWindow is applied only when multiplexing is explicitly
	// enabled. yamux ships a 256KB default, which caps a single stream at
	// window/RTT — roughly 2.5 MB/s at 100ms RTT no matter how fat the pipe
	// is. 8MB lifts that ceiling to ~80 MB/s at the same latency.
	DefaultMuxStreamWindow = 8 << 20 // 8 MiB
)

// Protocol selects the transport carrying the control and work connections.
type Protocol string

const (
	ProtocolTCP       Protocol = "tcp"
	ProtocolKCP       Protocol = "kcp"
	ProtocolQUIC      Protocol = "quic"
	ProtocolWebsocket Protocol = "websocket"
)

// Valid reports whether p is a recognised protocol.
func (p Protocol) Valid() bool {
	switch p {
	case ProtocolTCP, ProtocolKCP, ProtocolQUIC, ProtocolWebsocket:
		return true
	}
	return false
}

// Transport tunes how the client talks to the server.
type Transport struct {
	Protocol Protocol `json:"protocol,omitempty"`

	// Mux multiplexes every work stream over a single TCP connection.
	//
	// This defaults to FALSE, which is the opposite of frp. Multiplexing puts
	// all streams behind one congestion window and one retransmission queue,
	// so a single lost packet head-of-line blocks every tunnel at once, and
	// the yamux window caps per-stream throughput on high-latency links. It
	// also makes splice(2) impossible, forcing every byte through userspace.
	//
	// Enable it only when the number of sockets matters more than throughput.
	Mux bool `json:"mux,omitempty"`

	// MuxStreamWindow overrides the per-stream flow-control window when Mux
	// is enabled. Ignored otherwise.
	MuxStreamWindow int `json:"mux_stream_window,omitempty"`

	// PoolCount is how many idle work connections to keep pre-established.
	// Each one saves a full round trip when a user connection arrives.
	PoolCount int `json:"pool_count,omitempty"`

	// TLSEnable protects the control connection.
	TLSEnable bool `json:"tls_enable,omitempty"`

	HeartbeatInterval Duration `json:"heartbeat_interval,omitempty"`
	HeartbeatTimeout  Duration `json:"heartbeat_timeout,omitempty"`
	DialTimeout       Duration `json:"dial_timeout,omitempty"`
}

// Client is the configuration for the openfrpc daemon.
type Client struct {
	// Servers are the endpoints this router connects to, each with its own
	// control connection. A tunnel names the one it belongs to.
	Servers []Upstream `json:"servers,omitempty"`

	// The fields below are the single-server shape this configuration had
	// before it could hold more than one. They are still read — see
	// Client.Upstreams — so an upgrade needs no migration.
	ServerAddr string `json:"server_addr,omitempty"`
	ServerPort int    `json:"server_port,omitempty"`
	Token      string `json:"token,omitempty"`

	// Name identifies this client to the server. Empty means the server
	// assigns one, which is fine for single-client deployments.
	Name string `json:"name,omitempty"`

	Transport Transport `json:"transport,omitempty"`
	Tunnels   []Tunnel  `json:"tunnels,omitempty"`
	Log       Log       `json:"log,omitempty"`
}

// LoadClient reads and validates a client config from path.
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

// ApplyDefaults fills unset fields with their defaults.
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

// ApplyDefaults fills in the transport settings left unset.
//
// A method on Transport rather than inline in Client.ApplyDefaults because
// every server carries its own copy: one may multiplex over a lossy link while
// another does not, and the defaults have to be applied to each.
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

// Validate reports the first problem with the transport settings.
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

// Validate reports the first configuration problem found.
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

		// Port zero means "server allocates", so several tunnels may carry it
		// without conflicting. The key includes the server: two tunnels on
		// different servers may both bind 6022, and rejecting that would be
		// wrong now that there can be more than one.
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

// EnabledTunnels returns only the tunnels the user has switched on.
func (c *Client) EnabledTunnels() []Tunnel {
	out := make([]Tunnel, 0, len(c.Tunnels))
	for _, t := range c.Tunnels {
		if t.Enabled {
			out = append(out, t)
		}
	}
	return out
}
