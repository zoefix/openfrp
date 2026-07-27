package config

import (
	"fmt"
	"net"
	"strings"

	"github.com/zoefix/openfrp/pkg/netutil"
)

// TunnelType identifies how a tunnel exposes its local service.
type TunnelType string

const (
	// TunnelTCP binds a dedicated port on the server.
	TunnelTCP TunnelType = "tcp"
	// TunnelUDP binds a dedicated UDP port on the server.
	TunnelUDP TunnelType = "udp"
	// TunnelHTTP shares the server's HTTP vhost port, routed by Host header.
	TunnelHTTP TunnelType = "http"
	// TunnelHTTPS shares the server's HTTPS vhost port, routed by TLS SNI.
	TunnelHTTPS TunnelType = "https"
	// TunnelSTCP exposes no public port; visitors authenticate with a secret.
	TunnelSTCP TunnelType = "stcp"
)

// TLSMode selects how a tunnel's TLS traffic is handled at the server edge.
type TLSMode string

const (
	// TLSNone carries plaintext; the server does no TLS work.
	TLSNone TLSMode = "none"
	// TLSPassthrough routes on the SNI without decrypting. The local service
	// owns the certificate. Cheapest option: no crypto on the server.
	TLSPassthrough TLSMode = "passthrough"
	// TLSTerminate decrypts at the server using a certificate this client
	// issued and pushed up, then forwards plaintext through the tunnel.
	TLSTerminate TLSMode = "terminate"
)

// Tunnel declares one exposed service.
type Tunnel struct {
	Name    string     `json:"name"`
	Enabled bool       `json:"enabled"`
	Type    TunnelType `json:"type"`

	// LocalIP and LocalPort address the service on the LAN side.
	LocalIP   string `json:"local_ip"`
	LocalPort int    `json:"local_port"`

	// RemotePort is required for tcp and udp tunnels.
	RemotePort int `json:"remote_port,omitempty"`

	// Domains is required for http and https tunnels. Patterns may use a
	// leading "*" label at any depth; see internal/tunnel/vhost.
	Domains []string `json:"domains,omitempty"`

	// TLSMode applies to https tunnels. Defaults to passthrough.
	TLSMode TLSMode `json:"tls_mode,omitempty"`

	// Server names the upstream this tunnel is published to. Empty means the
	// first one configured, which is what a single-server setup wants and what
	// a configuration written before servers had names meant.
	Server string `json:"server,omitempty"`

	// CertID binds an issued certificate to this tunnel, by the id it has in
	// the local database.
	//
	// Only a bound tunnel has a certificate pushed to the server. Terminating
	// TLS without one is an unfinished configuration rather than an invitation
	// to guess which of several certificates was meant — and guessing wrong
	// serves the wrong name, which a browser reports as an impersonation
	// attempt.
	CertID int `json:"cert_id,omitempty"`

	// SecretKey authenticates visitors to an stcp tunnel.
	SecretKey string `json:"secret_key,omitempty"`

	// ProxyProtocol makes the client announce the visitor's address to the
	// local service before relaying, so logs and rate limits see whoever
	// actually connected rather than this router.
	//
	// Empty disables it. The local service must be configured to expect the
	// header — it arrives where a request is expected, so a service that is
	// not looking for one rejects the connection. That is the right failure:
	// the alternative is every visitor being recorded as the router, which
	// looks like working software.
	ProxyProtocol string `json:"proxy_protocol,omitempty"`
}

// NeedsRemotePort reports whether this tunnel type binds its own server port.
func (t TunnelType) NeedsRemotePort() bool {
	return t == TunnelTCP || t == TunnelUDP
}

// NeedsDomains reports whether this tunnel type is routed by domain name.
func (t TunnelType) NeedsDomains() bool {
	return t == TunnelHTTP || t == TunnelHTTPS
}

// Valid reports whether t is a recognised tunnel type.
func (t TunnelType) Valid() bool {
	switch t {
	case TunnelTCP, TunnelUDP, TunnelHTTP, TunnelHTTPS, TunnelSTCP:
		return true
	}
	return false
}

// Valid reports whether m is a recognised TLS mode.
func (m TLSMode) Valid() bool {
	switch m {
	case TLSNone, TLSPassthrough, TLSTerminate:
		return true
	}
	return false
}

// applyDefaults fills in the values a user may reasonably omit.
func (t *Tunnel) applyDefaults() {
	if t.LocalIP == "" {
		t.LocalIP = "127.0.0.1"
	}
	if t.Type == TunnelHTTPS && t.TLSMode == "" {
		t.TLSMode = TLSPassthrough
	}
	if t.Type != TunnelHTTPS && t.TLSMode == "" {
		t.TLSMode = TLSNone
	}
}

// Validate checks the tunnel for internal consistency. It reports the first
// problem found, naming the tunnel so the LuCI form can point at the right row.
func (t *Tunnel) Validate() error {
	if strings.TrimSpace(t.Name) == "" {
		return fmt.Errorf("tunnel name must not be empty")
	}
	if !t.Type.Valid() {
		return fmt.Errorf("tunnel %q: unknown type %q", t.Name, t.Type)
	}
	if t.LocalIP != "" && net.ParseIP(t.LocalIP) == nil {
		// A hostname is acceptable too; only reject something that looks like a
		// malformed address rather than a name.
		if strings.ContainsAny(t.LocalIP, ":/ ") {
			return fmt.Errorf("tunnel %q: invalid local_ip %q", t.Name, t.LocalIP)
		}
	}
	if err := validatePort(t.LocalPort); err != nil {
		return fmt.Errorf("tunnel %q: local_port: %w", t.Name, err)
	}

	if t.Type.NeedsRemotePort() {
		// Zero is legal and means "let the server allocate a port". The
		// assigned port comes back in the publish response and is surfaced in
		// the status view.
		if t.RemotePort != 0 {
			if err := validatePort(t.RemotePort); err != nil {
				return fmt.Errorf("tunnel %q: remote_port: %w", t.Name, err)
			}
		}
	}

	if t.Type.NeedsDomains() {
		if len(t.Domains) == 0 {
			return fmt.Errorf("tunnel %q: type %s requires at least one domain", t.Name, t.Type)
		}
	}

	if !t.TLSMode.Valid() {
		return fmt.Errorf("tunnel %q: unknown tls_mode %q", t.Name, t.TLSMode)
	}
	if t.TLSMode != TLSNone && t.Type != TunnelHTTPS {
		return fmt.Errorf("tunnel %q: tls_mode %q only applies to https tunnels", t.Name, t.TLSMode)
	}

	if t.Type == TunnelSTCP && t.SecretKey == "" {
		return fmt.Errorf("tunnel %q: stcp requires a secret_key", t.Name)
	}

	if !netutil.ValidProxyProtocol(t.ProxyProtocol) {
		return fmt.Errorf("tunnel %q: unknown proxy_protocol %q, want v1 or v2",
			t.Name, t.ProxyProtocol)
	}
	if t.ProxyProtocol != "" && t.Type == TunnelUDP {
		return fmt.Errorf("tunnel %q: proxy_protocol needs a stream; UDP has none",
			t.Name)
	}

	return nil
}

func validatePort(p int) error {
	if p < 1 || p > 65535 {
		return fmt.Errorf("port %d out of range 1-65535", p)
	}
	return nil
}
