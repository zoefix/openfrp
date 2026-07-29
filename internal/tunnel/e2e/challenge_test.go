package e2e

import (
	"context"
	"net"
	"testing"
	"time"

	"github.com/zoefix/openfrp/internal/config"
	"github.com/zoefix/openfrp/internal/tunnel/client"
	"github.com/zoefix/openfrp/internal/tunnel/protocol"
)

func startVhostForChallenges(t *testing.T) *vhostHarness {
	t.Helper()

	host, port := startHTTPService(t, "backend")
	return startVhost(t, []config.Tunnel{{
		Name:      "site",
		Enabled:   true,
		Type:      config.TunnelHTTP,
		LocalIP:   host,
		LocalPort: port,
		Domains:   []string{"site.example"},
	}})
}

func announceChallenge(t *testing.T, h *vhostHarness, domain, token, keyAuth string) {
	t.Helper()

	before := h.server.Registry().Len()

	upstream := config.Upstream{
		Name:  "test",
		Addr:  "127.0.0.1",
		Port:  h.server.Addr().(*net.TCPAddr).Port,
		Token: testToken,
	}

	reply, err := client.Announce(context.Background(), upstream, "test",
		&protocol.HTTPChallenge{Domain: domain, Token: token, KeyAuth: keyAuth},
		protocol.TypeHTTPChallengeResp)
	if err != nil {
		t.Fatalf("announce: %v", err)
	}
	if resp, ok := reply.(*protocol.HTTPChallengeResp); ok && resp.Error != "" {
		t.Fatalf("server refused the challenge: %s", resp.Error)
	}

	deadline := time.Now().Add(5 * time.Second)
	for h.server.Registry().Len() > before {
		if time.Now().After(deadline) {
			t.Fatalf("the announcing session was still open 5s after it hung up")
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestPublishedChallengeOutlivesTheConnectionThatPublishedIt(t *testing.T) {
	h := startVhostForChallenges(t)

	const (
		domain  = "cert.example"
		token   = "kK4kY9Yl3tG8ZaQmS2xHbN7pR1vC0dUw"
		keyAuth = token + ".thumbprint"
	)

	announceChallenge(t, h, domain, token, keyAuth)

	status, body := h.get(t, domain, "/.well-known/acme-challenge/"+token)
	if status != 200 {
		t.Fatalf("the authority got %d, want 200 — the challenge did not survive "+
			"the publishing connection", status)
	}
	if body != keyAuth {
		t.Errorf("answered %q, want %q", body, keyAuth)
	}
}

func TestChallengeAnswersEveryValidationAttempt(t *testing.T) {
	h := startVhostForChallenges(t)

	const (
		domain  = "multi.example"
		token   = "Zx8Qw2Er4Ty6Ui0Op1As3Df5Gh7Jk9Lm"
		keyAuth = token + ".thumbprint"
	)

	announceChallenge(t, h, domain, token, keyAuth)

	for attempt := 1; attempt <= 4; attempt++ {
		status, body := h.get(t, domain, "/.well-known/acme-challenge/"+token)
		if status != 200 || body != keyAuth {
			t.Fatalf("validation %d got %d %q, want 200 %q",
				attempt, status, body, keyAuth)
		}
	}
}

func TestUnpublishedTokenIsStillA404(t *testing.T) {
	h := startVhostForChallenges(t)

	status, _ := h.get(t, "nobody.example",
		"/.well-known/acme-challenge/never-published-this-one")
	if status != 404 {
		t.Errorf("got %d for a token nobody published, want 404", status)
	}
}
