package dns

import (
	"fmt"
	"strings"
)

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

	TypeURL RecordType = "URL"
)

type Record struct {
	ID string `json:"id,omitempty"`

	Name string `json:"name"`

	Type  RecordType `json:"type"`
	Value string     `json:"value"`
	TTL   int        `json:"ttl,omitempty"`

	Line Line `json:"line,omitempty"`

	Priority int `json:"priority,omitempty"`

	Weight int `json:"weight,omitempty"`

	Remark string `json:"remark,omitempty"`

	Enabled bool `json:"enabled"`

	Proxied *bool `json:"proxied,omitempty"`
}

func (r Record) FQDN(zone string) string {
	zone = strings.TrimSuffix(zone, ".")
	if r.Name == "" || r.Name == "@" {
		return zone
	}
	return r.Name + "." + zone
}

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

type Domain struct {
	ID string `json:"id,omitempty"`

	Name string `json:"name"`

	RecordCount int `json:"record_count,omitempty"`
}

type RemarkStyle int

const (
	RemarkUnsupported RemarkStyle = iota

	RemarkSeparate

	RemarkInline
)

type Capabilities struct {
	Remark RemarkStyle `json:"remark"`

	Status bool `json:"status"`

	Log bool `json:"log"`

	Weight bool `json:"weight"`

	Redirect bool `json:"redirect"`

	Proxy bool `json:"proxy"`

	AddZone bool `json:"add_zone"`

	Paginated bool `json:"paginated"`

	MinTTL int `json:"min_ttl"`

	Lines []Line `json:"lines"`
}

type ListOptions struct {
	Keyword string

	Type RecordType

	Page int

	PageSize int
}

func (o ListOptions) Normalise() ListOptions {
	if o.Page < 1 {
		o.Page = 1
	}
	if o.PageSize < 1 {
		o.PageSize = 100
	}
	return o
}
