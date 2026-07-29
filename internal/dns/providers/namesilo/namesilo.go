package namesilo

import (
	"context"
	"encoding/xml"
	"fmt"
	"net/url"
	"strconv"
	"strings"

	"github.com/zoefix/openfrp/internal/dns"
	"github.com/zoefix/openfrp/pkg/schema"
)

const (
	providerKey = "namesilo"
	apiBase     = "https://www.namesilo.com/api"
)

func init() {
	dns.Register(dns.Descriptor{
		Key:   providerKey,
		Label: "NameSilo",
		Form: schema.Form{Fields: []schema.Field{
			{Name: "api_key", Label: "API Key", Kind: schema.KindPassword,
				Required: true, Secret: true,
				Help: "Generate one under Account Options, API Manager."},
		}},
		Capabilities: dns.Capabilities{
			Remark: dns.RemarkUnsupported,
			Status: false,

			Paginated: false,
			MinTTL:    3600,
		},
	}, func(values map[string]string) (dns.Provider, error) {
		return &provider{apiKey: values["api_key"], http: dns.NewHTTPClient()}, nil
	})
}

type provider struct {
	apiKey string
	http   *dns.HTTPClient
}

type reply struct {
	XMLName xml.Name `xml:"namesilo"`
	Reply   struct {
		Code   int    `xml:"code"`
		Detail string `xml:"detail"`

		Domains struct {
			Domain []string `xml:"domain"`
		} `xml:"domains"`

		ResourceRecord []struct {
			RecordID string `xml:"record_id"`
			Type     string `xml:"type"`
			Host     string `xml:"host"`
			Value    string `xml:"value"`
			TTL      int    `xml:"ttl"`
			Distance int    `xml:"distance"`
		} `xml:"resource_record"`

		RecordID string `xml:"record_id"`
	} `xml:"reply"`
}

func (r reply) ok() error {
	if r.Reply.Code == 300 {
		return nil
	}
	return fmt.Errorf("namesilo: %d: %s", r.Reply.Code, r.Reply.Detail)
}

func (p *provider) call(ctx context.Context, operation string, params url.Values) (*reply, error) {
	values := url.Values{
		"version": {"1"},
		"type":    {"xml"},
		"key":     {p.apiKey},
	}
	for key, items := range params {
		for _, item := range items {
			values.Add(key, item)
		}
	}

	var raw xmlBody
	req := dns.Request{URL: fmt.Sprintf("%s/%s?%s", apiBase, operation, values.Encode())}
	if err := p.http.Do(ctx, req, &raw); err != nil {
		return nil, err
	}
	return raw.parsed, nil
}

type xmlBody struct{ parsed *reply }

func (b *xmlBody) UnmarshalJSON(data []byte) error {
	var decoded reply
	if err := xml.Unmarshal(data, &decoded); err != nil {
		return fmt.Errorf("namesilo: parse xml response: %w", err)
	}
	b.parsed = &decoded
	return nil
}

func (p *provider) Check(ctx context.Context) error {
	resp, err := p.call(ctx, "listDomains", nil)
	if err != nil {
		return fmt.Errorf("namesilo: credentials rejected: %w", err)
	}
	return resp.ok()
}

func (p *provider) Capabilities() dns.Capabilities {
	desc, _ := dns.Describe(providerKey)
	return desc.Capabilities
}

func (p *provider) ListDomains(ctx context.Context, _ dns.ListOptions) ([]dns.Domain, error) {
	resp, err := p.call(ctx, "listDomains", nil)
	if err != nil {
		return nil, err
	}
	if err := resp.ok(); err != nil {
		return nil, err
	}

	out := make([]dns.Domain, 0, len(resp.Reply.Domains.Domain))
	for _, name := range resp.Reply.Domains.Domain {
		out = append(out, dns.Domain{Name: name})
	}
	return out, nil
}

func (p *provider) ListRecords(ctx context.Context, zone string, _ dns.ListOptions) ([]dns.Record, error) {
	resp, err := p.call(ctx, "dnsListRecords", url.Values{"domain": {zone}})
	if err != nil {
		return nil, err
	}
	if err := resp.ok(); err != nil {
		return nil, err
	}

	out := make([]dns.Record, 0, len(resp.Reply.ResourceRecord))
	for _, r := range resp.Reply.ResourceRecord {
		out = append(out, dns.Record{
			ID:       r.RecordID,
			Name:     dns.NormaliseName(r.Host, zone),
			Type:     dns.RecordType(strings.ToUpper(r.Type)),
			Value:    r.Value,
			TTL:      r.TTL,
			Priority: r.Distance,
			Line:     dns.LineDefault,
			Enabled:  true,
		})
	}
	return out, nil
}

func (p *provider) recordParams(zone string, record dns.Record) (url.Values, error) {
	if err := record.Validate(); err != nil {
		return nil, err
	}
	if record.Line != "" && record.Line != dns.LineDefault {
		return nil, fmt.Errorf("namesilo: carrier lines are not supported")
	}

	ttl := record.TTL
	if ttl < 3600 {

		ttl = 3600
	}

	host := record.Name
	if host == "@" {
		host = ""
	}

	params := url.Values{
		"domain":  {zone},
		"rrtype":  {string(record.Type)},
		"rrhost":  {host},
		"rrvalue": {record.Value},
		"rrttl":   {strconv.Itoa(ttl)},
	}
	if record.Type == dns.TypeMX {
		params.Set("rrdistance", strconv.Itoa(record.Priority))
	}
	return params, nil
}

func (p *provider) AddRecord(ctx context.Context, zone string, record dns.Record) (string, error) {
	params, err := p.recordParams(zone, record)
	if err != nil {
		return "", err
	}
	resp, err := p.call(ctx, "dnsAddRecord", params)
	if err != nil {
		return "", err
	}
	if err := resp.ok(); err != nil {
		return "", err
	}
	return resp.Reply.RecordID, nil
}

func (p *provider) UpdateRecord(ctx context.Context, zone string, record dns.Record) error {
	if record.ID == "" {
		return fmt.Errorf("namesilo: cannot update a record without an ID")
	}
	params, err := p.recordParams(zone, record)
	if err != nil {
		return err
	}
	params.Set("rrid", record.ID)

	resp, err := p.call(ctx, "dnsUpdateRecord", params)
	if err != nil {
		return err
	}
	return resp.ok()
}

func (p *provider) DeleteRecord(ctx context.Context, zone, recordID string) error {
	resp, err := p.call(ctx, "dnsDeleteRecord",
		url.Values{"domain": {zone}, "rrid": {recordID}})
	if err != nil {
		return err
	}
	return resp.ok()
}

var _ dns.Provider = (*provider)(nil)
