package config

import (
	"fmt"
	"net"
	"strings"

	"github.com/zoefix/openfrp/pkg/netutil"
)

type TunnelType string

const (
	TunnelTCP TunnelType = "tcp"

	TunnelUDP TunnelType = "udp"

	TunnelHTTP TunnelType = "http"

	TunnelHTTPS TunnelType = "https"

	TunnelSTCP TunnelType = "stcp"
)

type TLSMode string

const (
	TLSNone TLSMode = "none"

	TLSPassthrough TLSMode = "passthrough"

	TLSTerminate TLSMode = "terminate"
)

type Tunnel struct {
	Name    string     `json:"name"`
	Enabled bool       `json:"enabled"`
	Type    TunnelType `json:"type"`

	LocalIP   string `json:"local_ip"`
	LocalPort int    `json:"local_port"`

	RemotePort int `json:"remote_port,omitempty"`

	Domains []string `json:"domains,omitempty"`

	TLSMode TLSMode `json:"tls_mode,omitempty"`

	Server string `json:"server,omitempty"`

	CertID int `json:"cert_id,omitempty"`

	SecretKey string `json:"secret_key,omitempty"`

	ProxyProtocol string `json:"proxy_protocol,omitempty"`
}

func (t TunnelType) NeedsRemotePort() bool {
	return t == TunnelTCP || t == TunnelUDP
}

func (t TunnelType) NeedsDomains() bool {
	return t == TunnelHTTP || t == TunnelHTTPS
}

func (t TunnelType) Valid() bool {
	switch t {
	case TunnelTCP, TunnelUDP, TunnelHTTP, TunnelHTTPS, TunnelSTCP:
		return true
	}
	return false
}

func (m TLSMode) Valid() bool {
	switch m {
	case TLSNone, TLSPassthrough, TLSTerminate:
		return true
	}
	return false
}

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

func (t *Tunnel) Validate() error {
	if strings.TrimSpace(t.Name) == "" {
		return fmt.Errorf("tunnel name must not be empty")
	}
	if !t.Type.Valid() {
		return fmt.Errorf("tunnel %q: unknown type %q", t.Name, t.Type)
	}
	if t.LocalIP != "" && net.ParseIP(t.LocalIP) == nil {

		if strings.ContainsAny(t.LocalIP, ":/ ") {
			return fmt.Errorf("tunnel %q: invalid local_ip %q", t.Name, t.LocalIP)
		}
	}
	if err := validatePort(t.LocalPort); err != nil {
		return fmt.Errorf("tunnel %q: local_port: %w", t.Name, err)
	}

	if t.Type.NeedsRemotePort() {

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
