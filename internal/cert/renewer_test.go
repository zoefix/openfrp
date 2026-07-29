package cert

import (
	"context"
	"testing"
	"time"
)

func certExpiringIn(days int, now time.Time) *Certificate {
	return &Certificate{NotAfter: now.Add(time.Duration(days) * 24 * time.Hour)}
}

func TestDueRespectsThresholdAndAutoRenew(t *testing.T) {
	now := time.Date(2026, 7, 27, 12, 0, 0, 0, time.UTC)

	tests := []struct {
		name    string
		managed Managed
		want    bool
	}{
		{
			name:    "fresh certificate is not due",
			managed: Managed{AutoRenew: true, Certificate: certExpiringIn(60, now)},
			want:    false,
		},
		{
			name:    "inside the default threshold is due",
			managed: Managed{AutoRenew: true, Certificate: certExpiringIn(20, now)},
			want:    true,
		},
		{
			name:    "already expired is due",
			managed: Managed{AutoRenew: true, Certificate: certExpiringIn(-5, now)},
			want:    true,
		},
		{
			name:    "never issued is due",
			managed: Managed{AutoRenew: true},
			want:    true,
		},
		{
			name:    "auto-renew off is never due",
			managed: Managed{AutoRenew: false, Certificate: certExpiringIn(-5, now)},
			want:    false,
		},
		{
			name: "a custom threshold is honoured",
			managed: Managed{AutoRenew: true, RenewBefore: 7,
				Certificate: certExpiringIn(20, now)},
			want: false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.managed.Due(now); got != tc.want {
				t.Errorf("Due() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestFailedRenewalBacksOff(t *testing.T) {
	now := time.Date(2026, 7, 27, 12, 0, 0, 0, time.UTC)

	failed := Managed{
		AutoRenew:   true,
		Certificate: certExpiringIn(5, now),
		LastAttempt: now.Add(-time.Hour),
		LastError:   "dns credentials rejected",
	}

	if failed.Due(now) {
		t.Error("a renewal that failed an hour ago should be backing off")
	}

	failed.LastAttempt = now.Add(-retryBackoff - time.Minute)
	if !failed.Due(now) {
		t.Error("should be due again after the backoff window")
	}

	succeeded := Managed{
		AutoRenew:   true,
		Certificate: certExpiringIn(5, now),
		LastAttempt: now.Add(-time.Minute),
	}
	if !succeeded.Due(now) {
		t.Error("a certificate inside the threshold with no error should be due")
	}
}

func TestDefaultThresholdLeavesRoomToRecover(t *testing.T) {
	if DefaultRenewBefore < 14 {
		t.Errorf("DefaultRenewBefore = %d; below 14 days there is not enough "+
			"room to survive a multi-day outage", DefaultRenewBefore)
	}
}

func TestCertificateCoversUsesSingleLabelWildcards(t *testing.T) {

	tests := []struct {
		pattern, host string
		want          bool
	}{
		{"example.com", "example.com", true},
		{"example.com", "www.example.com", false},
		{"*.example.com", "www.example.com", true},
		{"*.example.com", "example.com", false},
		{"*.example.com", "a.b.example.com", false},
		{"*.b.example.com", "a.b.example.com", true},
		{"*.example.com", "example.com.evil.com", false},
		{"*.example.com", ".example.com", false},
	}

	for _, tc := range tests {
		if got := matchesName(tc.pattern, tc.host); got != tc.want {
			t.Errorf("matchesName(%q, %q) = %v, want %v",
				tc.pattern, tc.host, got, tc.want)
		}
	}
}

func TestNormaliseDomainsIsOrderIndependent(t *testing.T) {

	a := NormaliseDomains([]string{"B.example.com", "a.example.com", "a.example.com"})
	b := NormaliseDomains([]string{"a.example.com.", "b.EXAMPLE.com"})

	if len(a) != 2 {
		t.Fatalf("got %v, want two deduplicated entries", a)
	}
	if len(a) != len(b) {
		t.Fatalf("%v and %v have different lengths", a, b)
	}
	for i := range a {
		if a[i] != b[i] {
			t.Errorf("normalisation is order dependent: %v vs %v", a, b)
		}
	}
}

func TestNeedsDNSChallenge(t *testing.T) {
	if !NeedsDNSChallenge([]string{"example.com", "*.example.com"}) {
		t.Error("a wildcard must force DNS-01")
	}
	if NeedsDNSChallenge([]string{"example.com", "www.example.com"}) {
		t.Error("plain names should not require DNS-01")
	}
}

func TestIssueRejectsWildcardWithoutSolver(t *testing.T) {
	issuer := NewIssuer(nil)

	_, err := issuer.Issue(context.Background(), IssueRequest{
		Domains: []string{"*.example.com"},
		Account: &Account{},
	})
	if err == nil {
		t.Fatal("a wildcard without a DNS solver should be refused")
	}

	if !contains(err.Error(), "DNS-01") || !contains(err.Error(), "*.example.com") {
		t.Errorf("error should name the wildcard and the requirement: %v", err)
	}
}

func TestIssueRejectsUnknownCA(t *testing.T) {
	issuer := NewIssuer(nil)
	_, err := issuer.Issue(context.Background(), IssueRequest{
		Domains: []string{"example.com"},
		CA:      "not-a-ca",
		Account: &Account{},
	})
	if err == nil {
		t.Fatal("an unknown CA should be refused")
	}
}

func TestKeyTypesGenerate(t *testing.T) {
	for _, keyType := range KeyTypes() {
		if !keyType.Valid() {
			t.Errorf("%s is listed but reports itself invalid", keyType)
		}
		key, err := keyType.GenerateKey()
		if err != nil {
			t.Errorf("%s: %v", keyType, err)
			continue
		}
		if key == nil {
			t.Errorf("%s produced a nil key", keyType)
		}
		if _, err := keyType.legoKeyType(); err != nil {
			t.Errorf("%s has no lego mapping: %v", keyType, err)
		}
	}

	if _, err := KeyType("nonsense").legoKeyType(); err == nil {
		t.Error("an unknown key type should be refused")
	}
}

func TestAccountKeyRoundTrips(t *testing.T) {
	account := &Account{Email: "ops@example.com"}

	if err := account.ensureKey(); err != nil {
		t.Fatalf("ensureKey: %v", err)
	}
	if len(account.PrivateKey) == 0 {
		t.Fatal("no key was persisted")
	}
	first := account.GetPrivateKey()

	reloaded := &Account{Email: account.Email, PrivateKey: account.PrivateKey}
	if err := reloaded.ensureKey(); err != nil {
		t.Fatalf("reload: %v", err)
	}
	if reloaded.GetPrivateKey() == nil {
		t.Fatal("reloaded account has no key")
	}
	if first == nil {
		t.Fatal("original account has no key")
	}
}

func contains(haystack, needle string) bool {
	return len(haystack) >= len(needle) &&
		(haystack == needle || len(needle) == 0 ||
			indexOf(haystack, needle) >= 0)
}

func indexOf(haystack, needle string) int {
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			return i
		}
	}
	return -1
}
