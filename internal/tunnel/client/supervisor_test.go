package client

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/zoefix/openfrp/internal/config"
)

// newTestSupervisor builds one over a discarding logger.
func newTestSupervisor(t *testing.T, cfg *config.Client) *Supervisor {
	t.Helper()
	supervisor, err := NewSupervisor(cfg, slog.New(slog.DiscardHandler), "test")
	if err != nil {
		t.Fatalf("NewSupervisor: %v", err)
	}
	return supervisor
}

// twoServers is a router publishing one tunnel on each of two servers. The
// second tunnel has to name its server, because only the first is implied.
func twoServers() *config.Client {
	cfg := &config.Client{
		Servers: []config.Upstream{
			{Name: "home", Addr: "198.51.100.1", Port: 7000, Token: "a"},
			{Name: "hk", Addr: "203.0.113.1", Port: 7000, Token: "b"},
		},
		Tunnels: []config.Tunnel{
			{Name: "nas", Enabled: true, Type: "tcp",
				LocalIP: "192.168.1.2", LocalPort: 80, RemotePort: 8080},
			{Name: "site", Enabled: true, Type: "tcp", Server: "hk",
				LocalIP: "192.168.1.3", LocalPort: 80, RemotePort: 8081},
		},
	}
	cfg.ApplyDefaults()
	return cfg
}

// A tunnel that names its server must still validate once the supervisor has
// narrowed the configuration down to that one server.
//
// The narrowed configuration carries the server in the single-server fields,
// where it is known by the default name rather than its own. A tunnel naming
// the server it belongs to then matched nothing and validated as an orphan, so
// every server but the first refused to start — which is the whole of the
// multi-server feature failing, silently, for anyone who used it.
func TestScopedConfigKeepsATunnelThatNamesItsServer(t *testing.T) {
	cfg := twoServers()
	supervisor := newTestSupervisor(t, cfg)

	for _, server := range cfg.Upstreams() {
		scoped, err := supervisor.scopedConfig(server)
		if err != nil {
			t.Fatalf("server %q: %v", server.Name, err)
		}
		if len(scoped.Tunnels) != 1 {
			t.Fatalf("server %q: got %d tunnels, want 1",
				server.Name, len(scoped.Tunnels))
		}
		if scoped.ServerAddr != server.Addr {
			t.Errorf("server %q: scoped to %q", server.Name, scoped.ServerAddr)
		}
	}
}

// The tunnels each server is given are its own, and nobody else's.
func TestScopedConfigSplitsTunnelsByServer(t *testing.T) {
	cfg := twoServers()
	supervisor := newTestSupervisor(t, cfg)

	got := map[string]string{}
	for _, server := range cfg.Upstreams() {
		scoped, err := supervisor.scopedConfig(server)
		if err != nil {
			t.Fatalf("server %q: %v", server.Name, err)
		}
		got[server.Name] = scoped.Tunnels[0].Name
	}

	if got["home"] != "nas" || got["hk"] != "site" {
		t.Errorf("tunnels landed on %v, want home=nas hk=site", got)
	}
}

// Narrowing must not edit the configuration it was narrowing from: the
// supervisor holds one copy and hands out a view of it per server.
func TestScopedConfigLeavesTheOriginalAlone(t *testing.T) {
	cfg := twoServers()
	supervisor := newTestSupervisor(t, cfg)

	if _, err := supervisor.scopedConfig(cfg.Upstreams()[1]); err != nil {
		t.Fatalf("scoping: %v", err)
	}

	if cfg.Tunnels[1].Server != "hk" {
		t.Errorf("the original tunnel now names %q, want hk", cfg.Tunnels[1].Server)
	}
}

// One server panicking must not end the process. Every other server in it
// loses its connections when it does, which is the failure this daemon exists
// to avoid — the tunnels are independent and one bug should not be all of
// their problem.
func TestAPanicStopsOneServerNotTheProcess(t *testing.T) {
	var logged strings.Builder
	supervisor, err := NewSupervisor(twoServers(),
		slog.New(slog.NewTextHandler(&logged, nil)), "test")
	if err != nil {
		t.Fatalf("NewSupervisor: %v", err)
	}

	done := make(chan struct{})
	go func() {
		defer close(done)
		supervisor.runServer(context.Background(), "hk",
			func(context.Context) error { panic("a bug in one server") })
	}()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("runServer did not return")
	}

	// The panic has to be findable afterwards: nothing else prints it now that
	// the runtime never sees it.
	out := logged.String()
	for _, want := range []string{"a bug in one server", "server=hk", "stack="} {
		if !strings.Contains(out, want) {
			t.Errorf("the log does not mention %q:\n%s", want, out)
		}
	}
}

// A server that stops with an error is reported, not swallowed.
func TestAServerErrorIsReported(t *testing.T) {
	var logged strings.Builder
	supervisor, err := NewSupervisor(twoServers(),
		slog.New(slog.NewTextHandler(&logged, nil)), "test")
	if err != nil {
		t.Fatalf("NewSupervisor: %v", err)
	}

	supervisor.runServer(context.Background(), "home",
		func(context.Context) error { return errors.New("dial failed") })

	if !strings.Contains(logged.String(), "dial failed") {
		t.Errorf("the error was not reported:\n%s", logged.String())
	}
}
