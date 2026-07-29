package schema

import (
	"strings"
	"testing"
)

func testForm() Form {
	return Form{Fields: []Field{
		{Name: "provider", Label: "Product", Kind: KindSelect, Required: true,
			Options: []Option{{Value: "dns"}, {Value: "esa"}}},
		{Name: "access_key", Label: "Access key", Kind: KindText, Required: true},
		{Name: "secret", Label: "Secret", Kind: KindPassword, Required: true, Secret: true},
		{Name: "region", Label: "Region", Kind: KindText, Default: "cn-hangzhou",
			ShowIf: "provider == 'esa'"},
		{Name: "port", Label: "Port", Kind: KindNumber, Pattern: `^\d+$`},
	}}
}

func TestConditionGrammar(t *testing.T) {
	values := map[string]string{"kind": "http", "mode": "terminate", "empty": ""}

	tests := []struct {
		condition string
		want      bool
	}{
		{"", true},
		{"kind == 'http'", true},
		{"kind == \"http\"", true},
		{"kind == 'tcp'", false},
		{"kind != 'tcp'", true},
		{"kind != 'http'", false},
		{"kind == 'http' && mode == 'terminate'", true},
		{"kind == 'http' && mode == 'passthrough'", false},
		{"kind == 'tcp' || mode == 'terminate'", true},
		{"kind == 'tcp' || mode == 'passthrough'", false},

		{"kind == 'tcp' && mode == 'x' || kind == 'http'", true},
		{"kind == 'http' || kind == 'tcp' && mode == 'x'", true},

		{"missing == ''", true},
		{"missing != ''", false},
		{"empty == ''", true},

		{"kind == http", true},
	}

	for _, tc := range tests {
		got, err := Visible(tc.condition, values)
		if err != nil {
			t.Errorf("Visible(%q): unexpected error: %v", tc.condition, err)
			continue
		}
		if got != tc.want {
			t.Errorf("Visible(%q) = %v, want %v", tc.condition, got, tc.want)
		}
	}
}

func TestConditionRejectsNonsense(t *testing.T) {
	for _, condition := range []string{"kind", "kind = 'x'", "== 'x'"} {
		if _, err := Visible(condition, nil); err == nil {
			t.Errorf("Visible(%q) should have failed", condition)
		}
	}
}

func TestHiddenFieldsAreNotRequired(t *testing.T) {
	form := testForm()

	values := map[string]string{"provider": "dns", "access_key": "AK", "secret": "SK"}
	if err := form.Validate(values); err != nil {
		t.Errorf("a hidden field blocked validation: %v", err)
	}

	values["provider"] = "esa"
	if err := form.Validate(values); err != nil {
		t.Errorf("esa product failed validation: %v", err)
	}
}

func TestValidateRequiredAndPattern(t *testing.T) {
	form := testForm()

	if err := form.Validate(map[string]string{"provider": "dns", "secret": "SK"}); err == nil {
		t.Error("a missing required field should fail")
	} else if !strings.Contains(err.Error(), "Access key") {
		t.Errorf("error should name the field: %v", err)
	}

	bad := map[string]string{"provider": "dns", "access_key": "AK", "secret": "SK", "port": "abc"}
	if err := form.Validate(bad); err == nil {
		t.Error("a value failing its pattern should be rejected")
	}

	good := map[string]string{"provider": "dns", "access_key": "AK", "secret": "SK", "port": "53"}
	if err := form.Validate(good); err != nil {
		t.Errorf("valid values were rejected: %v", err)
	}
}

func TestValidateRejectsUnknownSelectOption(t *testing.T) {
	form := testForm()
	values := map[string]string{"provider": "nope", "access_key": "AK", "secret": "SK"}
	if err := form.Validate(values); err == nil {
		t.Error("an option outside the declared set should be rejected")
	}
}

func TestRedactNeverEchoesSecrets(t *testing.T) {
	form := testForm()
	values := map[string]string{"access_key": "AK", "secret": "super-secret-value"}

	redacted := form.Redact(values)

	if redacted["secret"] == "super-secret-value" {
		t.Fatal("the secret was returned verbatim")
	}
	if strings.Contains(redacted["secret"], "super") {
		t.Error("the redacted value leaks part of the secret")
	}

	if len(redacted["secret"]) == len("super-secret-value") {
		t.Error("the redacted value leaks the secret's length")
	}
	if redacted["access_key"] != "AK" {
		t.Error("a non-secret field should be returned unchanged")
	}

	if values["secret"] != "super-secret-value" {
		t.Error("Redact mutated its input")
	}
}

func TestApplyDefaults(t *testing.T) {
	form := testForm()
	values := form.ApplyDefaults(map[string]string{"provider": "esa"})

	if values["region"] != "cn-hangzhou" {
		t.Errorf("region = %q, want the declared default", values["region"])
	}
	if values["provider"] != "esa" {
		t.Error("ApplyDefaults overwrote a supplied value")
	}
}

func TestSecretNames(t *testing.T) {
	got := testForm().SecretNames()
	if len(got) != 1 || got[0] != "secret" {
		t.Errorf("SecretNames() = %v, want [secret]", got)
	}
}
