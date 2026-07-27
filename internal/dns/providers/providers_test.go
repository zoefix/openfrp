package providers

import (
	"strings"
	"testing"

	"github.com/zoefix/openfrp/internal/dns"
	"github.com/zoefix/openfrp/pkg/schema"
)

// TestEveryProviderIsWellFormed is the guard that makes adding a provider safe.
//
// A provider is only reachable through its descriptor, so a mistake in the
// declaration — a missing secret marker, a line it claims but cannot express,
// a credential form that cannot possibly validate — would not show up until
// someone tried to use it with real credentials. This checks all of it at
// build time instead.
func TestEveryProviderIsWellFormed(t *testing.T) {
	descriptors := dns.Descriptors()
	if len(descriptors) < 5 {
		t.Fatalf("only %d providers registered; expected the P5 set", len(descriptors))
	}

	for _, desc := range descriptors {
		t.Run(desc.Key, func(t *testing.T) {
			if desc.Label == "" {
				t.Error("no display label")
			}
			if len(desc.Form.Fields) == 0 {
				t.Fatal("no credential fields")
			}

			var (
				sawSecret   bool
				sawRequired bool
			)
			names := map[string]bool{}

			for _, field := range desc.Form.Fields {
				if field.Name == "" || field.Label == "" {
					t.Errorf("field %+v is missing a name or label", field)
				}
				if names[field.Name] {
					t.Errorf("duplicate field name %q", field.Name)
				}
				names[field.Name] = true

				if field.Kind == "" {
					t.Errorf("field %q has no kind", field.Name)
				}
				// A password field that is not marked secret would be echoed
				// back to the browser. That is the one mistake here with a
				// security consequence.
				if field.Kind == "password" && !field.Secret {
					t.Errorf("field %q is a password but is not marked secret", field.Name)
				}
				if field.Secret {
					sawSecret = true
				}
				if field.Required {
					sawRequired = true
				}
				if field.ShowIf != "" {
					if _, err := evaluate(field.ShowIf); err != nil {
						t.Errorf("field %q has an invalid condition %q: %v",
							field.Name, field.ShowIf, err)
					}
				}
			}

			if !sawSecret {
				t.Error("no field is marked secret; every provider here needs a credential")
			}
			if !sawRequired {
				t.Error("no field is required")
			}

			// Every line the provider advertises must actually map to
			// something, or a record using it would be silently mistranslated.
			for _, line := range desc.Capabilities.Lines {
				if _, ok := dns.ProviderLine(desc.Key, line); !ok {
					t.Errorf("advertises line %s with no mapping", line)
				}
			}
			if _, ok := dns.ProviderLine(desc.Key, dns.LineDefault); !ok {
				t.Error("has no default line mapping")
			}

			if desc.Capabilities.MinTTL < 0 {
				t.Error("negative minimum TTL")
			}
		})
	}
}

// evaluate exercises a condition so a malformed one fails the build. Values
// are empty on purpose: the point is that the grammar parses, not what it
// evaluates to.
func evaluate(condition string) (bool, error) {
	return schema.Visible(condition, map[string]string{})
}

func TestProviderConstructionRejectsMissingCredentials(t *testing.T) {
	for _, desc := range dns.Descriptors() {
		// An empty credential set must be refused by every provider rather
		// than producing a client that fails later with a confusing API error.
		if _, err := dns.New(desc.Key, map[string]string{}); err == nil {
			t.Errorf("%s: empty credentials were accepted", desc.Key)
		}
	}
}

func TestProviderConstructionSucceedsWithPlausibleCredentials(t *testing.T) {
	// Fill every required field with a placeholder. This does not talk to any
	// API; it checks that a complete form actually yields a provider.
	for _, desc := range dns.Descriptors() {
		values := map[string]string{}
		for _, field := range desc.Form.Fields {
			if field.Default != "" {
				values[field.Name] = field.Default
				continue
			}
			switch field.Kind {
			case "select":
				if len(field.Options) > 0 {
					values[field.Name] = field.Options[0].Value
				}
			case "number":
				values[field.Name] = "1"
			default:
				values[field.Name] = "placeholder"
			}
		}

		provider, err := dns.New(desc.Key, values)
		if err != nil {
			t.Errorf("%s: %v", desc.Key, err)
			continue
		}
		if provider == nil {
			t.Errorf("%s: returned a nil provider", desc.Key)
			continue
		}
		if caps := provider.Capabilities(); caps.MinTTL < 0 {
			t.Errorf("%s: negative minimum TTL from the live provider", desc.Key)
		}
	}
}

func TestExpectedProvidersArePresent(t *testing.T) {
	want := []string{"aliyun", "cloudflare", "dnspod", "huawei", "west",
		"namesilo", "powerdns"}
	have := strings.Join(dns.Keys(), ",")

	for _, key := range want {
		if !strings.Contains(have, key) {
			t.Errorf("provider %q is not registered (have: %s)", key, have)
		}
	}
}
