package vhost

import (
	"fmt"
	"strings"

	"golang.org/x/net/idna"
)

const Wildcard = "*"

const CatchAll = "*"

type Pattern struct {
	raw string

	labels []string

	catchAll bool
}

func (p Pattern) String() string { return p.raw }

func (p Pattern) IsCatchAll() bool { return p.catchAll }

func (p Pattern) Labels() []string { return p.labels }

func ParsePattern(s string) (Pattern, error) {
	normalised, err := normalise(s)
	if err != nil {
		return Pattern{}, err
	}

	if normalised == CatchAll {
		return Pattern{raw: CatchAll, catchAll: true}, nil
	}

	parts := strings.Split(normalised, ".")
	if len(parts) < 2 {
		return Pattern{}, fmt.Errorf("vhost: pattern %q needs at least two labels", s)
	}

	for i, label := range parts {
		if label == "" {
			return Pattern{}, fmt.Errorf("vhost: pattern %q has an empty label", s)
		}
		if label == Wildcard {

			if i != 0 {
				return Pattern{}, fmt.Errorf(
					"vhost: pattern %q: '*' is only allowed as the leftmost label", s)
			}
			continue
		}
		if strings.Contains(label, Wildcard) {
			return Pattern{}, fmt.Errorf(
				"vhost: pattern %q: '*' must be a whole label, not part of one", s)
		}
	}

	if parts[0] == Wildcard && len(parts) < 3 {
		return Pattern{}, fmt.Errorf(
			"vhost: pattern %q is too broad; wildcards need a domain beneath them", s)
	}

	return Pattern{raw: normalised, labels: reverse(parts)}, nil
}

func NormaliseHost(host string) (string, error) {
	return normalise(host)
}

func normalise(s string) (string, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return "", fmt.Errorf("vhost: empty host")
	}

	if !strings.HasPrefix(s, "[") {
		if idx := strings.LastIndexByte(s, ':'); idx != -1 {
			if !strings.Contains(s[idx+1:], ":") && !strings.Contains(s[idx+1:], ".") {
				s = s[:idx]
			}
		}
	}

	s = strings.ToLower(s)

	s = strings.TrimSuffix(s, ".")

	if s == "" {
		return "", fmt.Errorf("vhost: host is empty after normalisation")
	}
	if s == CatchAll {
		return CatchAll, nil
	}

	converted, err := idna.Lookup.ToASCII(strings.ReplaceAll(s, Wildcard, "\x00"))
	if err != nil {

		converted, err = convertLabels(s)
		if err != nil {
			return "", fmt.Errorf("vhost: %q is not a usable host name: %w", s, err)
		}
		return converted, nil
	}
	return strings.ReplaceAll(converted, "\x00", Wildcard), nil
}

func convertLabels(s string) (string, error) {
	parts := strings.Split(s, ".")
	for i, label := range parts {
		if label == Wildcard || label == "" {
			continue
		}
		converted, err := idna.ToASCII(label)
		if err != nil {
			return "", err
		}
		parts[i] = converted
	}
	return strings.Join(parts, "."), nil
}

func splitHostLabels(host string) ([]string, error) {
	normalised, err := normalise(host)
	if err != nil {
		return nil, err
	}
	if normalised == CatchAll {
		return nil, fmt.Errorf("vhost: %q is a pattern, not a host", host)
	}
	return reverse(strings.Split(normalised, ".")), nil
}

func reverse(parts []string) []string {
	out := make([]string, len(parts))
	for i, p := range parts {
		out[len(parts)-1-i] = p
	}
	return out
}
