package manage

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"github.com/zoefix/openfrp/internal/config"
	"github.com/zoefix/openfrp/internal/tunnel/client"
	"github.com/zoefix/openfrp/internal/tunnel/protocol"
)

// httpSolver answers ACME HTTP-01 validations through the tunnel servers.
//
// The authority fetches http://<domain>/.well-known/acme-challenge/<token>.
// That name resolves to a tunnel server — it has to, because that is what the
// certificate is being obtained for — so the server answers it directly on its
// shared HTTP port. Nothing reaches the router, and the LAN service is not
// involved, so a name can be validated before it has a tunnel at all.
//
// This is the path that needs no DNS credentials. Requiring an API key for a
// whole zone in order to certify one name is a real cost, and this avoids it.
type httpSolver struct {
	servers []config.Upstream
	version string
	logger  *slog.Logger
}

// SetHTTPChallengeServers tells the service where to publish HTTP-01
// validations. Without them, only DNS-01 is available.
func (s *Service) SetHTTPChallengeServers(servers []config.Upstream, version string) {
	if len(servers) == 0 {
		s.httpSolver = nil
		return
	}
	s.httpSolver = &httpSolver{
		servers: servers,
		version: version,
		logger:  slog.Default(),
	}
}

// Present publishes the validation on every configured server.
//
// Every one, not just the likely one: which server the authority reaches is
// decided by DNS, which this router does not control in the HTTP-01 case —
// that is the whole point of using it. Publishing everywhere costs a few bytes
// and removes the guess.
func (h *httpSolver) Present(ctx context.Context, domain, token, keyAuth string) error {
	return h.broadcast(ctx, &protocol.HTTPChallenge{
		Domain: domain, Token: token, KeyAuth: keyAuth,
	}, "publish")
}

// CleanUp withdraws it again.
//
// A failure here is reported but does not fail the issuance: a challenge left
// behind expires on its own, while refusing a certificate that was already
// granted would be a self-inflicted outage.
func (h *httpSolver) CleanUp(ctx context.Context, domain, token, keyAuth string) error {
	if err := h.broadcast(ctx, &protocol.HTTPChallenge{
		Domain: domain, Token: token, Remove: true,
	}, "withdraw"); err != nil {
		h.logger.Warn("could not withdraw an ACME challenge",
			"domain", domain, "error", err)
	}
	return nil
}

// broadcast sends one message to every server, succeeding if any accepts it.
func (h *httpSolver) broadcast(ctx context.Context,
	message *protocol.HTTPChallenge, what string) error {

	var failures []error

	for _, server := range h.servers {
		reply, err := client.Announce(ctx, server, h.version,
			message, protocol.TypeHTTPChallengeResp)
		if err != nil {
			failures = append(failures,
				fmt.Errorf("%s: %w", server.Name, err))
			continue
		}

		if resp, ok := reply.(*protocol.HTTPChallengeResp); ok && resp.Error != "" {
			failures = append(failures,
				fmt.Errorf("%s: %s", server.Name, resp.Error))
			continue
		}

		h.logger.Info("published an ACME challenge",
			"server", server.Name, "domain", message.Domain, "action", what)

		// One server holding it is enough: the authority reaches exactly one.
		// Carrying on lets the others hold it too, which is harmless and means
		// a DNS change between now and validation does not matter.
	}

	if len(failures) == len(h.servers) {
		return fmt.Errorf("manage: no server could %s the challenge for %s: %w",
			what, message.Domain, errors.Join(failures...))
	}
	return nil
}
