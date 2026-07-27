package manage

import (
	"context"
	"fmt"

	"github.com/zoefix/openfrp/internal/dns"
	_ "github.com/zoefix/openfrp/internal/dns/providers" // populate the registry
	"github.com/zoefix/openfrp/internal/storage/repo"
	"github.com/zoefix/openfrp/pkg/schema"
)

// AccountView is an account as the UI sees it: credentials redacted.
type AccountView struct {
	ID       int64  `json:"id"`
	Name     string `json:"name"`
	Provider string `json:"provider"`
	// ProviderLabel saves the UI a lookup when rendering a list.
	ProviderLabel string `json:"provider_label"`
	// Credentials carries only the non-secret fields. Secrets are omitted
	// entirely rather than replaced with a marker: a form pre-filled with a
	// placeholder submits that placeholder back, and storing it would destroy
	// the credential silently.
	Credentials map[string]string `json:"credentials"`

	// SecretsSet names the secret fields that do have a stored value, so the
	// form can say "configured, leave blank to keep" instead of looking empty.
	SecretsSet []string `json:"secrets_set"`
	CreatedAt  int64    `json:"created_at"`
	UpdatedAt  int64    `json:"updated_at"`
}

// Providers returns every registered DNS provider with its credential form,
// which is what the schema-driven UI renders.
func (s *Service) Providers() []dns.Descriptor {
	return dns.Descriptors()
}

// ListAccounts returns stored accounts, redacted.
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

// redact converts a stored account into something safe to send to a browser.
//
// Secrets are omitted rather than masked. Sending a mask as the field's value
// looks harmless and is not: the form renders it into the input, and saving
// without touching that field submits the mask back as the new secret,
// replacing a working credential with a row of bullets and reporting success.
// SecretsSet carries the "this is configured" signal instead, which is all the
// form actually needs.
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
		// A provider that was removed from the build. Redact everything: with
		// no form to say which fields are secret, all of them might be.
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

// AccountInput is a create or update request from the UI.
type AccountInput struct {
	ID          int64             `json:"id,omitempty"`
	Name        string            `json:"name"`
	Provider    string            `json:"provider"`
	Credentials map[string]string `json:"credentials"`
}

// CreateAccount validates and stores a new account.
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

// UpdateAccount stores an edited account.
//
// A secret submitted empty keeps whatever is already stored. The edit form
// never receives the real value — it shows a placeholder — so treating an
// empty submission as "clear this" would destroy a working credential every
// time someone renamed an account.
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

	// Changing provider invalidates the old credential shape entirely, so only
	// carry secrets forward when the provider is unchanged.
	if provider == existing.Provider {
		for _, name := range descriptor.Form.SecretNames() {
			// Blank means "unchanged" because the form never received the real
			// value. The marker is accepted too: a client that echoes back what
			// it was shown must not overwrite a working credential with a row
			// of bullets.
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

// DeleteAccount removes an account.
func (s *Service) DeleteAccount(ctx context.Context, id int64) error {
	return s.accounts.Delete(ctx, id)
}

// provider builds a live provider client from a stored account. This is the
// only place stored credentials are turned into an API client.
func (s *Service) provider(ctx context.Context, accountID int64) (dns.Provider, error) {
	account, err := s.accounts.Get(ctx, accountID)
	if err != nil {
		return nil, err
	}
	return dns.New(account.Provider, account.Credentials)
}

// TestAccount verifies stored credentials against the provider's API.
func (s *Service) TestAccount(ctx context.Context, id int64) error {
	client, err := s.provider(ctx, id)
	if err != nil {
		return err
	}
	return client.Check(ctx)
}

// ListDomains returns the zones an account can manage.
func (s *Service) ListDomains(ctx context.Context, accountID int64,
	opts dns.ListOptions) ([]dns.Domain, error) {

	client, err := s.provider(ctx, accountID)
	if err != nil {
		return nil, err
	}
	return client.ListDomains(ctx, opts)
}

// ListRecords returns the records in a zone.
func (s *Service) ListRecords(ctx context.Context, accountID int64, zone string,
	opts dns.ListOptions) ([]dns.Record, error) {

	client, err := s.provider(ctx, accountID)
	if err != nil {
		return nil, err
	}
	return client.ListRecords(ctx, zone, opts)
}

// AddRecord creates a record and returns its provider-assigned id.
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

// UpdateRecord modifies a record.
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

// DeleteRecord removes a record.
func (s *Service) DeleteRecord(ctx context.Context, accountID int64, zone, id string) error {
	client, err := s.provider(ctx, accountID)
	if err != nil {
		return err
	}
	return client.DeleteRecord(ctx, zone, id)
}

// SetRecordStatus pauses or resumes a record, where the provider supports it.
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

// Capabilities reports what an account's provider supports, so the UI can hide
// controls rather than offer them and fail on save.
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
