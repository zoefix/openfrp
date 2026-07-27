package storage_test

import (
	"context"
	"testing"

	"github.com/zoefix/openfrp/internal/storage/repo"
)

// TestEABCanBeStoredBeforeRegistration covers the order operations actually
// happen in.
//
// External account binding credentials are entered before the first issuance,
// and the account key is only generated when lego registers. The column was
// NOT NULL, so saving them failed with a constraint error naming a column the
// operator has never heard of, at the exact moment they were trying to unblock
// themselves.
func TestEABCanBeStoredBeforeRegistration(t *testing.T) {
	ctx := context.Background()
	accounts := repo.NewACMEAccounts(open(t).DB)

	saved, err := accounts.Save(ctx, repo.ACMEAccount{
		CA: "zerossl", Email: "ops@example.com",
		EABKeyID: "M1sM5Znr2agZjX", EABHMAC: "hmac-secret",
	})
	if err != nil {
		t.Fatalf("storing EAB before registration: %v", err)
	}
	if saved.ID == 0 {
		t.Fatal("save returned no id")
	}

	found, err := accounts.Find(ctx, "zerossl", "ops@example.com")
	if err != nil {
		t.Fatal(err)
	}
	if found.EABKeyID != "M1sM5Znr2agZjX" {
		t.Errorf("EAB key id is %q", found.EABKeyID)
	}
	if len(found.PrivateKey) != 0 {
		t.Error("a key appeared before registration")
	}
}

// TestSavingEABKeepsTheAccountKey is the sequel: once registered, updating the
// binding credentials must not wipe the key.
//
// Losing it costs the ability to revoke everything issued under that account,
// and does so silently — the next issuance would simply register a new one.
func TestSavingEABKeepsTheAccountKey(t *testing.T) {
	ctx := context.Background()
	accounts := repo.NewACMEAccounts(open(t).DB)

	if _, err := accounts.Save(ctx, repo.ACMEAccount{
		CA: "zerossl", Email: "ops@example.com",
		PrivateKey:   []byte("ACCOUNT-KEY"),
		Registration: []byte(`{"uri":"https://example/acct/1"}`),
		EABKeyID:     "first", EABHMAC: "first-hmac",
	}); err != nil {
		t.Fatal(err)
	}

	// Rotating the binding credentials, as SetEAB does, carries no key.
	if _, err := accounts.Save(ctx, repo.ACMEAccount{
		CA: "zerossl", Email: "ops@example.com",
		EABKeyID: "second", EABHMAC: "second-hmac",
	}); err != nil {
		t.Fatal(err)
	}

	found, err := accounts.Find(ctx, "zerossl", "ops@example.com")
	if err != nil {
		t.Fatal(err)
	}

	if string(found.PrivateKey) != "ACCOUNT-KEY" {
		t.Errorf("the account key is now %q; revocation is lost", found.PrivateKey)
	}
	if len(found.Registration) == 0 {
		t.Error("the registration was wiped, so the next issuance re-registers")
	}
	if found.EABKeyID != "second" {
		t.Errorf("the binding was not updated: %q", found.EABKeyID)
	}
}

// TestEABIsReusedAcrossContactAddresses is the behaviour an operator expects:
// the binding pair belongs to their account at the CA, so entering it once is
// enough however many certificates or contact addresses they later use.
//
// Keying the lookup strictly on (ca, email) made changing the contact address
// look like the credentials had been lost.
func TestEABIsReusedAcrossContactAddresses(t *testing.T) {
	ctx := context.Background()
	accounts := repo.NewACMEAccounts(open(t).DB)

	if _, err := accounts.Save(ctx, repo.ACMEAccount{
		CA: "zerossl", Email: "first@example.com",
		EABKeyID: "M1sM5Znr2agZjX", EABHMAC: "hmac-secret",
	}); err != nil {
		t.Fatal(err)
	}

	keyID, hmac, err := accounts.FindEAB(ctx, "zerossl")
	if err != nil {
		t.Fatalf("a stored binding was not found for the authority: %v", err)
	}
	if keyID != "M1sM5Znr2agZjX" || hmac != "hmac-secret" {
		t.Errorf("got %q/%q", keyID, hmac)
	}

	// A different authority must not borrow it.
	if _, _, err := accounts.FindEAB(ctx, "google"); err == nil {
		t.Error("a binding leaked across authorities")
	}
}

// TestNewestEABWins covers replacing a pair: the operator minted a new one at
// the CA and the old one no longer works.
func TestNewestEABWins(t *testing.T) {
	ctx := context.Background()
	accounts := repo.NewACMEAccounts(open(t).DB)

	for _, pair := range []struct{ email, key string }{
		{"old@example.com", "old-key"},
		{"new@example.com", "new-key"},
	} {
		if _, err := accounts.Save(ctx, repo.ACMEAccount{
			CA: "zerossl", Email: pair.email,
			EABKeyID: pair.key, EABHMAC: pair.key + "-hmac",
		}); err != nil {
			t.Fatal(err)
		}
	}

	keyID, _, err := accounts.FindEAB(ctx, "zerossl")
	if err != nil {
		t.Fatal(err)
	}
	if keyID != "new-key" {
		t.Errorf("FindEAB returned %q, want the most recently stored pair", keyID)
	}
}
