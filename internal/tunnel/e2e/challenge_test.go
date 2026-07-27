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

// startVhostForChallenges brings up a server with an ordinary client attached.
//
// The client is not what the tests are about, but its presence is: a real
// server has one, and waiting for its tunnel to publish is what makes the
// session count below a settled number rather than a race.
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

// announceChallenge publishes one the way certificate issuance does, and
// returns once the server has finished tearing that connection down — so what
// a test observes afterwards is the state that outlasts the publisher, not a
// race against its teardown.
func announceChallenge(t *testing.T, h *vhostHarness, domain, token, keyAuth string) {
	t.Helper()

	// The harness keeps a client connected of its own, and it is already up:
	// this waits for the one connection this function opens, not for an empty
	// server.
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

// A published challenge has to outlive the connection that published it.
//
// Certificate issuance runs in its own process, with no control connection of
// its own, so it opens a short one, publishes, and hangs up. The server
// withdrew everything a client had published when its session closed — right
// for a router that dropped off, wrong here, where hanging up is the ordinary
// end of a successful publish. The challenge was deleted milliseconds after
// being stored and the authority fetched a 404.
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

// Answering is not one-shot: the authority validates from several vantage
// points, and every one of them fetches the same URL.
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

// A router that goes away still takes its challenges with it. The client here
// publishes and disconnects like any other, so the two cases are told apart by
// whether anything was published at all, not by how the connection ended —
// this is the behaviour the fix must not have thrown away.
func TestUnpublishedTokenIsStillA404(t *testing.T) {
	h := startVhostForChallenges(t)

	status, _ := h.get(t, "nobody.example",
		"/.well-known/acme-challenge/never-published-this-one")
	if status != 404 {
		t.Errorf("got %d for a token nobody published, want 404", status)
	}
}
