package config

import (
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestExampleConfigsLoad(t *testing.T) {
	serverPath := filepath.Join("..", "..", "configs", "openfrps.example.json")
	srv, err := LoadServer(serverPath)
	if err != nil {
		t.Fatalf("load %s: %v", serverPath, err)
	}
	if srv.BindPort != 7000 {
		t.Errorf("BindPort = %d, want 7000", srv.BindPort)
	}

	clientPath := filepath.Join("..", "..", "configs", "openfrpc.example.json")
	cli, err := LoadClient(clientPath)
	if err != nil {
		t.Fatalf("load %s: %v", clientPath, err)
	}
	if cli.Transport.Mux {
		t.Error("the example must ship with mux off; it is the slow path")
	}
	if got := len(cli.EnabledTunnels()); got != 1 {
		t.Errorf("enabled tunnels = %d, want 1", got)
	}
	if cli.Transport.HeartbeatInterval.D() != 20*time.Second {
		t.Errorf("HeartbeatInterval = %s, want 20s", cli.Transport.HeartbeatInterval)
	}
}

func TestClientDefaultsFavourTheFastPath(t *testing.T) {
	cfg := &Client{ServerAddr: "example.test"}
	cfg.ApplyDefaults()

	if cfg.Transport.Mux {
		t.Error("Mux must default to false: multiplexing forfeits splice(2) " +
			"and puts every tunnel behind one congestion window")
	}
	if cfg.Transport.PoolCount != DefaultPoolCount {
		t.Errorf("PoolCount = %d, want %d", cfg.Transport.PoolCount, DefaultPoolCount)
	}
	if cfg.Transport.MuxStreamWindow != DefaultMuxStreamWindow {
		t.Errorf("MuxStreamWindow = %d, want %d — yamux's own 256KiB default "+
			"caps a stream at window/RTT", cfg.Transport.MuxStreamWindow, DefaultMuxStreamWindow)
	}
	if err := cfg.Validate(); err != nil {
		t.Errorf("a minimal config should validate: %v", err)
	}
}

func TestTunnelValidation(t *testing.T) {
	tests := []struct {
		name    string
		tunnel  Tunnel
		wantErr string
	}{
		{
			name:   "tcp with explicit port",
			tunnel: Tunnel{Name: "ssh", Type: TunnelTCP, LocalPort: 22, RemotePort: 6022},
		},
		{
			name:   "tcp with server-allocated port",
			tunnel: Tunnel{Name: "ssh", Type: TunnelTCP, LocalPort: 22, RemotePort: 0},
		},
		{
			name:    "unnamed",
			tunnel:  Tunnel{Type: TunnelTCP, LocalPort: 22},
			wantErr: "name must not be empty",
		},
		{
			name:    "unknown type",
			tunnel:  Tunnel{Name: "x", Type: "gopher", LocalPort: 70},
			wantErr: "unknown type",
		},
		{
			name:    "local port out of range",
			tunnel:  Tunnel{Name: "x", Type: TunnelTCP, LocalPort: 99999},
			wantErr: "local_port",
		},
		{
			name:    "http without domains",
			tunnel:  Tunnel{Name: "web", Type: TunnelHTTP, LocalPort: 80},
			wantErr: "requires at least one domain",
		},
		{
			name: "tls mode on a non-https tunnel",
			tunnel: Tunnel{Name: "web", Type: TunnelHTTP, LocalPort: 80,
				Domains: []string{"a.test"}, TLSMode: TLSTerminate},
			wantErr: "only applies to https",
		},
		{
			name:    "a type the server has no proxy for",
			tunnel:  Tunnel{Name: "priv", Type: "stcp", LocalPort: 22},
			wantErr: "unknown type",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			tunnel := tc.tunnel
			tunnel.applyDefaults()
			err := tunnel.Validate()

			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				return
			}
			if err == nil {
				t.Fatalf("expected an error containing %q", tc.wantErr)
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Errorf("error = %q, want it to mention %q", err, tc.wantErr)
			}
		})
	}
}

func TestClientRejectsDuplicateRemotePorts(t *testing.T) {
	cfg := &Client{
		ServerAddr: "example.test",
		Tunnels: []Tunnel{
			{Name: "a", Enabled: true, Type: TunnelTCP, LocalPort: 1, RemotePort: 6000},
			{Name: "b", Enabled: true, Type: TunnelTCP, LocalPort: 2, RemotePort: 6000},
		},
	}
	cfg.ApplyDefaults()

	err := cfg.Validate()
	if err == nil || !strings.Contains(err.Error(), "remote_port 6000") {
		t.Fatalf("err = %v, want a remote_port collision error", err)
	}

	cfg.Tunnels[0].RemotePort = 0
	cfg.Tunnels[1].RemotePort = 0
	if err := cfg.Validate(); err != nil {
		t.Errorf("two server-allocated ports should not collide: %v", err)
	}
}

func TestDurationAcceptsStringsAndSeconds(t *testing.T) {
	var d Duration

	if err := d.UnmarshalJSON([]byte(`"1m30s"`)); err != nil {
		t.Fatalf("string form: %v", err)
	}
	if d.D() != 90*time.Second {
		t.Errorf("got %s, want 1m30s", d)
	}

	if err := d.UnmarshalJSON([]byte(`45`)); err != nil {
		t.Fatalf("numeric form: %v", err)
	}
	if d.D() != 45*time.Second {
		t.Errorf("got %s, want 45s", d)
	}

	if err := d.UnmarshalJSON([]byte(`"not a duration"`)); err == nil {
		t.Error("expected an error for an unparseable duration")
	}
}

func TestServerRejectsPortCollisions(t *testing.T) {
	cfg := &Server{BindPort: 7000, VhostHTTPPort: 7000}
	cfg.ApplyDefaults()
	if err := cfg.Validate(); err == nil {
		t.Error("bind_port colliding with a vhost port should be rejected")
	}

	cfg = &Server{BindPort: 7000, VhostHTTPPort: 8080, VhostHTTPSPort: 8080}
	cfg.ApplyDefaults()
	if err := cfg.Validate(); err == nil {
		t.Error("identical vhost http and https ports should be rejected")
	}
}
