package manage

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"

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

	// resolve overrides how names are looked up. Nil uses public DNS; a test
	// supplies its own so the pre-flight can be exercised without a network.
	resolve func(ctx context.Context, name string) []string
}

// SetHTTPChallengeResolver overrides name resolution for the pre-flight check.
// Intended for tests.
func (s *Service) SetHTTPChallengeResolver(resolve func(context.Context, string) []string) {
	if s.httpSolver != nil {
		s.httpSolver.resolve = resolve
	}
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

// Present publishes the validation where the authority will look for it.
//
// The server that matters is the one the name resolves to, and only that one:
// publishing succeeded elsewhere while failing there is indistinguishable from
// working until the authority reports a 404 from an address the log never
// mentions. Every other server is a courtesy, so that a DNS change between now
// and validation still finds it.
func (h *httpSolver) Present(ctx context.Context, domain, token, keyAuth string) error {
	message := &protocol.HTTPChallenge{
		Domain: domain, Token: token, KeyAuth: keyAuth,
	}

	required := h.serversFor(ctx, domain)
	if len(required) > 0 {
		if err := h.send(ctx, required, message, "publish"); err != nil {
			return err
		}
		// The rest are best effort; the one that counts already has it.
		h.sendQuietly(ctx, h.others(required), message, "publish")
		return nil
	}

	// Where the name points could not be determined. Publish everywhere and
	// let the authority decide, rather than guessing at one.
	return h.send(ctx, h.servers, message, "publish")
}

// serversFor returns the configured servers a name actually resolves to.
func (h *httpSolver) serversFor(ctx context.Context, domain string) []config.Upstream {
	addresses := map[string]bool{}
	for _, address := range h.lookup(ctx, domain) {
		addresses[address] = true
	}
	if len(addresses) == 0 {
		return nil
	}

	var out []config.Upstream
	for _, server := range h.servers {
		for _, address := range h.addressesOf(ctx, server) {
			if addresses[address] {
				out = append(out, server)
				break
			}
		}
	}
	return out
}

// addressesOf is where one server is.
func (h *httpSolver) addressesOf(ctx context.Context, server config.Upstream) []string {
	if ip := net.ParseIP(server.Addr); ip != nil {
		return []string{ip.String()}
	}
	return h.lookup(ctx, server.Addr)
}

// others is every configured server except those given.
func (h *httpSolver) others(exclude []config.Upstream) []config.Upstream {
	excluded := map[string]bool{}
	for _, server := range exclude {
		excluded[server.Name] = true
	}

	var out []config.Upstream
	for _, server := range h.servers {
		if !excluded[server.Name] {
			out = append(out, server)
		}
	}
	return out
}

// CleanUp withdraws it again.
//
// A failure here is reported but does not fail the issuance: a challenge left
// behind expires on its own, while refusing a certificate that was already
// granted would be a self-inflicted outage.
func (h *httpSolver) CleanUp(ctx context.Context, domain, token, keyAuth string) error {
	if err := h.send(ctx, h.servers, &protocol.HTTPChallenge{
		Domain: domain, Token: token, Remove: true,
	}, "withdraw"); err != nil {
		h.logger.Warn("could not withdraw an ACME challenge",
			"domain", domain, "error", err)
	}
	return nil
}

// sendQuietly delivers where a failure does not matter.
func (h *httpSolver) sendQuietly(ctx context.Context, servers []config.Upstream,
	message *protocol.HTTPChallenge, what string) {

	if err := h.send(ctx, servers, message, what); err != nil {
		h.logger.Debug("secondary server did not take the challenge",
			"domain", message.Domain, "error", err)
	}
}

// send delivers one message to the given servers, failing if none accepts it.
func (h *httpSolver) send(ctx context.Context, servers []config.Upstream,
	message *protocol.HTTPChallenge, what string) error {

	if len(servers) == 0 {
		return nil
	}

	var failures []error

	for _, server := range servers {
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

	if len(failures) == len(servers) {
		return fmt.Errorf("manage: could not %s the challenge for %s on the "+
			"server that name points at: %w",
			what, message.Domain, errors.Join(failures...))
	}
	return nil
}
