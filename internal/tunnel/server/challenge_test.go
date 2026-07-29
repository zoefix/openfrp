package server

import (
	"testing"
	"time"

	"github.com/zoefix/openfrp/internal/cert"
)

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

	if _, ok := store.Answer(cert.HTTPChallengePath("other")); ok {
		t.Error("answered a token that was never published")
	}
	if _, ok := store.Answer("/index.html"); ok {
		t.Error("answered an ordinary request")
	}
}

func TestChallengeTokenIsNotAPath(t *testing.T) {
	store := NewChallengeStore()

	for _, token := range []string{"../etc/passwd", "a/b", "with space", ""} {
		if err := store.Publish("run-1", "example.com", token, "auth"); err == nil {
			t.Errorf("accepted %q as a challenge token", token)
		}
	}
}

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

func TestChallengeOutlastsItsPublisher(t *testing.T) {
	store := NewChallengeStore()

	if err := store.Publish("run-1", "a.example", "tok-a", "auth"); err != nil {
		t.Fatal(err)
	}

	if _, ok := store.Answer(cert.HTTPChallengePath("tok-a")); !ok {
		t.Error("the challenge did not outlast the run that published it")
	}

	store.Withdraw("run-1", "tok-a")
	if _, ok := store.Answer(cert.HTTPChallengePath("tok-a")); ok {
		t.Error("an explicit withdrawal left the challenge answering")
	}
}

func TestExpiredChallengeStopsAnswering(t *testing.T) {
	store := NewChallengeStore()

	if err := store.Publish("run-1", "a.example", "tok-a", "auth"); err != nil {
		t.Fatal(err)
	}

	store.mu.Lock()
	aged := store.tokens["tok-a"]
	aged.deadline = time.Now().Add(-time.Second)
	store.tokens["tok-a"] = aged
	store.mu.Unlock()

	if _, ok := store.Answer(cert.HTTPChallengePath("tok-a")); ok {
		t.Error("an expired challenge is still being answered")
	}
}
