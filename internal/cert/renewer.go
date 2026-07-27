package cert

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"
)

// Renewal defaults.
const (
	// DefaultRenewBefore is how many days ahead of expiry to renew.
	//
	// Let's Encrypt issues for 90 days and recommends renewing at 30, which
	// leaves a fortnight of daily retries before anything breaks. Renewing at
	// 7 — as dnsmgr does — works until the first outage that lasts a week.
	DefaultRenewBefore = 30

	// DefaultCheckInterval is how often to look for work.
	DefaultCheckInterval = 12 * time.Hour

	// retryBackoff bounds how soon a failed renewal is retried, so a
	// persistent failure cannot hammer a CA into rate-limiting the account.
	retryBackoff = 6 * time.Hour
)

// Store persists certificates. Declared here, on the consuming side, so the
// renewer depends only on what it uses.
type Store interface {
	// List returns every managed certificate.
	List(ctx context.Context) ([]Managed, error)
	// Save records a renewed certificate.
	Save(ctx context.Context, managed Managed) error
}

// Installer delivers a certificate to wherever it is needed.
type Installer interface {
	Install(ctx context.Context, managed Managed) error
}

// Managed is a certificate under renewal management.
type Managed struct {
	// ID identifies this managed certificate.
	ID string `json:"id"`
	// Request is what to ask for when renewing.
	Domains []string `json:"domains"`
	CA      string   `json:"ca"`
	KeyType KeyType  `json:"key_type"`
	// DNSAccount names the credential set used for the challenge.
	DNSAccount string `json:"dns_account,omitempty"`

	// Certificate is the material currently held. Nil before first issuance.
	Certificate *Certificate `json:"certificate,omitempty"`

	// AutoRenew is false for a certificate the operator manages by hand.
	AutoRenew bool `json:"auto_renew"`
	// RenewBefore overrides the default threshold in days.
	RenewBefore int `json:"renew_before,omitempty"`

	// LastAttempt and LastError record the most recent try, so a persistent
	// failure is visible in the UI rather than only in a log.
	LastAttempt time.Time `json:"last_attempt,omitempty"`
	LastError   string    `json:"last_error,omitempty"`
}

// threshold returns the renewal threshold in days.
func (m Managed) threshold() int {
	if m.RenewBefore > 0 {
		return m.RenewBefore
	}
	return DefaultRenewBefore
}

// Due reports whether this certificate should be renewed now.
func (m Managed) Due(now time.Time) bool {
	if !m.AutoRenew {
		return false
	}
	if m.Certificate == nil {
		return true
	}
	// Back off after a failure so a broken configuration does not retry every
	// cycle and burn the CA's rate limit for the account.
	if !m.LastAttempt.IsZero() && m.LastError != "" {
		if now.Sub(m.LastAttempt) < retryBackoff {
			return false
		}
	}
	return m.Certificate.NeedsRenewal(now, m.threshold())
}

// SolverFactory builds a challenge solver for one managed certificate.
//
// A function rather than a stored solver because credentials are looked up per
// certificate, and because a solver holds per-issuance state that must not be
// shared between orders.
type SolverFactory func(ctx context.Context, managed Managed) (ChallengeSolver, error)

// Renewer keeps managed certificates current.
type Renewer struct {
	store     Store
	issuer    *Issuer
	solvers   SolverFactory
	installer Installer
	logger    *slog.Logger

	// now is injectable for tests.
	now func() time.Time

	mu      sync.Mutex
	running bool
}

// NewRenewer returns a renewer.
func NewRenewer(store Store, issuer *Issuer, solvers SolverFactory,
	installer Installer, logger *slog.Logger) *Renewer {

	if logger == nil {
		logger = slog.Default()
	}
	return &Renewer{
		store:     store,
		issuer:    issuer,
		solvers:   solvers,
		installer: installer,
		logger:    logger,
		now:       time.Now,
	}
}

// Name implements scheduler.Job.
func (r *Renewer) Name() string { return "certificate-renewal" }

// Interval implements scheduler.Job.
func (r *Renewer) Interval() time.Duration { return DefaultCheckInterval }

// Run renews everything that is due.
//
// One certificate failing does not stop the others: a single bad DNS
// credential should not hold up every other renewal on the router.
func (r *Renewer) Run(ctx context.Context) error {
	// Guard against a slow cycle overlapping the next tick. The scheduler
	// already resets from the end of a run, but the renewer is also callable
	// directly from the UI.
	r.mu.Lock()
	if r.running {
		r.mu.Unlock()
		r.logger.Debug("renewal already in progress, skipping this cycle")
		return nil
	}
	r.running = true
	r.mu.Unlock()

	defer func() {
		r.mu.Lock()
		r.running = false
		r.mu.Unlock()
	}()

	managed, err := r.store.List(ctx)
	if err != nil {
		return fmt.Errorf("cert: list managed certificates: %w", err)
	}

	now := r.now()
	var due []Managed
	for _, item := range managed {
		if item.Due(now) {
			due = append(due, item)
		}
	}

	if len(due) == 0 {
		r.logger.Debug("no certificates are due for renewal", "managed", len(managed))
		return nil
	}

	r.logger.Info("renewing certificates", "due", len(due), "managed", len(managed))

	var failures int
	for _, item := range due {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if err := r.renewOne(ctx, item); err != nil {
			failures++
			r.logger.Error("renewal failed",
				"id", item.ID, "domains", item.Domains, "error", err)
		}
	}

	if failures > 0 {
		return fmt.Errorf("cert: %d of %d renewals failed", failures, len(due))
	}
	return nil
}

// RenewNow renews one certificate immediately, whatever its schedule says.
func (r *Renewer) RenewNow(ctx context.Context, managed Managed) error {
	return r.renewOne(ctx, managed)
}

func (r *Renewer) renewOne(ctx context.Context, managed Managed) error {
	managed.LastAttempt = r.now()

	// Record the outcome either way, so a persistent failure is visible in the
	// UI rather than only in a log the operator has to go looking for.
	save := func(err error) error {
		if err != nil {
			managed.LastError = err.Error()
		} else {
			managed.LastError = ""
		}
		if saveErr := r.store.Save(ctx, managed); saveErr != nil {
			r.logger.Error("could not record renewal outcome",
				"id", managed.ID, "error", saveErr)
		}
		return err
	}

	var solver ChallengeSolver
	if r.solvers != nil {
		built, err := r.solvers(ctx, managed)
		if err != nil {
			return save(fmt.Errorf("build challenge solver: %w", err))
		}
		solver = built
	}

	if NeedsDNSChallenge(managed.Domains) && solver == nil {
		return save(fmt.Errorf(
			"a wildcard requires DNS-01, but no DNS account is configured for this certificate"))
	}

	issued, err := r.issuer.Issue(ctx, IssueRequest{
		Domains: managed.Domains,
		CA:      managed.CA,
		KeyType: managed.KeyType,
		Account: &Account{},
		Solver:  solver,
	})
	if err != nil {
		return save(err)
	}

	managed.Certificate = issued

	if r.installer != nil {
		if err := r.installer.Install(ctx, managed); err != nil {
			// The certificate is valid; only delivery failed. Keep it, so the
			// next cycle retries installation rather than re-issuing and
			// spending another CA order.
			return save(fmt.Errorf("install renewed certificate: %w", err))
		}
	}

	r.logger.Info("certificate renewed",
		"id", managed.ID,
		"domains", managed.Domains,
		"expires", issued.NotAfter.Format(time.RFC3339),
		"days", issued.DaysRemaining(r.now()))

	return save(nil)
}
