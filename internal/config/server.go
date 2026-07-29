package config

import (
	"fmt"
	"strings"
)

const (
	DefaultBindPort   = 7000
	DefaultAcceptLoop = 0
)

type Server struct {
	BindAddr string `json:"bind_addr,omitempty"`
	BindPort int    `json:"bind_port,omitempty"`

	Token string `json:"token,omitempty"`

	VhostHTTPPort  int `json:"vhost_http_port,omitempty"`
	VhostHTTPSPort int `json:"vhost_https_port,omitempty"`

	MaxPoolCount int `json:"max_pool_count,omitempty"`

	AcceptLoops int `json:"accept_loops,omitempty"`

	Log Log `json:"log,omitempty"`
}

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

func (c *Server) Validate() error {

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

		return nil
	}
	return nil
}
