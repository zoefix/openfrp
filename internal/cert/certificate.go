package cert

import (
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"sort"
	"strings"
	"time"
)

type Certificate struct {
	Domains []string `json:"domains"`

	FullchainPEM []byte `json:"fullchain_pem"`

	PrivateKeyPEM []byte `json:"private_key_pem"`

	IssuedAt time.Time `json:"issued_at"`
	NotAfter time.Time `json:"not_after"`

	Issuer string `json:"issuer,omitempty"`

	SerialNumber string `json:"serial_number,omitempty"`
}

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

func (c *Certificate) DaysRemaining(now time.Time) int {
	return int(c.NotAfter.Sub(now).Hours() / 24)
}

func (c *Certificate) NeedsRenewal(now time.Time, threshold int) bool {
	if c.NotAfter.IsZero() {
		return true
	}
	return c.DaysRemaining(now) <= threshold
}

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

func matchesName(pattern, host string) bool {
	if pattern == host {
		return true
	}
	if !strings.HasPrefix(pattern, "*.") {
		return false
	}

	suffix := pattern[1:]
	if !strings.HasSuffix(host, suffix) {
		return false
	}

	prefix := host[:len(host)-len(suffix)]
	return prefix != "" && !strings.Contains(prefix, ".")
}

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

func NeedsDNSChallenge(domains []string) bool {
	for _, domain := range domains {
		if strings.HasPrefix(domain, "*.") {
			return true
		}
	}
	return false
}
