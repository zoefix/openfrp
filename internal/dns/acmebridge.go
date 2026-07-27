package dns

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// Solver adapts a Provider to what an ACME DNS-01 challenge needs.
//
// The provider interface is deliberately wider than this — an operator managing
// tunnel records wants carrier lines, remarks and history that ACME has no use
// for. Rather than write DNS twice, the management interface is the real one
// and this is a thin adapter over it, so a provider added for certificates
// immediately works in the records UI and vice versa.
type Solver struct {
	provider Provider
	// zone is the registrable domain the records belong to.
	zone string

	// created remembers what we added so cleanup can be exact. Deleting by
	// name and value instead would race a concurrent issuance for a sibling
	// name, since both write to the same _acme-challenge record set.
	created map[string]string
}

// NewSolver adapts a provider for challenge records in one zone.
func NewSolver(provider Provider, zone string) *Solver {
	return &Solver{
		provider: provider,
		zone:     strings.TrimSuffix(strings.ToLower(zone), "."),
		created:  map[string]string{},
	}
}

// challengeKey is the fully qualified name a DNS-01 challenge answers on.
func challengeKey(fqdn string) string {
	fqdn = strings.TrimSuffix(strings.ToLower(fqdn), ".")
	// A wildcard is validated against the base name: the challenge for
	// *.example.com lives at _acme-challenge.example.com, exactly where the
	// challenge for example.com does. That collision is why cleanup tracks
	// record IDs rather than matching on name.
	fqdn = strings.TrimPrefix(fqdn, "*.")
	return "_acme-challenge." + fqdn
}

// Present publishes the challenge TXT record.
func (s *Solver) Present(ctx context.Context, fqdn, value string) error {
	name := NormaliseName(challengeKey(fqdn), s.zone)

	record := Record{
		Name:  name,
		Type:  TypeTXT,
		Value: value,
		// The shortest TTL the provider will take: the record lives for
		// seconds and a long TTL only delays the retry after a failure.
		TTL:     minChallengeTTL(s.provider.Capabilities()),
		Line:    LineDefault,
		Remark:  "openfrp ACME challenge",
		Enabled: true,
	}

	id, err := s.provider.AddRecord(ctx, s.zone, record)
	if err != nil {
		return fmt.Errorf("dns: publish challenge for %s: %w", fqdn, err)
	}

	s.created[challengeToken(fqdn, value)] = id
	return nil
}

// CleanUp removes the challenge record.
//
// Failure here is reported but should not fail an issuance that otherwise
// succeeded: a stale TXT record is untidy, while refusing a valid certificate
// over it is an outage.
func (s *Solver) CleanUp(ctx context.Context, fqdn, value string) error {
	token := challengeToken(fqdn, value)

	id, known := s.created[token]
	if !known {
		// Nothing recorded — most likely Present failed, so there is nothing
		// to remove.
		return nil
	}
	delete(s.created, token)

	if err := s.provider.DeleteRecord(ctx, s.zone, id); err != nil {
		return fmt.Errorf("dns: remove challenge record for %s: %w", fqdn, err)
	}
	return nil
}

// Timeout reports how long to wait for propagation, and how often to poll.
//
// These are generous. A DNS-01 challenge that is checked too eagerly fails and
// consumes a CA rate-limit slot, which is far more expensive than waiting.
func (s *Solver) Timeout() (timeout, interval time.Duration) {
	return 10 * time.Minute, 15 * time.Second
}

func challengeToken(fqdn, value string) string {
	return strings.ToLower(strings.TrimSuffix(fqdn, ".")) + "|" + value
}

func minChallengeTTL(caps Capabilities) int {
	if caps.MinTTL > 0 {
		return caps.MinTTL
	}
	return 600
}

// Zone reports the zone this solver writes to.
func (s *Solver) Zone() string { return s.zone }

// RegistrableZone finds which of the account's zones a name belongs to.
//
// A certificate for a.b.example.com has to have its challenge written into the
// example.com zone, and only the provider knows which zones exist. The longest
// matching suffix wins, so a delegated sub-zone is preferred over its parent.
func RegistrableZone(ctx context.Context, provider Provider, fqdn string) (string, error) {
	name := strings.TrimSuffix(strings.ToLower(strings.TrimPrefix(fqdn, "*.")), ".")

	domains, err := provider.ListDomains(ctx, ListOptions{PageSize: 500})
	if err != nil {
		return "", fmt.Errorf("dns: list zones: %w", err)
	}

	best := ""
	for _, domain := range domains {
		zone := strings.TrimSuffix(strings.ToLower(domain.Name), ".")
		if name == zone || strings.HasSuffix(name, "."+zone) {
			if len(zone) > len(best) {
				best = zone
			}
		}
	}

	if best == "" {
		return "", fmt.Errorf("dns: no zone in this account covers %s", fqdn)
	}
	return best, nil
}
