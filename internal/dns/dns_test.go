package dns

import (
	"context"
	"fmt"
	"strings"
	"testing"
)

// fakeProvider records what a caller did, so the adapter above it can be
// tested without touching a real API.
type fakeProvider struct {
	domains []Domain
	records map[string]Record
	nextID  int

	addErr    error
	deleteErr error
	deleted   []string
}

func newFakeProvider(zones ...string) *fakeProvider {
	p := &fakeProvider{records: map[string]Record{}}
	for _, zone := range zones {
		p.domains = append(p.domains, Domain{Name: zone})
	}
	return p
}

func (p *fakeProvider) Check(context.Context) error { return nil }

func (p *fakeProvider) Capabilities() Capabilities {
	return Capabilities{MinTTL: 120, Lines: []Line{LineDefault}}
}

func (p *fakeProvider) ListDomains(context.Context, ListOptions) ([]Domain, error) {
	return p.domains, nil
}

func (p *fakeProvider) ListRecords(context.Context, string, ListOptions) ([]Record, error) {
	out := make([]Record, 0, len(p.records))
	for _, record := range p.records {
		out = append(out, record)
	}
	return out, nil
}

func (p *fakeProvider) AddRecord(_ context.Context, _ string, record Record) (string, error) {
	if p.addErr != nil {
		return "", p.addErr
	}
	p.nextID++
	id := fmt.Sprintf("rec-%d", p.nextID)
	record.ID = id
	p.records[id] = record
	return id, nil
}

func (p *fakeProvider) UpdateRecord(_ context.Context, _ string, record Record) error {
	p.records[record.ID] = record
	return nil
}

func (p *fakeProvider) DeleteRecord(_ context.Context, _ string, id string) error {
	if p.deleteErr != nil {
		return p.deleteErr
	}
	p.deleted = append(p.deleted, id)
	delete(p.records, id)
	return nil
}

func TestSolverPublishesAndRemovesChallenge(t *testing.T) {
	provider := newFakeProvider("example.com")
	solver := NewSolver(provider, "example.com")
	ctx := context.Background()

	if err := solver.Present(ctx, "www.example.com", "token-value"); err != nil {
		t.Fatalf("Present: %v", err)
	}

	if len(provider.records) != 1 {
		t.Fatalf("expected one record, got %d", len(provider.records))
	}
	for _, record := range provider.records {
		if record.Type != TypeTXT {
			t.Errorf("record type = %s, want TXT", record.Type)
		}
		if record.Name != "_acme-challenge.www" {
			t.Errorf("record name = %q, want _acme-challenge.www", record.Name)
		}
		if record.Value != "token-value" {
			t.Errorf("record value = %q", record.Value)
		}
		if record.TTL != 120 {
			t.Errorf("TTL = %d, want the provider's minimum of 120", record.TTL)
		}
	}

	if err := solver.CleanUp(ctx, "www.example.com", "token-value"); err != nil {
		t.Fatalf("CleanUp: %v", err)
	}
	if len(provider.records) != 0 {
		t.Errorf("record survived cleanup: %v", provider.records)
	}
}

// TestSolverWildcardSharesTheBaseChallengeName is the case that makes
// tracking record IDs necessary rather than matching on name: a certificate
// covering both example.com and *.example.com produces two challenges at the
// same DNS name, and cleaning up by name would remove the wrong one.
func TestSolverWildcardSharesTheBaseChallengeName(t *testing.T) {
	provider := newFakeProvider("example.com")
	solver := NewSolver(provider, "example.com")
	ctx := context.Background()

	if err := solver.Present(ctx, "example.com", "value-base"); err != nil {
		t.Fatalf("Present base: %v", err)
	}
	if err := solver.Present(ctx, "*.example.com", "value-wildcard"); err != nil {
		t.Fatalf("Present wildcard: %v", err)
	}

	if len(provider.records) != 2 {
		t.Fatalf("expected two records at the same name, got %d", len(provider.records))
	}
	for _, record := range provider.records {
		if record.Name != "_acme-challenge" {
			t.Errorf("record name = %q, want _acme-challenge for the apex", record.Name)
		}
	}

	// Removing one must leave the other alone.
	if err := solver.CleanUp(ctx, "*.example.com", "value-wildcard"); err != nil {
		t.Fatalf("CleanUp wildcard: %v", err)
	}
	if len(provider.records) != 1 {
		t.Fatalf("cleanup removed %d records, want exactly one", 2-len(provider.records))
	}
	for _, record := range provider.records {
		if record.Value != "value-base" {
			t.Errorf("the wrong record survived: %q", record.Value)
		}
	}
}

func TestSolverCleanUpIsSafeWithoutPresent(t *testing.T) {
	provider := newFakeProvider("example.com")
	solver := NewSolver(provider, "example.com")

	// Present failed, so there is nothing to remove and nothing to complain
	// about — an issuance must not be failed by tidy-up of a record that was
	// never created.
	if err := solver.CleanUp(context.Background(), "www.example.com", "v"); err != nil {
		t.Errorf("CleanUp without Present: %v", err)
	}
	if len(provider.deleted) != 0 {
		t.Error("CleanUp deleted something it never created")
	}
}

func TestRegistrableZonePrefersTheLongestMatch(t *testing.T) {
	provider := newFakeProvider("example.com", "sub.example.com", "other.org")
	ctx := context.Background()

	tests := map[string]string{
		"www.example.com":      "example.com",
		"a.b.example.com":      "example.com",
		"host.sub.example.com": "sub.example.com",
		"sub.example.com":      "sub.example.com",
		"*.sub.example.com":    "sub.example.com",
		"deep.host.other.org":  "other.org",
	}

	for name, want := range tests {
		got, err := RegistrableZone(ctx, provider, name)
		if err != nil {
			t.Errorf("RegistrableZone(%q): %v", name, err)
			continue
		}
		if got != want {
			t.Errorf("RegistrableZone(%q) = %q, want %q", name, got, want)
		}
	}

	if _, err := RegistrableZone(ctx, provider, "nothing.example.net"); err == nil {
		t.Error("a name outside every zone should be an error")
	}
}

func TestLineMappingsAreConsistent(t *testing.T) {
	for _, key := range KnownProviderTables() {
		lines := SupportedLines(key)
		if len(lines) == 0 {
			t.Errorf("provider %q reports no lines at all", key)
		}

		// Every provider must offer a default; a record with no carrier split
		// has to be expressible everywhere.
		if _, ok := ProviderLine(key, LineDefault); !ok {
			t.Errorf("provider %q has no default line", key)
		}

		// The round trip has to be stable, or a record read back would change
		// which carrier it claims to serve.
		for _, line := range lines {
			value, ok := ProviderLine(key, line)
			if !ok {
				t.Errorf("provider %q claims line %s but has no mapping", key, line)
				continue
			}
			if got := LineFromProvider(key, value); got != line {
				t.Errorf("provider %q: %s maps to %q which maps back to %s",
					key, line, value, got)
			}
		}
	}
}

func TestUnsupportedLineIsRefusedNotSubstituted(t *testing.T) {
	// Cloudflare has no carrier concept. Asking for a telecom-only record must
	// fail rather than quietly become a default record served to everyone,
	// which is the opposite of what the operator asked for.
	if _, ok := ProviderLine("cloudflare", LineTelecom); ok {
		t.Error("cloudflare should not claim to support a telecom line")
	}
}

func TestNormaliseName(t *testing.T) {
	tests := []struct{ fqdn, zone, want string }{
		{"www.example.com", "example.com", "www"},
		{"example.com", "example.com", "@"},
		{"example.com.", "example.com", "@"},
		{"WWW.EXAMPLE.COM", "example.com", "www"},
		{"a.b.example.com", "example.com", "a.b"},
		{"_acme-challenge.example.com", "example.com", "_acme-challenge"},
	}

	for _, tc := range tests {
		if got := NormaliseName(tc.fqdn, tc.zone); got != tc.want {
			t.Errorf("NormaliseName(%q, %q) = %q, want %q", tc.fqdn, tc.zone, got, tc.want)
		}
	}
}

func TestRecordValidation(t *testing.T) {
	if err := (Record{Type: TypeA, Value: "1.2.3.4"}).Validate(); err != nil {
		t.Errorf("a valid record was rejected: %v", err)
	}
	if err := (Record{Value: "1.2.3.4"}).Validate(); err == nil {
		t.Error("a record without a type should be rejected")
	}
	if err := (Record{Type: TypeA, Value: "  "}).Validate(); err == nil {
		t.Error("a record with a blank value should be rejected")
	}
	if err := (Record{Type: TypeA, Value: "x", TTL: -1}).Validate(); err == nil {
		t.Error("a negative TTL should be rejected")
	}
}

func TestRecordFQDN(t *testing.T) {
	tests := []struct{ name, zone, want string }{
		{"www", "example.com", "www.example.com"},
		{"@", "example.com", "example.com"},
		{"", "example.com", "example.com"},
		{"a.b", "example.com", "a.b.example.com"},
	}
	for _, tc := range tests {
		got := Record{Name: tc.name}.FQDN(tc.zone)
		if got != tc.want {
			t.Errorf("FQDN(%q, %q) = %q, want %q", tc.name, tc.zone, got, tc.want)
		}
	}
}

func TestChallengeKeyStripsWildcard(t *testing.T) {
	if got := challengeKey("*.example.com"); got != "_acme-challenge.example.com" {
		t.Errorf("challengeKey = %q", got)
	}
	if got := challengeKey("example.com"); got != "_acme-challenge.example.com" {
		t.Errorf("challengeKey = %q", got)
	}
	if !strings.HasPrefix(challengeKey("WWW.Example.COM."), "_acme-challenge.www") {
		t.Error("challengeKey should lowercase and strip the trailing dot")
	}
}
