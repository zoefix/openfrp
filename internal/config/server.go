package config

import (
	"fmt"
	"strings"
)

// Default server ports.
const (
	DefaultBindPort   = 7000
	DefaultAcceptLoop = 0 // 0 means "one per CPU"
)

// Server is the configuration for the openfrps daemon.
type Server struct {
	// BindAddr and BindPort accept client control connections.
	BindAddr string `json:"bind_addr,omitempty"`
	BindPort int    `json:"bind_port,omitempty"`

	// Token authenticates clients. Empty disables authentication, which is
	// only ever appropriate on a trusted network.
	Token string `json:"token,omitempty"`

	// VhostHTTPPort and VhostHTTPSPort serve domain-routed tunnels. Zero
	// disables that listener. Wired up in P1.
	VhostHTTPPort  int `json:"vhost_http_port,omitempty"`
	VhostHTTPSPort int `json:"vhost_https_port,omitempty"`

	// MaxPoolCount caps how many work connections a single client may
	// pre-establish, so one client cannot exhaust server file descriptors.
	MaxPoolCount int `json:"max_pool_count,omitempty"`

	// AcceptLoops is how many SO_REUSEPORT accept loops to run. Zero means
	// one per CPU, which removes accept-lock contention under high connection
	// churn. Set to 1 to disable.
	AcceptLoops int `json:"accept_loops,omitempty"`

	Log Log `json:"log,omitempty"`
}

// LoadServer reads and validates a server config from path.
func LoadServer(path string) (*Server, error) {
	var cfg Server
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
func (c *Server) ApplyDefaults() {
	if c.BindAddr == "" {
		c.BindAddr = "0.0.0.0"
	}
	if c.BindPort == 0 {
		c.BindPort = DefaultBindPort
	}
	if c.MaxPoolCount == 0 {
		c.MaxPoolCount = DefaultMaxPoolCount
	}
}

// Validate reports the first configuration problem found.
func (c *Server) Validate() error {
	// Zero means "let the kernel choose". ApplyDefaults substitutes the
	// standard port for an unset value, so a config loaded from disk never
	// reaches here with zero; only a caller that set it deliberately does.
	if c.BindPort != 0 {
		if err := validatePort(c.BindPort); err != nil {
			return fmt.Errorf("bind_port: %w", err)
		}
	}
	if c.VhostHTTPPort != 0 {
		if err := validatePort(c.VhostHTTPPort); err != nil {
			return fmt.Errorf("vhost_http_port: %w", err)
		}
	}
	if c.VhostHTTPSPort != 0 {
		if err := validatePort(c.VhostHTTPSPort); err != nil {
			return fmt.Errorf("vhost_https_port: %w", err)
		}
	}
	if c.VhostHTTPPort != 0 && c.VhostHTTPPort == c.VhostHTTPSPort {
		return fmt.Errorf("vhost_http_port and vhost_https_port must differ")
	}
	if c.BindPort != 0 && (c.BindPort == c.VhostHTTPPort || c.BindPort == c.VhostHTTPSPort) {
		return fmt.Errorf("bind_port %d collides with a vhost port", c.BindPort)
	}
	if c.MaxPoolCount < 1 {
		return fmt.Errorf("max_pool_count must be at least 1")
	}
	if c.AcceptLoops < 0 {
		return fmt.Errorf("accept_loops must not be negative")
	}
	if strings.TrimSpace(c.Token) == "" {
		// Not fatal, but the operator should know.
		return nil
	}
	return nil
}
