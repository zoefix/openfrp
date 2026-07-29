package cert

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"
)

const (
	DefaultRenewBefore = 30

	DefaultCheckInterval = 12 * time.Hour

	retryBackoff = 6 * time.Hour
)

type Store interface {
	List(ctx context.Context) ([]Managed, error)

	Save(ctx context.Context, managed Managed) error
}

type Installer interface {
	Install(ctx context.Context, managed Managed) error
}

type Managed struct {
	ID string `json:"id"`

	Domains []string `json:"domains"`
	CA      string   `json:"ca"`
	KeyType KeyType  `json:"key_type"`

	DNSAccount string `json:"dns_account,omitempty"`

	Certificate *Certificate `json:"certificate,omitempty"`

	AutoRenew bool `json:"auto_renew"`

	RenewBefore int `json:"renew_before,omitempty"`

	LastAttempt time.Time `json:"last_attempt,omitempty"`
	LastError   string    `json:"last_error,omitempty"`
}

func (m Managed) threshold() int {
	if m.RenewBefore > 0 {
		return m.RenewBefore
	}
	return DefaultRenewBefore
}

func (m Managed) Due(now time.Time) bool {
	if !m.AutoRenew {
		return false
	}
	if m.Certificate == nil {
		return true
	}

	if !m.LastAttempt.IsZero() && m.LastError != "" {
		if now.Sub(m.LastAttempt) < retryBackoff {
			return false
		}
	}
	return m.Certificate.NeedsRenewal(now, m.threshold())
}

type SolverFactory func(ctx context.Context, managed Managed) (ChallengeSolver, error)

type Renewer struct {
	store     Store
	issuer    *Issuer
	solvers   SolverFactory
	installer Installer
	logger    *slog.Logger

	now func() time.Time

	mu      sync.Mutex
	running bool
}

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

func (r *Renewer) Name() string { return "certificate-renewal" }

func (r *Renewer) Interval() time.Duration { return DefaultCheckInterval }

func (r *Renewer) Run(ctx context.Context) error {

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

func (r *Renewer) RenewNow(ctx context.Context, managed Managed) error {
	return r.renewOne(ctx, managed)
}

func (r *Renewer) renewOne(ctx context.Context, managed Managed) error {
	managed.LastAttempt = r.now()

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
