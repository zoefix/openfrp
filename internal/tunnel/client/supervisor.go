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

// Supervisor runs one client per configured server.
//
// Each server gets its own control connection, its own reconnect backoff and
// its own set of tunnels, because they are independent: one server being down
// must not stop the tunnels published to another. What they share is the
// traffic registry and the certificate source, so the status page reports one
// set of totals and a certificate can be bound to a tunnel on any of them.
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

// NewSupervisor builds a supervisor over every configured server.
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

// SetCertSource attaches the source used to resolve certificate bindings.
func (s *Supervisor) SetCertSource(source CertSource) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.certs = source
}

// SetStatsPath changes where the traffic snapshot is published.
func (s *Supervisor) SetStatsPath(path string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.statsPath = path
}

// Traffic returns the shared counters.
func (s *Supervisor) Traffic() *stats.Registry { return s.traffic }

// Run connects to every server and keeps them connected until ctx is
// cancelled.
//
// A server whose configuration is unusable is reported and skipped rather than
// failing the whole daemon: one mistyped address should not take every other
// tunnel offline.
func (s *Supervisor) Run(ctx context.Context) error {
	servers := s.cfg.Upstreams()

	for _, tunnel := range s.cfg.OrphanTunnels() {
		s.logger.Error("tunnel names a server that does not exist",
			"tunnel", tunnel.Name, "server", tunnel.Server)
	}

	// Traffic is published once, by the supervisor, rather than by each
	// client: they would otherwise take turns overwriting the same file with
	// their own share of the totals.
	publisher := s.publisher()
	go publisher.publishTraffic(ctx)
	defer publisher.removeTraffic()

	var (
		wg      sync.WaitGroup
		started int
	)

	for _, server := range servers {
		// A Cloudflare tunnel is served by cloudflared, not by this daemon.
		// There is no openfrps to log in to and no control connection to hold,
		// so opening one would be dialling an address that does not exist.
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

		// The registry is shared and the file is the supervisor's, so the
		// client accumulates but does not publish.
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

// runServer runs one server's client and contains a panic to that server.
//
// A panic in one client used to end the process, and with it every other
// server's connections. That is the one place where servers sharing a process
// is worse than a process each, and it is the whole of the difference: the
// server that hit the bug stops, and the rest keep running.
//
// The stack is logged because nothing else will print it now. A panic that
// reaches the runtime writes one on the way out; a recovered one leaves no
// trace unless it is asked for, and a contained fault nobody can diagnose is
// only half fixed.
//
// Not retried. Reconnecting is already the client's own job, so what is left
// here is a bug, and running a bug straight back is how an isolated fault
// becomes a busy loop.
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

// scopedConfig narrows the configuration to one server and its tunnels.
//
// Each client is handed a single-server configuration, which is what it has
// always understood. Keeping the multi-server knowledge here rather than
// threading it through the session, work pool and forwarder is what makes this
// change small.
func (s *Supervisor) scopedConfig(server config.Upstream) (*config.Client, error) {
	// One server per client, carried in the single-server fields because that
	// is what a Client reads. Inside that configuration the server answers to
	// the default name, not the one it has out here — so a tunnel that named
	// its server explicitly would no longer match it and would validate as an
	// orphan, taking the whole server down with it. The name is dropped: there
	// is exactly one server here, and it is the one these tunnels belong to.
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
		Tunnels:    tunnels,
		Log:        s.cfg.Log,
	}
	scoped.ApplyDefaults()

	if err := scoped.Validate(); err != nil {
		return nil, err
	}
	return scoped, nil
}

// publisher returns a client used only to write the shared traffic snapshot.
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

// serverStates snapshots every server's control-connection state for the
// published document.
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
