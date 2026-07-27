package cert

import (
	"context"
	"time"

	"github.com/go-acme/lego/v4/challenge"
	"github.com/go-acme/lego/v4/challenge/dns01"
)

// ChallengeSolver publishes and removes DNS-01 challenge records.
//
// Declared here, on the consuming side, and deliberately not in lego's shape:
// it takes a context, and it speaks in fully qualified names and values rather
// than lego's (domain, token, keyAuth). That keeps internal/dns free of any
// ACME dependency, so the DNS providers remain usable for ordinary record
// management by code that has never heard of certificates.
type ChallengeSolver interface {
	Present(ctx context.Context, fqdn, value string) error
	CleanUp(ctx context.Context, fqdn, value string) error
	Timeout() (timeout, interval time.Duration)
}

// legoSolver adapts a ChallengeSolver to lego's challenge.Provider.
//
// Holding a context in a struct is normally wrong, and it is done here because
// lego's interface has no room for one: the calls originate inside
// Certificate.Obtain, which does not thread a context through to the solver.
// The alternative is a solver that cannot be cancelled at all, which matters
// because a DNS-01 exchange can sit waiting on propagation for minutes.
type legoSolver struct {
	ctx   context.Context
	inner ChallengeSolver
}

// Present implements challenge.Provider.
func (s legoSolver) Present(domain, _, keyAuth string) error {
	info := dns01.GetChallengeInfo(domain, keyAuth)
	return s.inner.Present(s.ctx, info.EffectiveFQDN, info.Value)
}

// CleanUp implements challenge.Provider.
func (s legoSolver) CleanUp(domain, _, keyAuth string) error {
	info := dns01.GetChallengeInfo(domain, keyAuth)
	return s.inner.CleanUp(s.ctx, info.EffectiveFQDN, info.Value)
}

// Timeout implements challenge.ProviderTimeout, which is how lego learns how
// long to wait for propagation instead of using its own default.
func (s legoSolver) Timeout() (timeout, interval time.Duration) {
	return s.inner.Timeout()
}

var (
	_ challenge.Provider        = legoSolver{}
	_ challenge.ProviderTimeout = legoSolver{}
)
