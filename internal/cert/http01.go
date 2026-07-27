package cert

import (
	"context"

	"github.com/go-acme/lego/v4/challenge"
	"github.com/go-acme/lego/v4/challenge/http01"
)

// HTTPSolver publishes an ACME HTTP-01 validation somewhere the authority can
// reach it.
//
// Declared here, on the consuming side, and in the authority's own terms
// rather than lego's: a domain, the token it will request, and the body to
// return. The implementation in this project sends those to the tunnel server,
// which answers on its shared HTTP port — the router has no public address of
// its own, and the name being validated already points at that server.
//
// This is the validation method that needs no DNS credentials, which matters
// because a certificate for a single name should not require handing this
// router an API key for the whole zone.
type HTTPSolver interface {
	Present(ctx context.Context, domain, token, keyAuth string) error
	CleanUp(ctx context.Context, domain, token, keyAuth string) error
}

// legoHTTPSolver adapts an HTTPSolver to lego's challenge.Provider.
//
// The context is bound here for the same reason as the DNS solver's: lego's
// interface has no room for one, and a validation that cannot be cancelled
// leaves a published challenge behind when an issuance is abandoned.
type legoHTTPSolver struct {
	ctx   context.Context
	inner HTTPSolver
}

// Present implements challenge.Provider.
func (s legoHTTPSolver) Present(domain, token, keyAuth string) error {
	return s.inner.Present(s.ctx, domain, token, keyAuth)
}

// CleanUp implements challenge.Provider.
func (s legoHTTPSolver) CleanUp(domain, token, keyAuth string) error {
	return s.inner.CleanUp(s.ctx, domain, token, keyAuth)
}

var (
	_ challenge.Provider = legoHTTPSolver{}
	// The path lego's own providers serve under, kept here so the server's
	// interception and this stay describing the same URL.
	_ = http01.ChallengePath
)

// HTTPChallengePath is where an authority fetches a validation.
//
// Exported so the server's interception can be checked against the same
// definition the ACME library uses, rather than a copy that could drift.
func HTTPChallengePath(token string) string {
	return http01.ChallengePath(token)
}
