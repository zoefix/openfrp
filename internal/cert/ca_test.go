package cert

import "testing"

// TestResolveCA covers the two things a caller may legitimately pass, and the
// bug that came from confusing them.
//
// The management layer looked a CA up by key, then handed the issuer the
// resulting *directory URL* while the issuer expected a key. Every issuance
// failed with `unknown certificate authority "https://acme-v02..."` — an error
// naming a URL that is manifestly correct, which is about as misleading as it
// gets. Accepting both forms removes the trap and is also how an unlisted or
// private ACME server is reached.
func TestResolveCA(t *testing.T) {
	cases := []struct {
		name      string
		input     string
		directory string
		requires  bool
		wantErr   bool
	}{
		{
			name:      "empty defaults to Let's Encrypt",
			input:     "",
			directory: DirectoryLetsEncrypt,
		},
		{
			name:      "a known key",
			input:     "letsencrypt",
			directory: DirectoryLetsEncrypt,
		},
		{
			name:      "a known key that needs account binding",
			input:     "zerossl",
			directory: DirectoryZeroSSL,
			requires:  true,
		},
		{
			// The exact bug: the directory URL of a known CA must resolve, and
			// must keep that CA's metadata rather than becoming an anonymous
			// custom authority that silently skips the EAB requirement.
			input:     DirectoryZeroSSL,
			name:      "the directory URL of a known CA keeps its metadata",
			directory: DirectoryZeroSSL,
			requires:  true,
		},
		{
			name:      "an unlisted ACME server",
			input:     "https://acme.internal.example/directory",
			directory: "https://acme.internal.example/directory",
		},
		{
			name:    "a mistyped key is an error, not a URL",
			input:   "letsencrpyt",
			wantErr: true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ca, err := resolveCA(tc.input)

			if tc.wantErr {
				if err == nil {
					t.Fatalf("resolveCA(%q) succeeded, want an error", tc.input)
				}
				return
			}
			if err != nil {
				t.Fatalf("resolveCA(%q): %v", tc.input, err)
			}
			if ca.Directory != tc.directory {
				t.Errorf("directory = %q, want %q", ca.Directory, tc.directory)
			}
			if ca.RequiresEAB != tc.requires {
				t.Errorf("RequiresEAB = %v, want %v", ca.RequiresEAB, tc.requires)
			}
		})
	}
}
