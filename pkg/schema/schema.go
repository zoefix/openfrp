// Package schema describes provider configuration forms as data.
//
// Nineteen DNS providers each need a credential form, and hand-writing
// nineteen LuCI forms would be nineteen chances to drift. Instead every
// provider declares its fields here, the local API serves them as JSON, and
// one renderer in the web UI draws all of them. Adding a provider then means
// adding a Go package and nothing else — no UI change at all.
//
// The shape is deliberately close to what dnsmgr proved works in practice,
// including the conditional-visibility expression, which is what lets one form
// cover a provider whose fields depend on which product you picked.
package schema

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
)

// Kind is the input control a field needs.
type Kind string

const (
	KindText     Kind = "text"
	KindPassword Kind = "password"
	KindNumber   Kind = "number"
	KindSelect   Kind = "select"
	KindBool     Kind = "bool"
	KindTextarea Kind = "textarea"
)

// Option is one choice in a select field.
type Option struct {
	Value string `json:"value"`
	Label string `json:"label"`
	// Description explains a choice whose consequences are not obvious.
	Description string `json:"description,omitempty"`
}

// Field is one input in a form.
type Field struct {
	Name  string `json:"name"`
	Label string `json:"label"`
	Kind  Kind   `json:"kind"`

	Required    bool   `json:"required,omitempty"`
	Default     string `json:"default,omitempty"`
	Placeholder string `json:"placeholder,omitempty"`
	Help        string `json:"help,omitempty"`

	// Options populate a select field.
	Options []Option `json:"options,omitempty"`

	// ShowIf hides the field unless the condition holds. The grammar is
	// deliberately tiny — see Condition — because a form definition that needs
	// a real expression language has outgrown being data.
	ShowIf string `json:"show_if,omitempty"`

	// Secret marks a value that must never be returned to the UI once stored.
	// Credentials are write-only: the form can set them and cannot read them
	// back, so a compromised browser session cannot exfiltrate them.
	Secret bool `json:"secret,omitempty"`

	// Pattern validates the value. Compiled once at registration.
	Pattern string `json:"pattern,omitempty"`
}

// Form is a complete provider configuration form.
type Form struct {
	// Fields are rendered in order.
	Fields []Field `json:"fields"`
}

// Validate checks a submitted value set against the form.
//
// Fields hidden by their ShowIf condition are skipped entirely, including
// their required check — a field the user could not see must not block them.
func (f Form) Validate(values map[string]string) error {
	for _, field := range f.Fields {
		visible, err := Visible(field.ShowIf, values)
		if err != nil {
			return fmt.Errorf("field %q: %w", field.Name, err)
		}
		if !visible {
			continue
		}

		value := strings.TrimSpace(values[field.Name])

		if value == "" {
			if field.Required {
				return fmt.Errorf("%s is required", field.Label)
			}
			continue
		}

		if field.Pattern != "" {
			re, err := regexp.Compile(field.Pattern)
			if err != nil {
				return fmt.Errorf("field %q has an invalid pattern: %w", field.Name, err)
			}
			if !re.MatchString(value) {
				return fmt.Errorf("%s is not in the expected format", field.Label)
			}
		}

		if field.Kind == KindSelect && len(field.Options) > 0 {
			ok := false
			for _, option := range field.Options {
				if option.Value == value {
					ok = true
					break
				}
			}
			if !ok {
				return fmt.Errorf("%s: %q is not one of the available choices", field.Label, value)
			}
		}
	}
	return nil
}

// ApplyDefaults fills unset fields from their declared defaults.
func (f Form) ApplyDefaults(values map[string]string) map[string]string {
	if values == nil {
		values = map[string]string{}
	}
	for _, field := range f.Fields {
		if _, set := values[field.Name]; !set && field.Default != "" {
			values[field.Name] = field.Default
		}
	}
	return values
}

// Redact removes secret values, for anything travelling back to a client.
// RedactedMarker replaces a stored secret on its way to a client.
//
// Exported because anything that consumes a redacted value has to be able to
// recognise it: a client that echoes this back on save must not have it stored
// as the new secret, which would destroy a working credential and say nothing.
const RedactedMarker = "••••••••"

func (f Form) Redact(values map[string]string) map[string]string {
	out := make(map[string]string, len(values))
	for key, value := range values {
		out[key] = value
	}
	for _, field := range f.Fields {
		if !field.Secret {
			continue
		}
		if out[field.Name] != "" {
			// A fixed marker rather than the real length: even the length of a
			// secret is information worth not leaking.
			out[field.Name] = RedactedMarker
		}
	}
	return out
}

// SecretNames lists the fields that must never be echoed back.
func (f Form) SecretNames() []string {
	var names []string
	for _, field := range f.Fields {
		if field.Secret {
			names = append(names, field.Name)
		}
	}
	sort.Strings(names)
	return names
}
