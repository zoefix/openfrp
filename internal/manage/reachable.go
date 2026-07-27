package manage

import (
	"context"
	"fmt"
	"net"
	"sort"
	"strings"
	"time"
)

// publicResolvers are asked where a name points.
//
// The system resolver is deliberately not used. A router often runs a
// transparent proxy that answers with a placeholder address from 198.18.0.0/15
// so it can intercept the connection — this project's own test router does —
// and that answer says nothing about where the certificate authority will go.
// What matters here is exactly what public DNS says, because that is what the
// authority resolves.
var publicResolvers = []string{
	"1.1.1.1:53",
	"8.8.8.8:53",
}

// resolveTimeout bounds the whole pre-flight. It is a courtesy check, not a
// gate: a slow resolver must not hold up an issuance that would have worked.
const resolveTimeout = 5 * time.Second

// checkReachable reports why an HTTP validation would fail, before one is
// attempted.
//
// The authority fetches the challenge over HTTP at whatever address the name
// resolves to. If that is not one of this router's servers, the validation
// fails — and a failed validation is not free: authorities rate-limit them, so
// a misconfiguration can lock out the retry that would have worked.
//
// Nothing here blocks on doubt. A name that cannot be resolved at all, or a
// server whose address cannot be determined, produces no error: the check
// exists to catch the case that is definitely wrong, not to second-guess every
// case it cannot confirm.
func (h *httpSolver) checkReachable(ctx context.Context, domains []string) (string, error) {
	expected := h.serverAddresses(ctx)
	if len(expected) == 0 {
		return "could not determine the tunnel server's address; attempting anyway", nil
	}

	ctx, cancel := context.WithTimeout(ctx, resolveTimeout)
	defer cancel()

	var (
		problems  []string
		confirmed []string
		unknown   []string
	)

	for _, domain := range domains {
		addresses := h.lookup(ctx, domain)

		switch {
		case len(addresses) == 0:
			// Not resolvable from here. It may still be resolvable from the
			// authority, and refusing on that basis would be a guess.
			unknown = append(unknown, domain)

		case intersects(addresses, expected):
			confirmed = append(confirmed, domain)

		default:
			problems = append(problems, fmt.Sprintf(
				"%s resolves to %s", domain, strings.Join(addresses, ", ")))
		}
	}

	if len(problems) > 0 {
		return "", fmt.Errorf(
			"manage: %s, but the certificate is validated by fetching a file from "+
				"a tunnel server (%s). Point the record at the server, or pick a "+
				"DNS account and prove the domain that way instead",
			strings.Join(problems, "; "), strings.Join(sortedKeys(expected), ", "))
	}

	// Say which of the two happened. Reporting "the names resolve to a tunnel
	// server" when the lookup simply failed is a claim that was never checked,
	// and it turns a diagnosable misconfiguration into a mystery.
	if len(unknown) > 0 {
		return fmt.Sprintf(
			"could not look up %s from here, so whether it points at the server "+
				"is unverified; the authority will decide",
			strings.Join(unknown, ", ")), nil
	}

	return fmt.Sprintf("%s points at the tunnel server",
		strings.Join(confirmed, ", ")), nil
}

// serverAddresses is where the configured servers actually are.
func (h *httpSolver) serverAddresses(ctx context.Context) map[string]bool {
	out := map[string]bool{}

	for _, server := range h.servers {
		if ip := net.ParseIP(server.Addr); ip != nil {
			out[ip.String()] = true
			continue
		}
		// A server named rather than numbered: ask public DNS for the same
		// reason as above.
		for _, address := range h.lookup(ctx, server.Addr) {
			out[address] = true
		}
	}
	return out
}

// lookup resolves a name, through the injected resolver if there is one.
//
// Injectable so the check can be tested without depending on the network, and
// without a test that cannot see 1.1.1.1 quietly passing because the check
// declines to block on doubt.
func (h *httpSolver) lookup(ctx context.Context, name string) []string {
	if h.resolve != nil {
		return h.resolve(ctx, name)
	}
	return resolvePublic(ctx, name)
}

// resolvePublic asks the public resolvers for a name's addresses.
func resolvePublic(ctx context.Context, name string) []string {
	seen := map[string]bool{}

	for _, server := range publicResolvers {
		resolver := &net.Resolver{
			PreferGo: true,
			Dial: func(ctx context.Context, network, _ string) (net.Conn, error) {
				dialer := net.Dialer{Timeout: 2 * time.Second}
				return dialer.DialContext(ctx, network, server)
			},
		}

		addresses, err := resolver.LookupHost(ctx, name)
		if err != nil {
			continue
		}
		for _, address := range addresses {
			if ip := net.ParseIP(address); ip != nil {
				seen[ip.String()] = true
			}
		}
		if len(seen) > 0 {
			// One resolver answering is enough; they are asked in turn only so
			// that one being unreachable does not skip the check entirely.
			break
		}
	}

	return sortedKeys(seen)
}

func intersects(addresses []string, expected map[string]bool) bool {
	for _, address := range addresses {
		if expected[address] {
			return true
		}
	}
	return false
}

func sortedKeys(set map[string]bool) []string {
	out := make([]string, 0, len(set))
	for key := range set {
		out = append(out, key)
	}
	sort.Strings(out)
	return out
}
