package server

import (
	"testing"

	"github.com/zoefix/openfrp/internal/cert"
)

// TestChallengePathMatchesLego pins the interception against the ACME
// library's own definition rather than a copy of the URL.
//
// A path this server checks and a path lego publishes under are the same
// string or the validation silently 404s — with the authority reporting a
// generic failure and nothing here logging anything at all.
func TestChallengePathMatchesLego(t *testing.T) {
	const token = "sometoken123"

	if got := cert.HTTPChallengePath(token); got != challengePrefix+token {
		t.Errorf("lego fetches %q, this server answers %q",
			got, challengePrefix+token)
	}
}

func TestChallengeStoreAnswers(t *testing.T) {
	store := NewChallengeStore()

	if err := store.Publish("run-1", "openwrt.arm.moe", "tok", "tok.thumbprint"); err != nil {
		t.Fatal(err)
	}

	answer, ok := store.Answer(cert.HTTPChallengePath("tok"))
	if !ok {
		t.Fatal("the published challenge was not answered")
	}
	if answer != "tok.thumbprint" {
		t.Errorf("answered %q", answer)
	}

	// Anything else on that path is not ours to answer.
	if _, ok := store.Answer(cert.HTTPChallengePath("other")); ok {
		t.Error("answered a token that was never published")
	}
	if _, ok := store.Answer("/index.html"); ok {
		t.Error("answered an ordinary request")
	}
}

// TestChallengeTokenIsNotAPath refuses a token that could escape its URL
// segment. Tokens come from the authority and never contain these, so anything
// that does is either a bug or an attempt.
func TestChallengeTokenIsNotAPath(t *testing.T) {
	store := NewChallengeStore()

	for _, token := range []string{"../etc/passwd", "a/b", "with space", ""} {
		if err := store.Publish("run-1", "example.com", token, "auth"); err == nil {
			t.Errorf("accepted %q as a challenge token", token)
		}
	}
}

// TestWithdrawIsScopedToTheClient stops one client removing another's
// validation, which would fail an issuance that was about to succeed.
func TestWithdrawIsScopedToTheClient(t *testing.T) {
	store := NewChallengeStore()

	if err := store.Publish("run-1", "a.example", "tok", "auth"); err != nil {
		t.Fatal(err)
	}

	store.Withdraw("run-2", "tok")
	if _, ok := store.Answer(cert.HTTPChallengePath("tok")); !ok {
		t.Error("another client withdrew a challenge it did not publish")
	}

	store.Withdraw("run-1", "tok")
	if _, ok := store.Answer(cert.HTTPChallengePath("tok")); ok {
		t.Error("the owner could not withdraw its own challenge")
	}
}

// TestDisconnectWithdrawsEverything covers the client going away mid-issuance:
// nothing will clean up after it, and the server must not keep answering.
func TestDisconnectWithdrawsEverything(t *testing.T) {
	store := NewChallengeStore()

	store.Publish("run-1", "a.example", "tok-a", "auth")
	store.Publish("run-1", "b.example", "tok-b", "auth")
	store.Publish("run-2", "c.example", "tok-c", "auth")

	store.WithdrawClient("run-1")

	if store.Len() != 1 {
		t.Errorf("%d challenges left, want only the other client's", store.Len())
	}
	if _, ok := store.Answer(cert.HTTPChallengePath("tok-c")); !ok {
		t.Error("the other client's challenge was withdrawn too")
	}
}
