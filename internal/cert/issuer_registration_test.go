package cert

import (
	"encoding/json"
	"errors"
	"testing"

	"github.com/go-acme/lego/v4/acme"
	"github.com/go-acme/lego/v4/registration"
)

// TestRegistrationRoundTrips is the property that keeps issuance off the
// new-account endpoint.
//
// Both halves used to be broken: loading copied bytes to bytes without ever
// decoding them, and saving marshalled the byte slice rather than the
// resource. Nothing failed, because posting to new-account with a known key
// returns the account that key already owns — until the directory answers 500
// there, and issuance fails with a usable account sitting in the database.
func TestRegistrationRoundTrips(t *testing.T) {
	saved, err := json.Marshal(&registration.Resource{
		URI: "https://acme.example.com/acct/1",
	})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	account := &Account{Email: "ops@example.com"}
	if account.GetRegistration() != nil {
		t.Fatal("a fresh account claims a registration")
	}

	if err := account.LoadRegistration(saved); err != nil {
		t.Fatalf("load: %v", err)
	}
	if got := account.GetRegistration(); got == nil {
		t.Fatal("a stored registration was not restored; every issuance would " +
			"post to new-account again")
	} else if got.URI != "https://acme.example.com/acct/1" {
		t.Errorf("restored URI = %q", got.URI)
	}

	out, err := account.MarshalRegistration()
	if err != nil {
		t.Fatalf("marshal back: %v", err)
	}
	var back registration.Resource
	if err := json.Unmarshal(out, &back); err != nil {
		t.Fatalf("the saved form is not readable: %v", err)
	}
	if back.URI != "https://acme.example.com/acct/1" {
		t.Errorf("saved URI = %q, want it unchanged across a round trip", back.URI)
	}
}

func TestUnusableRegistrationIsIgnored(t *testing.T) {
	cases := map[string][]byte{
		"empty":         nil,
		"no uri":        []byte(`{}`),
		"uri is absent": []byte(`{"body":{"status":"valid"}}`),
	}
	for name, raw := range cases {
		account := &Account{}
		if err := account.LoadRegistration(raw); err != nil {
			t.Errorf("%s: %v", name, err)
		}
		if account.GetRegistration() != nil {
			t.Errorf("%s: was treated as usable; a registration with no URI "+
				"cannot sign anything, so trusting it turns one extra round "+
				"trip into a failure", name)
		}
	}

	account := &Account{}
	if err := account.LoadRegistration([]byte("not json")); err == nil {
		t.Error("unreadable stored registration was accepted")
	}
}

// TestTransientACMEErrorsAreRetryable separates "the authority is busy" from
// "you asked for something impossible". Retrying the second is pointless and
// failing on the first leaves an operator to do the retrying by hand.
func TestTransientACMEErrorsAreRetryable(t *testing.T) {
	retryable := map[int]string{
		429: "rateLimited",
		500: "serverInternal",
		502: "bad gateway",
		503: "Service busy; retry later",
		504: "gateway timeout",
	}
	for status, detail := range retryable {
		err := &acme.ProblemDetails{HTTPStatus: status, Detail: detail}
		if !transientACME(err) {
			t.Errorf("%d (%s) was treated as final; the authority expects a retry",
				status, detail)
		}
	}

	final := map[int]string{
		400: "malformed",
		403: "unauthorized",
		404: "not found",
		409: "already revoked",
	}
	for status, detail := range final {
		err := &acme.ProblemDetails{HTTPStatus: status, Detail: detail}
		if transientACME(err) {
			t.Errorf("%d (%s) was treated as retryable; retrying a refusal just "+
				"delays the error", status, detail)
		}
	}

	if transientACME(errors.New("connection refused")) {
		t.Error("a plain error was treated as an ACME problem")
	}
}
