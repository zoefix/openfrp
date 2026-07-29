package cert

import "testing"

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
