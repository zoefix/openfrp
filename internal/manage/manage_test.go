package manage_test

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/zoefix/openfrp/internal/config"
	"github.com/zoefix/openfrp/internal/manage"
	"github.com/zoefix/openfrp/internal/storage"
	"github.com/zoefix/openfrp/internal/storage/repo"
	"github.com/zoefix/openfrp/pkg/schema"
)

const realToken = "SUPERSECRET-TOKEN"

func service(t *testing.T) (*manage.Service, string) {
	t.Helper()

	path := filepath.Join(t.TempDir(), "openfrp.db")
	s, err := manage.New(path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s, path
}

func cloudflareAccount(t *testing.T, s *manage.Service) manage.AccountView {
	t.Helper()

	view, err := s.CreateAccount(context.Background(), manage.AccountInput{
		Name:        "cf-main",
		Provider:    "cloudflare",
		Credentials: map[string]string{"auth": "token", "api_token": realToken},
	})
	if err != nil {
		t.Fatalf("create account: %v", err)
	}
	return view
}

// TestSecretsNeverReachTheUI is the property the whole credential design rests
// on. A browser session that can read an API token can exfiltrate it, so the
// management layer must not hand one back under any code path.
func TestSecretsNeverReachTheUI(t *testing.T) {
	ctx := context.Background()
	s, _ := service(t)

	created := cloudflareAccount(t, s)
	if strings.Contains(created.Credentials["api_token"], realToken) {
		t.Error("CreateAccount returned the real token")
	}

	listed, err := s.ListAccounts(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, account := range listed {
		for field, value := range account.Credentials {
			if strings.Contains(value, realToken) {
				t.Errorf("ListAccounts returned the real token in %q", field)
			}
		}
	}
}

// TestBlankSecretKeepsStoredValue covers the edit form submitting an unchanged
// secret as empty, which is what it must do since it never received the real
// one. Treating that as "clear it" would destroy a working credential every
// time someone renamed an account.
func TestBlankSecretKeepsStoredValue(t *testing.T) {
	ctx := context.Background()
	s, path := service(t)

	created := cloudflareAccount(t, s)

	if _, err := s.UpdateAccount(ctx, manage.AccountInput{
		ID:          created.ID,
		Name:        "cf-renamed",
		Provider:    "cloudflare",
		Credentials: map[string]string{"auth": "token", "api_token": ""},
	}); err != nil {
		t.Fatalf("update: %v", err)
	}

	stored := storedCredentials(t, path, created.ID)
	if stored["api_token"] != realToken {
		t.Errorf("api_token is now %q; a rename destroyed the credential", stored["api_token"])
	}
}

// TestSecretsAreOmittedNotMasked checks the shape, not just the absence of the
// value.
//
// A masked secret sent as the field's value is worse than no protection: the
// form renders the mask into the input, and saving any other field submits the
// mask back as the new secret. The credential is destroyed and the UI reports
// success. Omitting the field is what makes the form's blank-means-unchanged
// path work.
func TestSecretsAreOmittedNotMasked(t *testing.T) {
	s, _ := service(t)
	created := cloudflareAccount(t, s)

	if _, present := created.Credentials["api_token"]; present {
		t.Errorf("api_token was sent to the UI as %q; it must be omitted",
			created.Credentials["api_token"])
	}

	var flagged bool
	for _, name := range created.SecretsSet {
		if name == "api_token" {
			flagged = true
		}
	}
	if !flagged {
		t.Error("api_token is stored but not listed in secrets_set, so the form " +
			"cannot tell it apart from one that was never configured")
	}
}

// TestMaskEchoedBackDoesNotOverwrite is defence in depth. Nothing should send
// the mask back now that it is never sent out, but if some client ever does,
// storing it would silently destroy the credential.
func TestMaskEchoedBackDoesNotOverwrite(t *testing.T) {
	ctx := context.Background()
	s, path := service(t)

	created := cloudflareAccount(t, s)

	if _, err := s.UpdateAccount(ctx, manage.AccountInput{
		ID: created.ID, Name: "cf-main", Provider: "cloudflare",
		Credentials: map[string]string{
			"auth": "token", "api_token": schema.RedactedMarker,
		},
	}); err != nil {
		t.Fatalf("update: %v", err)
	}

	if stored := storedCredentials(t, path, created.ID); stored["api_token"] != realToken {
		t.Errorf("api_token is now %q; the mask was stored as the credential",
			stored["api_token"])
	}
}

// TestChangingProviderDoesNotCarrySecretsOver checks the opposite case: the
// old credentials are meaningless under a new provider, so silently keeping
// them would leave an account that looks configured and cannot authenticate.
func TestChangingProviderDoesNotCarrySecretsOver(t *testing.T) {
	ctx := context.Background()
	s, path := service(t)

	created := cloudflareAccount(t, s)

	if _, err := s.UpdateAccount(ctx, manage.AccountInput{
		ID:       created.ID,
		Name:     "now-aliyun",
		Provider: "aliyun",
		Credentials: map[string]string{
			"access_key_id":     "LTAI",
			"access_key_secret": "other",
		},
	}); err != nil {
		t.Fatalf("update: %v", err)
	}

	stored := storedCredentials(t, path, created.ID)
	for field, value := range stored {
		if value == realToken {
			t.Errorf("the Cloudflare token survived into an Aliyun account as %q", field)
		}
	}
}

// TestUpdateRejectsInvalidCredentials makes sure validation is not skipped on
// the edit path, which is easy to do when secrets are being merged.
func TestUpdateRejectsInvalidCredentials(t *testing.T) {
	ctx := context.Background()
	s, _ := service(t)

	created := cloudflareAccount(t, s)

	_, err := s.UpdateAccount(ctx, manage.AccountInput{
		ID:       created.ID,
		Name:     "broken",
		Provider: "aliyun",
		// Switching provider without supplying the new provider's required
		// fields must fail rather than store an unusable account.
		Credentials: map[string]string{},
	})
	if err == nil {
		t.Error("an account with no credentials for its provider was accepted")
	}
}

// TestWildcardOrderNeedsDNSAccount checks the order is refused at creation.
//
// Only DNS-01 can prove a wildcard. Accepting the order and failing at
// issuance would surface as a red state minutes later, by which time the
// operator has moved on and the reason is buried in a log.
func TestWildcardOrderNeedsDNSAccount(t *testing.T) {
	ctx := context.Background()
	s, _ := service(t)

	_, err := s.CreateOrder(ctx, manage.OrderInput{
		Domains: []string{"*.aiqno.com"},
		KeyType: "ec256",
		CA:      "letsencrypt",
		Email:   "ops@example.com",
	})
	if err == nil {
		t.Fatal("a wildcard order was accepted with no DNS account")
	}
	if !strings.Contains(err.Error(), "DNS account") {
		t.Errorf("the error does not explain what is missing: %v", err)
	}
}

func TestOrderRequiresEmailAndKnownCA(t *testing.T) {
	ctx := context.Background()
	s, _ := service(t)

	if _, err := s.CreateOrder(ctx, manage.OrderInput{
		Domains: []string{"a.example.com"}, KeyType: "ec256", CA: "letsencrypt",
	}); err == nil {
		t.Error("an order with no contact email was accepted")
	}

	if _, err := s.CreateOrder(ctx, manage.OrderInput{
		Domains: []string{"a.example.com"}, KeyType: "ec256",
		CA: "not-a-ca", Email: "ops@example.com",
	}); err == nil {
		t.Error("an order naming an unknown CA was accepted")
	}
}

// TestIssueReachesTheCA checks the handoff from manage to cert.
//
// Issuance cannot be completed in a test — it talks to a real CA — so this
// asserts on how far it gets: past resolving the authority. The bug it guards
// is that manage passed a directory URL where the issuer wanted a key, so
// every issuance died at the first step with "unknown certificate authority"
// naming a URL that was perfectly valid.
func TestIssueReachesTheCA(t *testing.T) {
	ctx := context.Background()
	s, _ := service(t)

	account := cloudflareAccount(t, s)

	order, err := s.CreateOrder(ctx, manage.OrderInput{
		Domains: []string{"*.aiqno.com"}, KeyType: "ec256", CA: "letsencrypt",
		Email: "ops@example.com", AccountID: account.ID,
	})
	if err != nil {
		t.Fatal(err)
	}

	err = s.Issue(ctx, order.ID, nil)
	if err == nil {
		t.Skip("issuance unexpectedly succeeded; this test needs no network")
	}

	if strings.Contains(err.Error(), "unknown certificate authority") {
		t.Errorf("issuance failed while resolving the CA: %v", err)
	}

	// Whatever else happens, the order must not be left in "issuing" — the
	// next attempt would look like one is already running.
	orders, err := s.ListOrders(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if orders[0].State == "issuing" {
		t.Error("a failed issuance left the order stuck in the issuing state")
	}
}

// TestNewOrdersAutoRenewByDefault guards the trap that a plain bool would
// reintroduce: an order created without mentioning renewal must renew, or the
// first anyone hears of it is an expired certificate.
func TestNewOrdersAutoRenewByDefault(t *testing.T) {
	ctx := context.Background()
	s, _ := service(t)

	account := cloudflareAccount(t, s)

	order, err := s.CreateOrder(ctx, manage.OrderInput{
		Domains: []string{"*.aiqno.com"}, KeyType: "ec256", CA: "letsencrypt",
		Email: "ops@example.com", AccountID: account.ID,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !order.AutoRenew {
		t.Error("a new order does not auto-renew; it will silently expire")
	}
}

// storedCredentials reads what is actually on disk, bypassing redaction.
func storedCredentials(t *testing.T, path string, id int64) map[string]string {
	t.Helper()

	db, err := storage.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	account, err := repo.NewAccounts(db.DB).Get(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	return account.Credentials
}

// TestOrderWithoutDNSUsesHTTPValidation covers the case that needs no zone
// credentials.
//
// A single name can be proved by serving a file, which the tunnel server does
// on this router's behalf. Demanding an API key for the whole zone in order to
// certify one host is a real cost, and the only thing that genuinely requires
// it is a wildcard.
func TestOrderWithoutDNSUsesHTTPValidation(t *testing.T) {
	ctx := context.Background()
	s, _ := service(t)

	// With no server configured there is nothing to answer an HTTP validation,
	// and the order says so rather than failing later.
	_, err := s.CreateOrder(ctx, manage.OrderInput{
		Domains: []string{"openwrt.arm.moe"},
		KeyType: "ec256", CA: "letsencrypt", Email: "ops@example.com",
	})
	if err == nil {
		t.Fatal("an order was accepted with no way to prove ownership")
	}

	s.SetHTTPChallengeServers([]config.Upstream{
		{Name: "main", Addr: "203.0.113.1", Port: 7000},
	}, "test")

	if _, err := s.CreateOrder(ctx, manage.OrderInput{
		Domains: []string{"openwrt.arm.moe"},
		KeyType: "ec256", CA: "letsencrypt", Email: "ops@example.com",
	}); err != nil {
		t.Errorf("an order that can be validated over HTTP was rejected: %v", err)
	}

	// A wildcard still cannot: no file on any host proves a name that does not
	// exist yet.
	_, err = s.CreateOrder(ctx, manage.OrderInput{
		Domains: []string{"*.arm.moe"},
		KeyType: "ec256", CA: "letsencrypt", Email: "ops@example.com",
	})
	if err == nil {
		t.Fatal("a wildcard was accepted without a DNS account")
	}
	if !strings.Contains(err.Error(), "wildcard") {
		t.Errorf("the error does not explain why: %v", err)
	}
}
