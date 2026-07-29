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

func newTestSupervisor(t *testing.T, cfg *config.Client) *Supervisor {
	t.Helper()
	supervisor, err := NewSupervisor(cfg, slog.New(slog.DiscardHandler), "test")
	if err != nil {
		t.Fatalf("NewSupervisor: %v", err)
	}
	return supervisor
}

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

	out := logged.String()
	for _, want := range []string{"a bug in one server", "server=hk", "stack="} {
		if !strings.Contains(out, want) {
			t.Errorf("the log does not mention %q:\n%s", want, out)
		}
	}
}

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
