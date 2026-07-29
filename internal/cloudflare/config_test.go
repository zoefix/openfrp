package cloudflare

import (
	"strings"
	"testing"

	"github.com/zoefix/openfrp/internal/config"
)

func httpTunnel(name string, port int, domains ...string) config.Tunnel {
	return config.Tunnel{
		Name: name, Enabled: true, Type: config.TunnelHTTP,
		LocalIP: "192.168.1.2", LocalPort: port, Domains: domains,
	}
}

func TestExactHostnamesAreMatchedBeforeAWildcardOverThem(t *testing.T) {
	rules, _ := RulesFor([]config.Tunnel{
		httpTunnel("catchall", 80, "*.example.com"),
		httpTunnel("shop", 8080, "shop.example.com"),
	})

	if len(rules) != 2 {
		t.Fatalf("got %d rules, want 2", len(rules))
	}
	if rules[0].Hostname != "shop.example.com" {
		t.Errorf("first rule is %q; the wildcard would take every request",
			rules[0].Hostname)
	}
	if !strings.HasSuffix(rules[0].Service, ":8080") {
		t.Errorf("shop routes to %q", rules[0].Service)
	}
}

func TestADeeperWildcardComesFirst(t *testing.T) {
	rules, _ := RulesFor([]config.Tunnel{
		httpTunnel("broad", 80, "*.example.com"),
		httpTunnel("deep", 81, "*.lab.example.com"),
	})

	if rules[0].Hostname != "*.lab.example.com" {
		t.Errorf("first rule is %q, want the deeper wildcard", rules[0].Hostname)
	}
}

func TestTunnelsCloudflareCannotPublishAreReported(t *testing.T) {
	rules, skipped := RulesFor([]config.Tunnel{
		httpTunnel("web", 80, "web.example.com"),
		{Name: "ssh", Enabled: true, Type: config.TunnelTCP,
			LocalIP: "192.168.1.3", LocalPort: 22, RemotePort: 2222},
		{Name: "game", Enabled: true, Type: config.TunnelUDP,
			LocalIP: "192.168.1.4", LocalPort: 27015},
		httpTunnel("nameless", 80),
		{Name: "off", Enabled: false, Type: config.TunnelHTTP,
			LocalIP: "192.168.1.5", LocalPort: 80, Domains: []string{"off.example.com"}},
	})

	if len(rules) != 1 {
		t.Fatalf("got %d rules, want only the HTTP one with a domain", len(rules))
	}

	joined := strings.Join(skipped, " ")
	for _, want := range []string{"ssh", "game", "nameless"} {
		if !strings.Contains(joined, want) {
			t.Errorf("%q was dropped without being reported: %v", want, skipped)
		}
	}

	if strings.Contains(joined, "off") {
		t.Errorf("a disabled tunnel was reported as skipped: %v", skipped)
	}
}

func TestEveryDomainOfATunnelGetsARule(t *testing.T) {
	rules, _ := RulesFor([]config.Tunnel{
		httpTunnel("web", 80, "a.example.com", "b.example.com"),
	})

	if len(rules) != 2 {
		t.Fatalf("got %d rules, want one per domain", len(rules))
	}
	if rules[0].Service != rules[1].Service {
		t.Error("the two names of one tunnel point at different services")
	}
}

func TestTheConfigEndsWithACatchAll(t *testing.T) {
	rules, _ := RulesFor([]config.Tunnel{httpTunnel("web", 80, "web.example.com")})
	rendered := RenderConfig("tid", "/etc/openfrp/cf.json", rules)

	lines := strings.Split(strings.TrimSpace(rendered), "\n")
	if last := lines[len(lines)-1]; !strings.Contains(last, "http_status:404") {
		t.Errorf("the configuration ends with %q, which cloudflared will reject", last)
	}
	if !strings.Contains(rendered, `tunnel: "tid"`) {
		t.Error("the configuration does not name the tunnel")
	}
	if !strings.Contains(rendered, `credentials-file: "/etc/openfrp/cf.json"`) {
		t.Error("the configuration does not point at the credentials")
	}
}

func TestScalarsAreQuoted(t *testing.T) {
	rendered := RenderConfig("tid", "/etc/openfrp/cf.json", []Rule{
		{Hostname: `odd".example.com`, Service: "http://127.0.0.1:80"},
	})

	if !strings.Contains(rendered, `hostname: "odd\".example.com"`) {
		t.Errorf("a quote in a hostname was not escaped:\n%s", rendered)
	}
}

func TestTheServiceHopIsPlainHTTP(t *testing.T) {
	rules, _ := RulesFor([]config.Tunnel{{
		Name: "secure", Enabled: true, Type: config.TunnelHTTPS,
		LocalIP: "192.168.1.9", LocalPort: 443, Domains: []string{"s.example.com"},
	}})

	if len(rules) != 1 {
		t.Fatalf("got %d rules", len(rules))
	}
	if rules[0].Service != "http://192.168.1.9:443" {
		t.Errorf("service is %q, want plain http", rules[0].Service)
	}
}
