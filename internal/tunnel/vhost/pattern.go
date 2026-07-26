// Package vhost routes connections to tunnels by domain name.
//
// The matching rule is the project's defining behaviour, so it is stated once
// here and enforced by the trie in this package:
//
//	A "*" label matches EXACTLY ONE label, and may appear at any depth.
//
//	  *.aaa.com        matches www.aaa.com      but not x.bb.aaa.com
//	  *.bb.aaa.com     matches x.bb.aaa.com     but not y.x.bb.aaa.com
//	  aaa.com          matches aaa.com          but no subdomain
//
// Priority runs exact, then the deepest wildcard, then a global catch-all.
//
// frp behaves differently: its router replaces successive leading labels with
// "*", so *.aaa.com there also matches x.bb.aaa.com. Ours deliberately mirrors
// DNS and TLS certificate semantics instead — a Let's Encrypt *.aaa.com covers
// exactly one level — so a route can never succeed while the certificate for
// it fails. That mismatch is silent and miserable to debug, which is why the
// stricter rule is worth the extra pattern a user occasionally has to write.
package vhost

import (
	"fmt"
	"strings"

	"golang.org/x/net/idna"
)

// Wildcard is the label that matches exactly one arbitrary label.
const Wildcard = "*"

// CatchAll is the pattern matching any host not claimed by a more specific
// route. It is handled outside the trie because "*" as a whole pattern means
// "anything at any depth", not "exactly one label".
const CatchAll = "*"

// Pattern is a validated domain pattern, stored as labels in reverse order so
// it can be walked from the public suffix inward.
type Pattern struct {
	// raw is the normalised, human-readable form, kept for diagnostics.
	raw string
	// labels holds the pattern reversed: aaa.com becomes {"com", "aaa"}.
	labels []string
	// catchAll marks the bare "*" pattern.
	catchAll bool
}

// String returns the normalised pattern.
func (p Pattern) String() string { return p.raw }

// IsCatchAll reports whether this is the bare "*" fallback.
func (p Pattern) IsCatchAll() bool { return p.catchAll }

// Labels returns the reversed label list. The slice is shared, so callers must
// not modify it.
func (p Pattern) Labels() []string { return p.labels }

// ParsePattern validates and normalises a routing pattern.
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
			// A wildcard is only meaningful as the leftmost label. Allowing it
			// in the middle would invite patterns like a.*.com whose intent is
			// ambiguous and whose certificate story does not exist.
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

	// Reject *.com and the like: a wildcard directly under a public-suffix-like
	// single label would let one tunnel claim an entire TLD.
	if parts[0] == Wildcard && len(parts) < 3 {
		return Pattern{}, fmt.Errorf(
			"vhost: pattern %q is too broad; wildcards need a domain beneath them", s)
	}

	return Pattern{raw: normalised, labels: reverse(parts)}, nil
}

// NormaliseHost prepares an incoming host for lookup: lowercase, no trailing
// dot, no port, punycode.
func NormaliseHost(host string) (string, error) {
	return normalise(host)
}

func normalise(s string) (string, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return "", fmt.Errorf("vhost: empty host")
	}

	// Strip a port. Done by hand rather than with net.SplitHostPort because the
	// input usually has no port at all, and because a bracketed IPv6 literal
	// must survive to be rejected below rather than parsed as a domain.
	if !strings.HasPrefix(s, "[") {
		if idx := strings.LastIndexByte(s, ':'); idx != -1 {
			if !strings.Contains(s[idx+1:], ":") && !strings.Contains(s[idx+1:], ".") {
				s = s[:idx]
			}
		}
	}

	s = strings.ToLower(s)
	// A fully qualified name may carry a trailing dot; the routing table does
	// not store one.
	s = strings.TrimSuffix(s, ".")

	if s == "" {
		return "", fmt.Errorf("vhost: host is empty after normalisation")
	}
	if s == CatchAll {
		return CatchAll, nil
	}

	// Convert any internationalised labels to punycode so a browser sending
	// xn--… and a config written in native script agree.
	converted, err := idna.Lookup.ToASCII(strings.ReplaceAll(s, Wildcard, "\x00"))
	if err != nil {
		// idna rejects the placeholder in some positions; fall back to
		// converting the non-wildcard labels individually.
		converted, err = convertLabels(s)
		if err != nil {
			return "", fmt.Errorf("vhost: %q is not a usable host name: %w", s, err)
		}
		return converted, nil
	}
	return strings.ReplaceAll(converted, "\x00", Wildcard), nil
}

// convertLabels punycodes each label except wildcards.
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

// splitHostLabels normalises a host and returns its labels in reverse order.
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

// reverse returns a reversed copy of parts.
func reverse(parts []string) []string {
	out := make([]string, len(parts))
	for i, p := range parts {
		out[len(parts)-1-i] = p
	}
	return out
}
