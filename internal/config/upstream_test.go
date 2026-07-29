package config

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestOldSingleServerConfigStillWorks(t *testing.T) {
	var cfg Client
	err := json.Unmarshal([]byte(`{
		"server_addr": "64.83.33.99",
		"server_port": 7000,
		"token": "shared",
		"transport": {"protocol": "tcp", "pool_count": 4},
		"tunnels": [{"name": "web", "enabled": true, "type": "tcp",
		             "local_ip": "192.168.9.2", "local_port": 80, "remote_port": 8080}]
	}`), &cfg)
	if err != nil {
		t.Fatal(err)
	}
	cfg.ApplyDefaults()

	if err := cfg.Validate(); err != nil {
		t.Fatalf("an existing configuration stopped validating: %v", err)
	}

	servers := cfg.Upstreams()
	if len(servers) != 1 {
		t.Fatalf("got %d servers, want 1", len(servers))
	}
	if servers[0].Addr != "64.83.33.99" || servers[0].Port != 7000 {
		t.Errorf("server = %s:%d", servers[0].Addr, servers[0].Port)
	}
	if servers[0].Token != "shared" {
		t.Errorf("token was lost: %q", servers[0].Token)
	}

	if got := cfg.TunnelsFor(servers[0].Name); len(got) != 1 {
		t.Errorf("the tunnel was not assigned to the only server: %d", len(got))
	}
}

func multiServer(t *testing.T) *Client {
	t.Helper()

	cfg := &Client{
		Servers: []Upstream{
			{Name: "main", Addr: "203.0.113.1", Port: 7000},
			{Name: "backup", Addr: "203.0.113.2", Port: 7100},
		},
		Tunnels: []Tunnel{
			{Name: "unassigned", Enabled: true, Type: TunnelTCP,
				LocalIP: "10.0.0.1", LocalPort: 80, RemotePort: 8080},
			{Name: "on-backup", Enabled: true, Type: TunnelTCP, Server: "backup",
				LocalIP: "10.0.0.2", LocalPort: 80, RemotePort: 8081},
			{Name: "switched-off", Enabled: false, Type: TunnelTCP, Server: "backup",
				LocalIP: "10.0.0.3", LocalPort: 80},
		},
	}
	cfg.ApplyDefaults()
	return cfg
}

func TestTunnelsAreSplitByServer(t *testing.T) {
	cfg := multiServer(t)

	main := cfg.TunnelsFor("main")
	if len(main) != 1 || main[0].Name != "unassigned" {
		t.Errorf("main got %v, want the unassigned tunnel", names(main))
	}

	backup := cfg.TunnelsFor("backup")
	if len(backup) != 1 || backup[0].Name != "on-backup" {
		t.Errorf("backup got %v, want on-backup only", names(backup))
	}
}

func TestOrphanTunnelsAreReported(t *testing.T) {
	cfg := multiServer(t)
	cfg.Tunnels = append(cfg.Tunnels, Tunnel{
		Name: "lost", Enabled: true, Type: TunnelTCP, Server: "deleted",
		LocalIP: "10.0.0.9", LocalPort: 80, RemotePort: 9000,
	})

	orphans := cfg.OrphanTunnels()
	if len(orphans) != 1 || orphans[0].Name != "lost" {
		t.Fatalf("got %v, want the tunnel naming a missing server", names(orphans))
	}

	err := cfg.Validate()
	if err == nil {
		t.Fatal("a tunnel naming a missing server was accepted")
	}
	if !strings.Contains(err.Error(), "deleted") {
		t.Errorf("the error does not name the missing server: %v", err)
	}
}

func TestSamePortOnDifferentServersIsAllowed(t *testing.T) {
	cfg := &Client{
		Servers: []Upstream{
			{Name: "main", Addr: "203.0.113.1"},
			{Name: "backup", Addr: "203.0.113.2"},
		},
		Tunnels: []Tunnel{
			{Name: "ssh-main", Enabled: true, Type: TunnelTCP, Server: "main",
				LocalIP: "10.0.0.1", LocalPort: 22, RemotePort: 6022},
			{Name: "ssh-backup", Enabled: true, Type: TunnelTCP, Server: "backup",
				LocalIP: "10.0.0.2", LocalPort: 22, RemotePort: 6022},
		},
	}
	cfg.ApplyDefaults()

	if err := cfg.Validate(); err != nil {
		t.Fatalf("the same port on two different servers was rejected: %v", err)
	}

	cfg.Tunnels[1].Server = "main"
	if err := cfg.Validate(); err == nil {
		t.Error("two tunnels on one server both bound 6022")
	}
}

func TestDuplicateServerNamesAreRejected(t *testing.T) {
	cfg := &Client{
		Servers: []Upstream{
			{Name: "main", Addr: "203.0.113.1"},
			{Name: "main", Addr: "203.0.113.2"},
		},
	}
	cfg.ApplyDefaults()

	if err := cfg.Validate(); err == nil {
		t.Error("two servers shared a name, so a tunnel could not name either unambiguously")
	}
}

func TestEachServerKeepsItsOwnTransport(t *testing.T) {
	cfg := &Client{
		Servers: []Upstream{
			{Name: "clean", Addr: "203.0.113.1"},
			{Name: "lossy", Addr: "203.0.113.2", Transport: Transport{Mux: true, PoolCount: 32}},
		},
	}
	cfg.ApplyDefaults()

	if cfg.Servers[0].Transport.Mux {
		t.Error("multiplexing leaked onto a server that did not ask for it")
	}
	if !cfg.Servers[1].Transport.Mux {
		t.Error("multiplexing was dropped from the server that asked for it")
	}
	if cfg.Servers[0].Transport.PoolCount == 0 {
		t.Error("defaults were not applied per server")
	}
	if cfg.Servers[1].Transport.PoolCount != 32 {
		t.Errorf("pool count = %d, want the configured 32", cfg.Servers[1].Transport.PoolCount)
	}
}

func names(tunnels []Tunnel) []string {
	out := make([]string, 0, len(tunnels))
	for _, tunnel := range tunnels {
		out = append(out, tunnel.Name)
	}
	return out
}
