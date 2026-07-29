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

type httpSolver struct {
	servers []config.Upstream
	version string
	logger  *slog.Logger

	resolve func(ctx context.Context, name string) []string
}

func (s *Service) SetHTTPChallengeResolver(resolve func(context.Context, string) []string) {
	if s.httpSolver != nil {
		s.httpSolver.resolve = resolve
	}
}

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

func (h *httpSolver) Present(ctx context.Context, domain, token, keyAuth string) error {
	message := &protocol.HTTPChallenge{
		Domain: domain, Token: token, KeyAuth: keyAuth,
	}

	required := h.serversFor(ctx, domain)
	if len(required) > 0 {
		if err := h.send(ctx, required, message, "publish"); err != nil {
			return err
		}

		h.sendQuietly(ctx, h.others(required), message, "publish")
		return nil
	}

	return h.send(ctx, h.servers, message, "publish")
}

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

func (h *httpSolver) addressesOf(ctx context.Context, server config.Upstream) []string {
	if ip := net.ParseIP(server.Addr); ip != nil {
		return []string{ip.String()}
	}
	return h.lookup(ctx, server.Addr)
}

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

func (h *httpSolver) CleanUp(ctx context.Context, domain, token, keyAuth string) error {
	if err := h.send(ctx, h.servers, &protocol.HTTPChallenge{
		Domain: domain, Token: token, Remove: true,
	}, "withdraw"); err != nil {
		h.logger.Warn("could not withdraw an ACME challenge",
			"domain", domain, "error", err)
	}
	return nil
}

func (h *httpSolver) sendQuietly(ctx context.Context, servers []config.Upstream,
	message *protocol.HTTPChallenge, what string) {

	if err := h.send(ctx, servers, message, what); err != nil {
		h.logger.Debug("secondary server did not take the challenge",
			"domain", message.Domain, "error", err)
	}
}

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

	}

	if len(failures) == len(servers) {
		return fmt.Errorf("manage: could not %s the challenge for %s on the "+
			"server that name points at: %w",
			what, message.Domain, errors.Join(failures...))
	}
	return nil
}
