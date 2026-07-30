package manage

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/zoefix/openfrp/internal/cert"
	"github.com/zoefix/openfrp/internal/dns"
	"github.com/zoefix/openfrp/internal/storage/repo"
)

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

	DaysRemaining int    `json:"days_remaining"`
	Issuer        string `json:"issuer,omitempty"`
	SerialNumber  string `json:"serial_number,omitempty"`
}

func (s *Service) CAs() []cert.CA { return cert.CAs() }

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

func (s *Service) ListOrders(ctx context.Context) ([]OrderView, error) {
	orders, err := s.orders.List(ctx)
	if err != nil {
		return nil, err
	}

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

	if len(order.Certificate) > 0 {
		material := &cert.Certificate{FullchainPEM: order.Certificate}
		if err := material.Populate(); err == nil {
			view.Issuer = material.Issuer
			view.SerialNumber = material.SerialNumber
		}
	}

	return view
}

type OrderInput struct {
	Domains   []string `json:"domains"`
	KeyType   string   `json:"key_type"`
	CA        string   `json:"ca"`
	Email     string   `json:"email"`
	AccountID int64    `json:"account_id"`
	AutoRenew *bool    `json:"auto_renew,omitempty"`
}

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

	if cert.NeedsDNSChallenge(domains) && in.AccountID == 0 {
		return OrderView{}, fmt.Errorf(
			"manage: a wildcard certificate needs a DNS account, because only " +
				"the DNS-01 challenge can prove a wildcard")
	}
	if in.AccountID == 0 && s.httpSolver == nil {
		return OrderView{}, fmt.Errorf(
			"manage: without a DNS account this certificate is validated over " +
				"HTTP through a tunnel server, and none is configured")
	}
	if in.AccountID != 0 {
		if _, err := s.accounts.Get(ctx, in.AccountID); err != nil {
			return OrderView{}, err
		}
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

func (s *Service) DeleteOrder(ctx context.Context, id int64) error {
	return s.orders.Delete(ctx, id)
}

func (s *Service) OrderEvents(ctx context.Context, id int64, limit int) ([]repo.Event, error) {
	return s.orders.Events(ctx, id, limit)
}

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

		CA:      ca.Key,
		KeyType: cert.KeyType(order.KeyType),
		Account: account,
	}

	switch {
	case order.AccountID != 0:
		provider, err := s.provider(ctx, order.AccountID)
		if err != nil {
			return nil, err
		}

		zone, err := dns.RegistrableZone(ctx, provider, order.Domains[0])
		if err != nil {
			return nil, fmt.Errorf("manage: find the zone hosting %s: %w",
				order.Domains[0], err)
		}
		progress(fmt.Sprintf("proving ownership through DNS zone %s", zone))

		request.Solver = dns.NewSolver(provider, zone)

	case cert.NeedsDNSChallenge(order.Domains):
		return nil, fmt.Errorf(
			"manage: order %d covers a wildcard, which only DNS can prove. "+
				"Point it at a DNS account", order.ID)

	case s.httpSolver != nil:
		progress("proving ownership over HTTP, answered by the tunnel server")

		note, err := s.httpSolver.checkReachable(ctx, order.Domains)
		if err != nil {
			return nil, err
		}
		progress(note)

		request.HTTPSolver = s.httpSolver

	default:
		return nil, fmt.Errorf(
			"manage: order %d has no DNS account and no server to answer an "+
				"HTTP validation", order.ID)
	}

	progress(fmt.Sprintf("requesting %v from %s", order.Domains, ca.Label))
	issuer := cert.NewIssuer(slog.Default())

	certificate, err := issuer.Issue(ctx, request)
	if err != nil {
		return nil, err
	}

	if err := s.saveACMEAccount(ctx, ca.Key, account); err != nil {
		return nil, err
	}

	return certificate, nil
}

func (s *Service) acmeAccount(ctx context.Context, ca, email string) (*cert.Account, error) {
	accounts := repo.NewACMEAccounts(s.db.DB)

	stored, err := accounts.Find(ctx, ca, email)
	if err != nil {

		fresh := &cert.Account{Email: email}
		if keyID, hmac, err := accounts.FindEAB(ctx, ca); err == nil {
			fresh.EABKeyID, fresh.EABHMAC = keyID, hmac
		}
		return fresh, nil
	}

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
	if err := account.LoadRegistration(stored.Registration); err != nil {
		return nil, err
	}
	return account, nil
}

func (s *Service) saveACMEAccount(ctx context.Context, ca string, account *cert.Account) error {
	accounts := repo.NewACMEAccounts(s.db.DB)

	registration, err := account.MarshalRegistration()
	if err != nil {
		return err
	}

	_, err = accounts.Save(ctx, repo.ACMEAccount{
		CA: ca, Email: account.Email,
		PrivateKey: account.PrivateKey, Registration: registration,
		EABKeyID: account.EABKeyID, EABHMAC: account.EABHMAC,
	})
	return err
}

type EABState struct {
	Required  bool   `json:"required"`
	Present   bool   `json:"present"`
	CA        string `json:"ca"`
	CALabel   string `json:"ca_label"`
	Email     string `json:"email"`
	HowToGet  string `json:"how_to_get,omitempty"`
	AccountID int64  `json:"-"`
}

func (s *Service) EABStatus(ctx context.Context, orderID int64) (EABState, error) {
	order, err := s.orders.Get(ctx, orderID)
	if err != nil {
		return EABState{}, err
	}
	return s.EABStatusFor(ctx, order.CA, order.Email)
}

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

	if keyID, _, err := repo.NewACMEAccounts(s.db.DB).FindEAB(ctx, ca.Key); err == nil && keyID != "" {
		state.Present = true
	}
	return state, nil
}

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
