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

func TestUpdateRejectsInvalidCredentials(t *testing.T) {
	ctx := context.Background()
	s, _ := service(t)

	created := cloudflareAccount(t, s)

	_, err := s.UpdateAccount(ctx, manage.AccountInput{
		ID:       created.ID,
		Name:     "broken",
		Provider: "aliyun",

		Credentials: map[string]string{},
	})
	if err == nil {
		t.Error("an account with no credentials for its provider was accepted")
	}
}

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

	orders, err := s.ListOrders(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if orders[0].State == "issuing" {
		t.Error("a failed issuance left the order stuck in the issuing state")
	}
}

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

func TestOrderWithoutDNSUsesHTTPValidation(t *testing.T) {
	ctx := context.Background()
	s, _ := service(t)

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

func TestHTTPValidationChecksWhereTheNamePoints(t *testing.T) {
	ctx := context.Background()
	s, _ := service(t)

	s.SetHTTPChallengeServers([]config.Upstream{
		{Name: "main", Addr: "203.0.113.1", Port: 7000},
	}, "test")

	s.SetHTTPChallengeResolver(func(context.Context, string) []string {
		return []string{"64.90.22.142"}
	})

	order, err := s.CreateOrder(ctx, manage.OrderInput{
		Domains: []string{"openwrt.arm.moe"},
		KeyType: "ec256", CA: "letsencrypt", Email: "ops@example.com",
	})
	if err != nil {
		t.Fatal(err)
	}

	err = s.Issue(ctx, order.ID, nil)
	if err == nil {
		t.Fatal("issuance proceeded for a name that points elsewhere")
	}
	if !strings.Contains(err.Error(), "64.90.22.142") {
		t.Errorf("the error does not say where the name points: %v", err)
	}
	if !strings.Contains(err.Error(), "203.0.113.1") {
		t.Errorf("the error does not say where it should point: %v", err)
	}

	if strings.Contains(err.Error(), "acme") {
		t.Errorf("reached the authority anyway: %v", err)
	}
}

func TestHTTPValidationAllowsAMatchingName(t *testing.T) {
	ctx := context.Background()
	s, _ := service(t)

	s.SetHTTPChallengeServers([]config.Upstream{
		{Name: "main", Addr: "203.0.113.1", Port: 7000},
	}, "test")
	s.SetHTTPChallengeResolver(func(context.Context, string) []string {
		return []string{"203.0.113.1"}
	})

	order, err := s.CreateOrder(ctx, manage.OrderInput{
		Domains: []string{"good.example"},
		KeyType: "ec256", CA: "letsencrypt", Email: "ops@example.com",
	})
	if err != nil {
		t.Fatal(err)
	}

	if err := s.Issue(ctx, order.ID, nil); err != nil &&
		strings.Contains(err.Error(), "resolves to") {
		t.Errorf("a name pointing at the server was rejected: %v", err)
	}
}

func TestHTTPValidationSaysWhenItCouldNotCheck(t *testing.T) {
	ctx := context.Background()
	s, _ := service(t)

	s.SetHTTPChallengeServers([]config.Upstream{
		{Name: "main", Addr: "203.0.113.1", Port: 7000},
	}, "test")

	order, err := s.CreateOrder(ctx, manage.OrderInput{
		Domains: []string{"unresolvable.example"},
		KeyType: "ec256", CA: "letsencrypt", Email: "ops@example.com",
	})
	if err != nil {
		t.Fatal(err)
	}

	var notes []string
	s.SetHTTPChallengeResolver(func(context.Context, string) []string { return nil })
	s.Issue(ctx, order.ID, func(note string) { notes = append(notes, note) })

	joined := strings.Join(notes, " | ")
	if strings.Contains(joined, "points at the tunnel server") {
		t.Errorf("claimed the name was verified when it could not be looked up: %s", joined)
	}
	if !strings.Contains(joined, "unverified") {
		t.Errorf("did not say the check was inconclusive: %s", joined)
	}
}

func TestHTTPValidationDoesNotBlockOnDoubt(t *testing.T) {
	ctx := context.Background()
	s, _ := service(t)

	s.SetHTTPChallengeServers([]config.Upstream{
		{Name: "main", Addr: "203.0.113.1", Port: 7000},
	}, "test")
	s.SetHTTPChallengeResolver(func(context.Context, string) []string { return nil })

	order, err := s.CreateOrder(ctx, manage.OrderInput{
		Domains: []string{"unresolvable.example"},
		KeyType: "ec256", CA: "letsencrypt", Email: "ops@example.com",
	})
	if err != nil {
		t.Fatal(err)
	}

	if err := s.Issue(ctx, order.ID, nil); err != nil &&
		strings.Contains(err.Error(), "resolves to") {
		t.Errorf("an unresolvable name was refused rather than attempted: %v", err)
	}
}
