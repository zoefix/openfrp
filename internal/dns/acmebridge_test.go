package dns

import "testing"

func TestChallengeKey(t *testing.T) {
	cases := []struct {
		name  string
		input string
		want  string
	}{
		{
			name:  "a bare domain gets the label",
			input: "aiqno.com",
			want:  "_acme-challenge.aiqno.com",
		},
		{
			name:  "a wildcard validates against its base name",
			input: "*.aiqno.com",
			want:  "_acme-challenge.aiqno.com",
		},
		{
			name:  "an effective FQDN is left alone",
			input: "_acme-challenge.aiqno.com",
			want:  "_acme-challenge.aiqno.com",
		},
		{
			name:  "a trailing dot is trimmed",
			input: "_acme-challenge.aiqno.com.",
			want:  "_acme-challenge.aiqno.com",
		},
		{
			name:  "case is normalised",
			input: "*.AiqNo.CoM",
			want:  "_acme-challenge.aiqno.com",
		},
		{

			name:  "a delegated challenge name is preserved",
			input: "_acme-challenge.aiqno.com.acme.delegated.example",
			want:  "_acme-challenge.aiqno.com.acme.delegated.example",
		},
		{
			name:  "a subdomain keeps its labels",
			input: "shop.aiqno.com",
			want:  "_acme-challenge.shop.aiqno.com",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := challengeKey(tc.input); got != tc.want {
				t.Errorf("challengeKey(%q) = %q, want %q", tc.input, got, tc.want)
			}
		})
	}
}

func TestChallengeKeyIsIdempotent(t *testing.T) {
	for _, input := range []string{"aiqno.com", "*.aiqno.com", "a.b.aiqno.com"} {
		once := challengeKey(input)
		if twice := challengeKey(once); twice != once {
			t.Errorf("challengeKey is not idempotent for %q: %q then %q",
				input, once, twice)
		}
	}
}
