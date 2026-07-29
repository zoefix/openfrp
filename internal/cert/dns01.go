package cert

import (
	"context"
	"time"

	"github.com/go-acme/lego/v4/challenge"
	"github.com/go-acme/lego/v4/challenge/dns01"
)

type ChallengeSolver interface {
	Present(ctx context.Context, fqdn, value string) error
	CleanUp(ctx context.Context, fqdn, value string) error
	Timeout() (timeout, interval time.Duration)
}

type legoSolver struct {
	ctx   context.Context
	inner ChallengeSolver
}

func (s legoSolver) Present(domain, _, keyAuth string) error {
	info := dns01.GetChallengeInfo(domain, keyAuth)
	return s.inner.Present(s.ctx, info.EffectiveFQDN, info.Value)
}

func (s legoSolver) CleanUp(domain, _, keyAuth string) error {
	info := dns01.GetChallengeInfo(domain, keyAuth)
	return s.inner.CleanUp(s.ctx, info.EffectiveFQDN, info.Value)
}

func (s legoSolver) Timeout() (timeout, interval time.Duration) {
	return s.inner.Timeout()
}

var (
	_ challenge.Provider        = legoSolver{}
	_ challenge.ProviderTimeout = legoSolver{}
)
