package dns

import (
	"context"
	"fmt"
	"strings"
	"time"
)

type Solver struct {
	provider Provider

	zone string

	created map[string]string
}

func NewSolver(provider Provider, zone string) *Solver {
	return &Solver{
		provider: provider,
		zone:     strings.TrimSuffix(strings.ToLower(zone), "."),
		created:  map[string]string{},
	}
}

const challengePrefix = "_acme-challenge."

func challengeKey(fqdn string) string {
	fqdn = strings.TrimSuffix(strings.ToLower(fqdn), ".")

	fqdn = strings.TrimPrefix(fqdn, "*.")

	if strings.HasPrefix(fqdn, challengePrefix) {
		return fqdn
	}
	return challengePrefix + fqdn
}

func (s *Solver) Present(ctx context.Context, fqdn, value string) error {
	name := NormaliseName(challengeKey(fqdn), s.zone)

	record := Record{
		Name:  name,
		Type:  TypeTXT,
		Value: value,

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

func (s *Solver) CleanUp(ctx context.Context, fqdn, value string) error {
	token := challengeToken(fqdn, value)

	id, known := s.created[token]
	if !known {

		return nil
	}
	delete(s.created, token)

	if err := s.provider.DeleteRecord(ctx, s.zone, id); err != nil {
		return fmt.Errorf("dns: remove challenge record for %s: %w", fqdn, err)
	}
	return nil
}

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

func (s *Solver) Zone() string { return s.zone }

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
