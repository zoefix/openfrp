package client

import (
	"context"
	"fmt"
	"log/slog"
	"runtime/debug"
	"sync"

	"github.com/zoefix/openfrp/internal/config"
	"github.com/zoefix/openfrp/internal/stats"
)

type Supervisor struct {
	cfg     *config.Client
	logger  *slog.Logger
	version string

	traffic   *stats.Registry
	statsPath string
	certs     CertSource

	mu      sync.Mutex
	clients map[string]*Client
}

func NewSupervisor(cfg *config.Client, logger *slog.Logger, version string) (*Supervisor, error) {
	if cfg == nil {
		return nil, fmt.Errorf("client: nil config")
	}
	if logger == nil {
		logger = slog.Default()
	}
	if len(cfg.Upstreams()) == 0 {
		return nil, fmt.Errorf("client: no servers are configured")
	}

	return &Supervisor{
		cfg:       cfg,
		logger:    logger,
		version:   version,
		traffic:   stats.NewRegistry(),
		statsPath: DefaultStatsPath,
		clients:   map[string]*Client{},
	}, nil
}

func (s *Supervisor) SetCertSource(source CertSource) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.certs = source
}

func (s *Supervisor) SetStatsPath(path string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.statsPath = path
}

func (s *Supervisor) Traffic() *stats.Registry { return s.traffic }

func (s *Supervisor) Run(ctx context.Context) error {
	servers := s.cfg.Upstreams()

	for _, tunnel := range s.cfg.OrphanTunnels() {
		s.logger.Error("tunnel names a server that does not exist",
			"tunnel", tunnel.Name, "server", tunnel.Server)
	}

	publisher := s.publisher()
	go publisher.publishTraffic(ctx)
	defer publisher.removeTraffic()

	var (
		wg      sync.WaitGroup
		started int
	)

	for _, server := range servers {

		if server.IsCloudflare() {
			s.logger.Info("server is published by cloudflared",
				"server", server.Name, "tunnel", server.TunnelID)
			continue
		}

		scoped, err := s.scopedConfig(server)
		if err != nil {
			s.logger.Error("server is not usable", "server", server.Name, "error", err)
			continue
		}

		client, err := New(scoped, s.logger.With("server", server.Name), s.version)
		if err != nil {
			s.logger.Error("server is not usable", "server", server.Name, "error", err)
			continue
		}

		client.traffic = s.traffic
		client.SetStatsPath("")
		if s.certs != nil {
			client.SetCertSource(s.certs)
		}

		s.mu.Lock()
		s.clients[server.Name] = client
		s.mu.Unlock()

		started++
		wg.Add(1)
		go func() {
			defer wg.Done()
			s.runServer(ctx, server.Name, client.Run)
		}()
	}

	if started == 0 {
		return fmt.Errorf("client: none of the %d configured servers is usable", len(servers))
	}

	s.logger.Info("connecting", "servers", started, "tunnels", len(s.cfg.EnabledTunnels()))

	wg.Wait()
	return nil
}

func (s *Supervisor) runServer(ctx context.Context, name string,
	run func(context.Context) error) {

	defer func() {
		if panicked := recover(); panicked != nil {
			s.logger.Error("server stopped after a panic",
				"server", name, "panic", panicked,
				"stack", string(debug.Stack()))
		}
	}()

	if err := run(ctx); err != nil {
		s.logger.Error("server stopped", "server", name, "error", err)
	}
}

func (s *Supervisor) scopedConfig(server config.Upstream) (*config.Client, error) {

	tunnels := s.cfg.TunnelsFor(server.Name)
	for i := range tunnels {
		tunnels[i].Server = ""
	}

	scoped := &config.Client{
		ServerAddr: server.Addr,
		ServerPort: server.Port,
		Token:      server.Token,
		Name:       server.ClientName,
		Transport:  server.Transport,
		SocketGID:  server.SocketGID,
		SocketMark: server.SocketMark,
		Tunnels:    tunnels,
		Log:        s.cfg.Log,
	}
	scoped.ApplyDefaults()

	if err := scoped.Validate(); err != nil {
		return nil, err
	}
	return scoped, nil
}

func (s *Supervisor) publisher() *Client {
	s.mu.Lock()
	defer s.mu.Unlock()

	return &Client{
		logger:       s.logger,
		version:      s.version,
		traffic:      s.traffic,
		statsPath:    s.statsPath,
		serverStates: s.serverStates,
	}
}

func (s *Supervisor) serverStates() map[string]ServerSnapshot {
	s.mu.Lock()
	defer s.mu.Unlock()

	out := make(map[string]ServerSnapshot, len(s.clients))
	for name, client := range s.clients {
		version, connected := client.ServerState()
		out[name] = ServerSnapshot{Connected: connected, Version: version}
	}
	return out
}
