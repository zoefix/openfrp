package manage

import (
	"context"
	"fmt"
	"net"
	"sort"
	"strings"
	"time"
)

var publicResolvers = []string{
	"1.1.1.1:53",
	"8.8.8.8:53",
}

const resolveTimeout = 5 * time.Second

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

	if len(unknown) > 0 {
		return fmt.Sprintf(
			"could not look up %s from here, so whether it points at the server "+
				"is unverified; the authority will decide",
			strings.Join(unknown, ", ")), nil
	}

	return fmt.Sprintf("%s points at the tunnel server",
		strings.Join(confirmed, ", ")), nil
}

func (h *httpSolver) serverAddresses(ctx context.Context) map[string]bool {
	out := map[string]bool{}

	for _, server := range h.servers {
		if ip := net.ParseIP(server.Addr); ip != nil {
			out[ip.String()] = true
			continue
		}

		for _, address := range h.lookup(ctx, server.Addr) {
			out[address] = true
		}
	}
	return out
}

func (h *httpSolver) lookup(ctx context.Context, name string) []string {
	if h.resolve != nil {
		return h.resolve(ctx, name)
	}
	return resolvePublic(ctx, name)
}

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
