package cert

import (
	"context"

	"github.com/go-acme/lego/v4/challenge"
	"github.com/go-acme/lego/v4/challenge/http01"
)

type HTTPSolver interface {
	Present(ctx context.Context, domain, token, keyAuth string) error
	CleanUp(ctx context.Context, domain, token, keyAuth string) error
}

type legoHTTPSolver struct {
	ctx   context.Context
	inner HTTPSolver
}

func (s legoHTTPSolver) Present(domain, token, keyAuth string) error {
	return s.inner.Present(s.ctx, domain, token, keyAuth)
}

func (s legoHTTPSolver) CleanUp(domain, token, keyAuth string) error {
	return s.inner.CleanUp(s.ctx, domain, token, keyAuth)
}

var (
	_ challenge.Provider = legoHTTPSolver{}

	_ = http01.ChallengePath
)

func HTTPChallengePath(token string) string {
	return http01.ChallengePath(token)
}
