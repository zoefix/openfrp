package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestACloudflareServerLoadsWithItsZone(t *testing.T) {
	const rendered = `{
		"servers": [
			{"name": "cf", "kind": "cloudflare",
			 "tunnel_id": "abc", "zone": "example.com"}
		],
		"tunnels": [
			{"name": "web", "enabled": true, "type": "http",
			 "local_ip": "192.168.1.2", "local_port": 80,
			 "server": "cf", "domains": ["nas.example.com"]}
		]
	}`

	dir := t.TempDir()
	path := filepath.Join(dir, "openfrp.json")
	if err := os.WriteFile(path, []byte(rendered), 0o600); err != nil {
		t.Fatal(err)
	}

	cfg, err := LoadClient(path)
	if err != nil {
		t.Fatalf("a rendered Cloudflare server was rejected: %v", err)
	}

	server, ok := cfg.Upstream("cf")
	if !ok {
		t.Fatal("the server was not found")
	}
	if !server.IsCloudflare() {
		t.Error("the server did not read back as a Cloudflare one")
	}
	if server.Zone != "example.com" {
		t.Errorf("zone read as %q", server.Zone)
	}
}
