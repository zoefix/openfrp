package cert_test

import (
	"testing"

	"github.com/zoefix/openfrp/internal/cert"
	"github.com/zoefix/openfrp/internal/dns"
)

// TestDNSSolverSatisfiesChallengeSolver pins the seam between the two domain
// packages.
//
// This was broken for the entire life of both packages and nothing noticed,
// because nothing ever assigned one to the other: cert declared its solver
// interface in lego's three-argument shape, with a comment asserting that the
// DNS solver satisfied it, while the DNS solver took a context. Both packages
// compiled, both had passing tests, and DNS-01 could never have run.
//
// A compile-time assertion is the cheapest possible guard against an interface
// that only claims to match.
func TestDNSSolverSatisfiesChallengeSolver(t *testing.T) {
	var _ cert.ChallengeSolver = dns.NewSolver(nil, "example.com")
}
