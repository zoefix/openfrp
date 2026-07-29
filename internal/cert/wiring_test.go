package cert_test

import (
	"testing"

	"github.com/zoefix/openfrp/internal/cert"
	"github.com/zoefix/openfrp/internal/dns"
)

func TestDNSSolverSatisfiesChallengeSolver(t *testing.T) {
	var _ cert.ChallengeSolver = dns.NewSolver(nil, "example.com")
}
