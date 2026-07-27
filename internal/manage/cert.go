package manage

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/zoefix/openfrp/internal/cert"
	"github.com/zoefix/openfrp/internal/dns"
	"github.com/zoefix/openfrp/internal/storage/repo"
)

// OrderView is a certificate order as the UI sees it. Private key material is
// never included — the browser has no use for it, and a page that carries it
// is a page that can leak it.
type OrderView struct {
	ID      int64    `json:"id"`
	Domains []string `json:"domains"`
	KeyType string   `json:"key_type"`
	CA      string   `json:"ca"`
	CALabel string   `json:"ca_label"`
	Email   string   `json:"email"`

	AccountID   int64  `json:"account_id"`
	AccountName string `json:"account_name"`

	State     string `json:"state"`
	LastError string `json:"last_error,omitempty"`
	AutoRenew bool   `json:"auto_renew"`

	IssuedAt  int64 `json:"issued_at,omitempty"`
	ExpiresAt int64 `json:"expires_at,omitempty"`
	// DaysRemaining is negative once expired, which the UI colours differently
	// from "expiring soon".
	DaysRemaining int    `json:"days_remaining"`
	Issuer        string `json:"issuer,omitempty"`
	SerialNumber  string `json:"serial_number,omitempty"`
}

// CAs lists the supported certificate authorities.
func (s *Service) CAs() []cert.CA { return cert.CAs() }

// KeyTypes lists the supported certificate key algorithms.
func (s *Service) KeyTypes() []map[string]string {
	out := make([]map[string]string, 0, 4)
	for _, keyType := range cert.KeyTypes() {
		out = append(out, map[string]string{
			"value": string(keyType),
			"label": keyType.Label(),
		})
	}
	return out
}

// ListOrders returns every order, decorated for display.
func (s *Service) ListOrders(ctx context.Context) ([]OrderView, error) {
	orders, err := s.orders.List(ctx)
	if err != nil {
		return nil, err
	}

	// One lookup for the account names rather than one query per order.
	names := map[int64]string{}
	if accounts, err := s.accounts.List(ctx); err == nil {
		for _, account := range accounts {
			names[account.ID] = account.Name
		}
	}

	now := time.Now()
	out := make([]OrderView, 0, len(orders))
	for _, order := range orders {
		out = append(out, s.view(order, names, now))
	}
	return out, nil
}

func (s *Service) view(order repo.Order, names map[int64]string, now time.Time) OrderView {
	view := OrderView{
		ID: order.ID, Domains: order.Domains, KeyType: order.KeyType,
		CA: order.CA, CALabel: order.CA, Email: order.Email,
		AccountID: order.AccountID, AccountName: names[order.AccountID],
		State: order.State, LastError: order.LastError,
		IssuedAt: order.IssuedAt, ExpiresAt: order.ExpiresAt,
	}

	if order.AutoRenew != nil {
		view.AutoRenew = *order.AutoRenew
	}
	if ca, known := cert.LookupCA(order.CA); known {
		view.CALabel = ca.Label
	}

	if order.ExpiresAt > 0 {
		view.DaysRemaining = int(time.Unix(order.ExpiresAt, 0).Sub(now).Hours() / 24)
	}

	// Issuer and serial come from the certificate itself rather than from
	// anything recorded at request time, so they describe what is actually
	// installed.
	if len(order.Certificate) > 0 {
		material := &cert.Certificate{FullchainPEM: order.Certificate}
		if err := material.Populate(); err == nil {
			view.Issuer = material.Issuer
			view.SerialNumber = material.SerialNumber
		}
	}

	return view
}

// OrderInput creates a certificate order.
type OrderInput struct {
	Domains   []string `json:"domains"`
	KeyType   string   `json:"key_type"`
	CA        string   `json:"ca"`
	Email     string   `json:"email"`
	AccountID int64    `json:"account_id"`
	AutoRenew *bool    `json:"auto_renew,omitempty"`
}

// CreateOrder validates and stores an order. It does not issue anything;
// issuance is a long operation that runs as a job.
func (s *Service) CreateOrder(ctx context.Context, in OrderInput) (OrderView, error) {
	domains := cert.NormaliseDomains(in.Domains)
	if len(domains) == 0 {
		return OrderView{}, fmt.Errorf("manage: the order needs at least one domain")
	}
	if in.Email == "" {
		return OrderView{}, fmt.Errorf("manage: the CA requires a contact email address")
	}

	keyType := cert.KeyType(in.KeyType)
	if in.KeyType == "" {
		keyType = cert.KeyType("ec256")
	}
	if !keyType.Valid() {
		return OrderView{}, fmt.Errorf("manage: unsupported key type %q", in.KeyType)
	}

	ca := in.CA
	if ca == "" {
		ca = "letsencrypt"
	}
	if _, known := cert.LookupCA(ca); !known {
		return OrderView{}, fmt.Errorf("manage: unknown certificate authority %q", ca)
	}

	// Every order needs a DNS account, not just a wildcard one.
	//
	// A wildcard can only be proved over DNS-01, and DNS-01 is the only
	// challenge implemented here — HTTP-01 would need port 80 on this router
	// reachable from the internet, which is the thing the tunnel exists to
	// avoid relying on. Accepting an order without one produced an order that
	// looked fine and could never issue.
	if in.AccountID == 0 {
		if cert.NeedsDNSChallenge(domains) {
			return OrderView{}, fmt.Errorf(
				"manage: a wildcard certificate needs a DNS account, because only " +
					"the DNS-01 challenge can prove a wildcard")
		}
		return OrderView{}, fmt.Errorf(
			"manage: this certificate needs a DNS account: DNS-01 is the only " +
				"challenge available here, because HTTP validation would need " +
				"port 80 on this router reachable from the internet")
	}
	if _, err := s.accounts.Get(ctx, in.AccountID); err != nil {
		return OrderView{}, err
	}

	created, err := s.orders.Create(ctx, repo.Order{
		Domains: domains, KeyType: string(keyType), CA: ca,
		Email: in.Email, AccountID: in.AccountID, AutoRenew: in.AutoRenew,
	})
	if err != nil {
		return OrderView{}, err
	}

	_ = s.orders.AppendEvent(ctx, created.ID, "requested", "")
	return s.view(created, map[int64]string{}, time.Now()), nil
}

// DeleteOrder removes an order and its history.
func (s *Service) DeleteOrder(ctx context.Context, id int64) error {
	return s.orders.Delete(ctx, id)
}

// OrderEvents returns an order's history.
func (s *Service) OrderEvents(ctx context.Context, id int64, limit int) ([]repo.Event, error) {
	return s.orders.Events(ctx, id, limit)
}

// Material returns the issued certificate chain for an order.
//
// The private key is deliberately not returned here. Exporting it is a
// separate, explicit operation rather than something a list view can do by
// accident.
func (s *Service) Material(ctx context.Context, id int64) ([]byte, error) {
	order, err := s.orders.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	if len(order.Certificate) == 0 {
		return nil, fmt.Errorf("manage: order %d has not been issued", id)
	}
	return order.Certificate, nil
}

// Issue obtains or renews the certificate for an order.
//
// progress receives human-readable milestones, which the job worker writes
// straight to its log so the UI can follow along. Issuance takes tens of
// seconds at best — DNS propagation dominates — so this never runs inside an
// rpcd call.
func (s *Service) Issue(ctx context.Context, id int64, progress func(string)) error {
	if progress == nil {
		progress = func(string) {}
	}

	order, err := s.orders.Get(ctx, id)
	if err != nil {
		return err
	}

	if err := s.orders.SetState(ctx, id, repo.StateIssuing, ""); err != nil {
		return err
	}

	certificate, err := s.issue(ctx, order, progress)
	if err != nil {
		// Record the reason where the UI can show it. The state change must
		// happen even though we are returning an error, or the order stays
		// "issuing" forever and the next attempt looks like it is already
		// running.
		_ = s.orders.SetState(ctx, id, repo.StateFailed, err.Error())
		_ = s.orders.AppendEvent(ctx, id, "failed", err.Error())
		return err
	}

	if err := s.orders.StoreCertificate(ctx, id,
		certificate.FullchainPEM, certificate.PrivateKeyPEM,
		certificate.IssuedAt.Unix(), certificate.NotAfter.Unix()); err != nil {
		return err
	}

	kind := "issued"
	if order.IssuedAt > 0 {
		kind = "renewed"
	}
	_ = s.orders.AppendEvent(ctx, id, kind,
		fmt.Sprintf("valid until %s", certificate.NotAfter.Format(time.RFC3339)))

	progress(fmt.Sprintf("certificate valid until %s",
		certificate.NotAfter.Format("2006-01-02")))
	return nil
}

// issue runs the ACME exchange for one order.
func (s *Service) issue(ctx context.Context, order repo.Order,
	progress func(string)) (*cert.Certificate, error) {

	ca, known := cert.LookupCA(order.CA)
	if !known {
		return nil, fmt.Errorf("manage: unknown certificate authority %q", order.CA)
	}

	account, err := s.acmeAccount(ctx, ca.Key, order.Email)
	if err != nil {
		return nil, err
	}
	if ca.RequiresEAB && account.EABKeyID == "" {
		return nil, fmt.Errorf(
			"manage: %s will not issue without external account binding credentials",
			ca.Label)
	}

	request := cert.IssueRequest{
		Domains: order.Domains,
		// The key, not the directory URL: the issuer resolves it and needs the
		// key to find the authority's metadata.
		CA:      ca.Key,
		KeyType: cert.KeyType(order.KeyType),
		Account: account,
	}

	// DNS-01 is required for a wildcard and is the only challenge configured
	// here; HTTP-01 would need port 80 on this router to be reachable from the
	// internet, which is the thing the tunnel exists to avoid relying on.
	if order.AccountID == 0 {
		// Says what to do rather than why. The account may have been deleted,
		// or the order may predate this being required; neither is
		// distinguishable here, and claiming one of them would be a guess
		// dressed up as a diagnosis.
		return nil, fmt.Errorf(
			"manage: order %d has no DNS account. Delete it and request the "+
				"certificate again with one selected", order.ID)
	}

	provider, err := s.provider(ctx, order.AccountID)
	if err != nil {
		return nil, err
	}

	zone, err := dns.RegistrableZone(ctx, provider, order.Domains[0])
	if err != nil {
		return nil, fmt.Errorf("manage: find the zone hosting %s: %w", order.Domains[0], err)
	}
	progress(fmt.Sprintf("using DNS zone %s", zone))

	request.Solver = dns.NewSolver(provider, zone)

	progress(fmt.Sprintf("requesting %v from %s", order.Domains, ca.Label))
	issuer := cert.NewIssuer(slog.Default())

	certificate, err := issuer.Issue(ctx, request)
	if err != nil {
		return nil, err
	}

	// The account registration may have been created during this exchange, so
	// persist it before returning or the next issuance registers again.
	if err := s.saveACMEAccount(ctx, ca.Key, account); err != nil {
		return nil, err
	}

	return certificate, nil
}

// acmeAccount loads the stored ACME account, or prepares a fresh one.
func (s *Service) acmeAccount(ctx context.Context, ca, email string) (*cert.Account, error) {
	accounts := repo.NewACMEAccounts(s.db.DB)

	stored, err := accounts.Find(ctx, ca, email)
	if err != nil {
		// No account under this address yet. lego will register one and fill
		// in the key, but the binding credentials are the operator's and
		// already on file if they have used this authority before — carry
		// them over rather than refusing to issue.
		fresh := &cert.Account{Email: email}
		if keyID, hmac, err := accounts.FindEAB(ctx, ca); err == nil {
			fresh.EABKeyID, fresh.EABHMAC = keyID, hmac
		}
		return fresh, nil
	}

	// An account row can predate the binding, so fall back for it too.
	if stored.EABKeyID == "" {
		if keyID, hmac, err := accounts.FindEAB(ctx, ca); err == nil {
			stored.EABKeyID, stored.EABHMAC = keyID, hmac
		}
	}

	account := &cert.Account{
		Email:      stored.Email,
		PrivateKey: stored.PrivateKey,
		EABKeyID:   stored.EABKeyID,
		EABHMAC:    stored.EABHMAC,
	}
	if len(stored.Registration) > 0 {
		account.Registration = stored.Registration
	}
	return account, nil
}

func (s *Service) saveACMEAccount(ctx context.Context, ca string, account *cert.Account) error {
	accounts := repo.NewACMEAccounts(s.db.DB)

	registration := account.Registration
	if len(registration) == 0 && account.Registration != nil {
		encoded, err := json.Marshal(account.Registration)
		if err != nil {
			return err
		}
		registration = encoded
	}

	_, err := accounts.Save(ctx, repo.ACMEAccount{
		CA: ca, Email: account.Email,
		PrivateKey: account.PrivateKey, Registration: registration,
		EABKeyID: account.EABKeyID, EABHMAC: account.EABHMAC,
	})
	return err
}

// EABState reports whether an order's authority needs external account
// binding, and whether credentials for it are already stored.
//
// The UI needs both answers before it can decide what to ask for: prompting
// for EAB on a CA that does not use it is noise, and not prompting on one that
// does leaves the operator staring at a refusal with nothing to act on.
type EABState struct {
	Required  bool   `json:"required"`
	Present   bool   `json:"present"`
	CA        string `json:"ca"`
	CALabel   string `json:"ca_label"`
	Email     string `json:"email"`
	HowToGet  string `json:"how_to_get,omitempty"`
	AccountID int64  `json:"-"`
}

// EABStatus describes what an order needs before it can be issued.
func (s *Service) EABStatus(ctx context.Context, orderID int64) (EABState, error) {
	order, err := s.orders.Get(ctx, orderID)
	if err != nil {
		return EABState{}, err
	}
	return s.EABStatusFor(ctx, order.CA, order.Email)
}

// EABStatusFor answers the same question for an authority the operator is
// still choosing, before any order exists.
//
// This is what lets the request form say "already saved, leave blank" instead
// of presenting empty boxes and implying the credentials were never entered —
// which is how it read before, and sent people back to the CA's dashboard for
// a pair they already had.
func (s *Service) EABStatusFor(ctx context.Context, caKey, email string) (EABState, error) {
	ca, known := cert.LookupCA(caKey)
	if !known {
		return EABState{}, fmt.Errorf("manage: unknown certificate authority %q", caKey)
	}

	state := EABState{
		Required: ca.RequiresEAB,
		CA:       ca.Key,
		CALabel:  ca.Label,
		Email:    email,
	}
	if !ca.RequiresEAB {
		return state, nil
	}

	state.HowToGet = eabSource(ca.Key)

	// Any binding for this authority counts, not just one filed under this
	// email. See repo.FindEAB.
	if keyID, _, err := repo.NewACMEAccounts(s.db.DB).FindEAB(ctx, ca.Key); err == nil && keyID != "" {
		state.Present = true
	}
	return state, nil
}

// eabSource says where to obtain the credentials, which is the part an
// operator cannot guess from the refusal alone.
func eabSource(ca string) string {
	switch ca {
	case "zerossl":
		return "https://app.zerossl.com/developer"
	case "google":
		return "https://console.cloud.google.com/security/publicca"
	default:
		return ""
	}
}

// SetEAB stores external account binding credentials for a CA that needs them.
func (s *Service) SetEAB(ctx context.Context, ca, email, keyID, hmac string) error {
	accounts := repo.NewACMEAccounts(s.db.DB)

	existing, err := accounts.Find(ctx, ca, email)
	if err != nil {
		existing = repo.ACMEAccount{CA: ca, Email: email}
	}
	existing.EABKeyID = keyID
	existing.EABHMAC = hmac

	_, err = accounts.Save(ctx, existing)
	return err
}
