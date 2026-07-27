// Package dns manages authoritative DNS records across provider APIs.
//
// The interface here is wider than certificate issuance needs. ACME only ever
// wants to add and remove a TXT record, and libdns models exactly that — but a
// tunnel product also has to let an operator manage the records that point at
// their tunnels, including the carrier-line splits that are routine in China
// and that the narrow interfaces have no vocabulary for. So this is a
// management interface with an ACME adapter on top, not the reverse.
package dns

import (
	"fmt"
	"strings"
)

// RecordType is a DNS resource record type.
type RecordType string

const (
	TypeA     RecordType = "A"
	TypeAAAA  RecordType = "AAAA"
	TypeCNAME RecordType = "CNAME"
	TypeTXT   RecordType = "TXT"
	TypeMX    RecordType = "MX"
	TypeNS    RecordType = "NS"
	TypeSRV   RecordType = "SRV"
	TypeCAA   RecordType = "CAA"
	// TypeURL is a provider-specific forwarding record. Several Chinese
	// providers expose it; it is not a real DNS type.
	TypeURL RecordType = "URL"
)

// Record is one resource record.
type Record struct {
	// ID is the provider's identifier. Empty for a record not yet created.
	ID string `json:"id,omitempty"`

	// Name is the host portion relative to the zone: "www", or "@" for the
	// zone apex. Providers disagree about how to spell the apex, so the
	// normalisation happens in each provider rather than leaking here.
	Name string `json:"name"`

	Type  RecordType `json:"type"`
	Value string     `json:"value"`
	TTL   int        `json:"ttl,omitempty"`

	// Line is the carrier split this record answers for. Empty means default.
	Line Line `json:"line,omitempty"`

	// Priority applies to MX and SRV.
	Priority int `json:"priority,omitempty"`
	// Weight applies where the provider supports weighted answers.
	Weight int `json:"weight,omitempty"`

	Remark string `json:"remark,omitempty"`
	// Enabled is false for a record the provider is holding but not serving.
	Enabled bool `json:"enabled"`
}

// FQDN renders the record's fully qualified name within a zone.
func (r Record) FQDN(zone string) string {
	zone = strings.TrimSuffix(zone, ".")
	if r.Name == "" || r.Name == "@" {
		return zone
	}
	return r.Name + "." + zone
}

// Validate checks a record before it is sent to a provider.
func (r Record) Validate() error {
	if r.Type == "" {
		return fmt.Errorf("dns: record type is required")
	}
	if strings.TrimSpace(r.Value) == "" {
		return fmt.Errorf("dns: record value is required")
	}
	if r.TTL < 0 {
		return fmt.Errorf("dns: ttl must not be negative")
	}
	if r.Type == TypeMX && r.Priority < 0 {
		return fmt.Errorf("dns: MX priority must not be negative")
	}
	return nil
}

// Domain is a zone hosted at a provider.
type Domain struct {
	// ID is the provider's identifier, where it uses one.
	ID string `json:"id,omitempty"`
	// Name is the zone name without a trailing dot.
	Name string `json:"name"`
	// RecordCount is a hint for the UI; zero when the provider does not say.
	RecordCount int `json:"record_count,omitempty"`
}

// RemarkStyle describes how a provider handles per-record remarks.
type RemarkStyle int

const (
	// RemarkUnsupported means the provider has no remark field.
	RemarkUnsupported RemarkStyle = iota
	// RemarkSeparate means a remark is set by its own API call after the
	// record exists.
	RemarkSeparate
	// RemarkInline means a remark is a field of the record itself.
	RemarkInline
)

// Capabilities describes what a provider can actually do.
//
// The UI hides controls a provider does not support rather than offering them
// and failing at save time, so these have to be honest.
type Capabilities struct {
	Remark RemarkStyle `json:"remark"`
	// Status is per-record enable and pause.
	Status bool `json:"status"`
	// Log is a record change history.
	Log bool `json:"log"`
	// Weight is weighted answers.
	Weight bool `json:"weight"`
	// Redirect is URL forwarding.
	Redirect bool `json:"redirect"`
	// AddZone is creating a zone through the API.
	AddZone bool `json:"add_zone"`
	// Paginated is false when the provider returns everything at once and the
	// UI should paginate client-side.
	Paginated bool `json:"paginated"`
	// MinTTL is the smallest TTL the provider accepts.
	MinTTL int `json:"min_ttl"`
	// Lines are the carrier splits this provider offers.
	Lines []Line `json:"lines"`
}

// ListOptions narrows a listing.
type ListOptions struct {
	// Keyword filters by substring where the provider supports it.
	Keyword string
	// Type filters by record type.
	Type RecordType
	// Page is 1-based. Zero means the first page.
	Page int
	// PageSize is the provider's page size. Zero means the provider default.
	PageSize int
}

// Normalise fills in sane defaults.
func (o ListOptions) Normalise() ListOptions {
	if o.Page < 1 {
		o.Page = 1
	}
	if o.PageSize < 1 {
		o.PageSize = 100
	}
	return o
}
