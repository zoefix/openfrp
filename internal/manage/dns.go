package manage

import (
	"context"
	"fmt"

	"github.com/zoefix/openfrp/internal/dns"
	_ "github.com/zoefix/openfrp/internal/dns/providers"
	"github.com/zoefix/openfrp/internal/storage/repo"
	"github.com/zoefix/openfrp/pkg/schema"
)

type AccountView struct {
	ID       int64  `json:"id"`
	Name     string `json:"name"`
	Provider string `json:"provider"`

	ProviderLabel string `json:"provider_label"`

	Credentials map[string]string `json:"credentials"`

	SecretsSet []string `json:"secrets_set"`
	CreatedAt  int64    `json:"created_at"`
	UpdatedAt  int64    `json:"updated_at"`
}

func (s *Service) Providers() []dns.Descriptor {
	return dns.Descriptors()
}

func (s *Service) ListAccounts(ctx context.Context) ([]AccountView, error) {
	stored, err := s.accounts.List(ctx)
	if err != nil {
		return nil, err
	}

	out := make([]AccountView, 0, len(stored))
	for _, account := range stored {
		out = append(out, s.redact(account))
	}
	return out, nil
}

func (s *Service) redact(account repo.Account) AccountView {
	view := AccountView{
		ID:            account.ID,
		Name:          account.Name,
		Provider:      account.Provider,
		ProviderLabel: account.Provider,
		Credentials:   map[string]string{},
		SecretsSet:    []string{},
		CreatedAt:     account.CreatedAt,
		UpdatedAt:     account.UpdatedAt,
	}

	descriptor, known := dns.Describe(account.Provider)
	if !known {

		for name := range account.Credentials {
			view.Credentials[name] = ""
		}
		return view
	}

	view.ProviderLabel = descriptor.Label

	secret := map[string]bool{}
	for _, name := range descriptor.Form.SecretNames() {
		secret[name] = true
		if account.Credentials[name] != "" {
			view.SecretsSet = append(view.SecretsSet, name)
		}
	}
	for name, value := range account.Credentials {
		if !secret[name] {
			view.Credentials[name] = value
		}
	}
	return view
}

type AccountInput struct {
	ID          int64             `json:"id,omitempty"`
	Name        string            `json:"name"`
	Provider    string            `json:"provider"`
	Credentials map[string]string `json:"credentials"`
}

func (s *Service) CreateAccount(ctx context.Context, in AccountInput) (AccountView, error) {
	descriptor, known := dns.Describe(in.Provider)
	if !known {
		return AccountView{}, fmt.Errorf("manage: unknown DNS provider %q", in.Provider)
	}
	if in.Name == "" {
		return AccountView{}, fmt.Errorf("manage: the account needs a name")
	}

	values := descriptor.Form.ApplyDefaults(in.Credentials)
	if err := descriptor.Form.Validate(values); err != nil {
		return AccountView{}, err
	}

	created, err := s.accounts.Create(ctx, repo.Account{
		Name: in.Name, Provider: in.Provider, Credentials: values,
	})
	if err != nil {
		return AccountView{}, err
	}
	return s.redact(created), nil
}

func (s *Service) UpdateAccount(ctx context.Context, in AccountInput) (AccountView, error) {
	existing, err := s.accounts.Get(ctx, in.ID)
	if err != nil {
		return AccountView{}, err
	}

	provider := in.Provider
	if provider == "" {
		provider = existing.Provider
	}
	descriptor, known := dns.Describe(provider)
	if !known {
		return AccountView{}, fmt.Errorf("manage: unknown DNS provider %q", provider)
	}

	values := map[string]string{}
	for name, value := range in.Credentials {
		values[name] = value
	}

	if provider == existing.Provider {
		for _, name := range descriptor.Form.SecretNames() {

			if values[name] == "" || values[name] == schema.RedactedMarker {
				values[name] = existing.Credentials[name]
			}
		}
	}

	values = descriptor.Form.ApplyDefaults(values)
	if err := descriptor.Form.Validate(values); err != nil {
		return AccountView{}, err
	}

	name := in.Name
	if name == "" {
		name = existing.Name
	}

	updated := repo.Account{
		ID: in.ID, Name: name, Provider: provider, Credentials: values,
	}
	if err := s.accounts.Update(ctx, updated); err != nil {
		return AccountView{}, err
	}
	return s.redact(updated), nil
}

func (s *Service) DeleteAccount(ctx context.Context, id int64) error {
	return s.accounts.Delete(ctx, id)
}

func (s *Service) provider(ctx context.Context, accountID int64) (dns.Provider, error) {
	account, err := s.accounts.Get(ctx, accountID)
	if err != nil {
		return nil, err
	}
	return dns.New(account.Provider, account.Credentials)
}

func (s *Service) TestAccount(ctx context.Context, id int64) error {
	client, err := s.provider(ctx, id)
	if err != nil {
		return err
	}
	return client.Check(ctx)
}

func (s *Service) ListDomains(ctx context.Context, accountID int64,
	opts dns.ListOptions) ([]dns.Domain, error) {

	client, err := s.provider(ctx, accountID)
	if err != nil {
		return nil, err
	}
	return client.ListDomains(ctx, opts)
}

func (s *Service) ListRecords(ctx context.Context, accountID int64, zone string,
	opts dns.ListOptions) ([]dns.Record, error) {

	client, err := s.provider(ctx, accountID)
	if err != nil {
		return nil, err
	}
	return client.ListRecords(ctx, zone, opts)
}

func (s *Service) AddRecord(ctx context.Context, accountID int64, zone string,
	record dns.Record) (string, error) {

	if err := record.Validate(); err != nil {
		return "", err
	}
	client, err := s.provider(ctx, accountID)
	if err != nil {
		return "", err
	}
	return client.AddRecord(ctx, zone, record)
}

func (s *Service) UpdateRecord(ctx context.Context, accountID int64, zone string,
	record dns.Record) error {

	if err := record.Validate(); err != nil {
		return err
	}
	client, err := s.provider(ctx, accountID)
	if err != nil {
		return err
	}
	return client.UpdateRecord(ctx, zone, record)
}

func (s *Service) DeleteRecord(ctx context.Context, accountID int64, zone, id string) error {
	client, err := s.provider(ctx, accountID)
	if err != nil {
		return err
	}
	return client.DeleteRecord(ctx, zone, id)
}

func (s *Service) SetRecordStatus(ctx context.Context, accountID int64,
	zone, id string, enabled bool) error {

	client, err := s.provider(ctx, accountID)
	if err != nil {
		return err
	}

	setter, ok := client.(dns.StatusSetter)
	if !ok {
		return fmt.Errorf("manage: this provider cannot pause records")
	}
	return setter.SetRecordStatus(ctx, zone, id, enabled)
}

func (s *Service) Capabilities(ctx context.Context, accountID int64) (dns.Capabilities, error) {
	account, err := s.accounts.Get(ctx, accountID)
	if err != nil {
		return dns.Capabilities{}, err
	}

	descriptor, known := dns.Describe(account.Provider)
	if !known {
		return dns.Capabilities{}, fmt.Errorf("manage: unknown DNS provider %q", account.Provider)
	}
	return descriptor.Capabilities, nil
}
