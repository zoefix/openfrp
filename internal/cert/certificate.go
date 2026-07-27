package cert

import (
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"sort"
	"strings"
	"time"
)

// Certificate is an issued certificate and its private key.
type Certificate struct {
	// Domains are the names this certificate covers, as requested.
	Domains []string `json:"domains"`
	// FullchainPEM is the leaf followed by any intermediates.
	FullchainPEM []byte `json:"fullchain_pem"`
	// PrivateKeyPEM is the matching key.
	PrivateKeyPEM []byte `json:"private_key_pem"`

	// IssuedAt and NotAfter come from the parsed leaf.
	IssuedAt time.Time `json:"issued_at"`
	NotAfter time.Time `json:"not_after"`
	// Issuer is the CA's common name, for display.
	Issuer string `json:"issuer,omitempty"`
	// SerialNumber identifies this certificate to the CA.
	SerialNumber string `json:"serial_number,omitempty"`
}

// Leaf parses the end-entity certificate out of the chain.
func (c *Certificate) Leaf() (*x509.Certificate, error) {
	block, _ := pem.Decode(c.FullchainPEM)
	if block == nil {
		return nil, fmt.Errorf("cert: no PEM block in the chain")
	}
	if block.Type != "CERTIFICATE" {
		return nil, fmt.Errorf("cert: first PEM block is %q, not a certificate", block.Type)
	}

	leaf, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("cert: parse leaf: %w", err)
	}
	return leaf, nil
}

// Populate fills the metadata fields from the parsed leaf.
func (c *Certificate) Populate() error {
	leaf, err := c.Leaf()
	if err != nil {
		return err
	}

	c.IssuedAt = leaf.NotBefore
	c.NotAfter = leaf.NotAfter
	c.Issuer = leaf.Issuer.CommonName
	c.SerialNumber = leaf.SerialNumber.String()

	if len(c.Domains) == 0 {
		c.Domains = append([]string(nil), leaf.DNSNames...)
	}
	return nil
}

// DaysRemaining reports how long the certificate stays valid. It goes negative
// once expired, which is what makes a single threshold comparison work for
// both "renew soon" and "already dead".
func (c *Certificate) DaysRemaining(now time.Time) int {
	return int(c.NotAfter.Sub(now).Hours() / 24)
}

// NeedsRenewal reports whether the certificate should be renewed.
func (c *Certificate) NeedsRenewal(now time.Time, threshold int) bool {
	if c.NotAfter.IsZero() {
		return true
	}
	return c.DaysRemaining(now) <= threshold
}

// Covers reports whether the certificate is valid for a hostname, applying the
// same single-label wildcard rule the router uses.
//
// This matters because the two must agree. A certificate that covers a name
// the router will not route to it, or the reverse, produces a browser error
// that looks like a routing bug and is not.
func (c *Certificate) Covers(host string) bool {
	leaf, err := c.Leaf()
	if err != nil {
		return false
	}

	host = strings.ToLower(strings.TrimSuffix(host, "."))
	for _, name := range leaf.DNSNames {
		if matchesName(strings.ToLower(name), host) {
			return true
		}
	}
	return false
}

// matchesName applies RFC 6125 wildcard rules: a leading "*" matches exactly
// one label, and nothing else may be wildcarded.
func matchesName(pattern, host string) bool {
	if pattern == host {
		return true
	}
	if !strings.HasPrefix(pattern, "*.") {
		return false
	}

	suffix := pattern[1:] // ".example.com"
	if !strings.HasSuffix(host, suffix) {
		return false
	}

	// The wildcard covers one label, so what remains before the suffix must
	// contain no dot of its own.
	prefix := host[:len(host)-len(suffix)]
	return prefix != "" && !strings.Contains(prefix, ".")
}

// NormaliseDomains lowercases, deduplicates and sorts a domain list.
//
// The order is normalised because it feeds a cache key: the same set of names
// requested in a different order must not issue a second certificate.
func NormaliseDomains(domains []string) []string {
	seen := make(map[string]struct{}, len(domains))
	out := make([]string, 0, len(domains))

	for _, domain := range domains {
		domain = strings.ToLower(strings.TrimSuffix(strings.TrimSpace(domain), "."))
		if domain == "" {
			continue
		}
		if _, dup := seen[domain]; dup {
			continue
		}
		seen[domain] = struct{}{}
		out = append(out, domain)
	}

	sort.Strings(out)
	return out
}

// NeedsDNSChallenge reports whether any name requires DNS-01.
//
// A wildcard cannot be validated any other way — HTTP-01 and TLS-ALPN-01
// cannot prove control of names that do not exist yet — so a certificate
// containing one forces the whole order onto DNS.
func NeedsDNSChallenge(domains []string) bool {
	for _, domain := range domains {
		if strings.HasPrefix(domain, "*.") {
			return true
		}
	}
	return false
}
