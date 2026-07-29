package schema

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
)

type Kind string

const (
	KindText     Kind = "text"
	KindPassword Kind = "password"
	KindNumber   Kind = "number"
	KindSelect   Kind = "select"
	KindBool     Kind = "bool"
	KindTextarea Kind = "textarea"
)

type Option struct {
	Value string `json:"value"`
	Label string `json:"label"`

	Description string `json:"description,omitempty"`
}

type Field struct {
	Name  string `json:"name"`
	Label string `json:"label"`
	Kind  Kind   `json:"kind"`

	Required    bool   `json:"required,omitempty"`
	Default     string `json:"default,omitempty"`
	Placeholder string `json:"placeholder,omitempty"`
	Help        string `json:"help,omitempty"`

	Options []Option `json:"options,omitempty"`

	ShowIf string `json:"show_if,omitempty"`

	Secret bool `json:"secret,omitempty"`

	Pattern string `json:"pattern,omitempty"`
}

type Form struct {
	Fields []Field `json:"fields"`
}

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

			out[field.Name] = RedactedMarker
		}
	}
	return out
}

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
