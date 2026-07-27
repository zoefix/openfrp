// Package powerdns manages records in a self-hosted PowerDNS authoritative
// server.
package powerdns

import (
	"context"
	"fmt"
	"net/http"
	"strings"

	"github.com/zoefix/openfrp/internal/dns"
	"github.com/zoefix/openfrp/pkg/schema"
)

const providerKey = "powerdns"

func init() {
	dns.Register(dns.Descriptor{
		Key:   providerKey,
		Label: "PowerDNS",
		Form: schema.Form{Fields: []schema.Field{
			{Name: "url", Label: "API URL", Kind: schema.KindText, Required: true,
				Placeholder: "http://127.0.0.1:8081",
				Help:        "The base URL of the PowerDNS webserver, without /api."},
			{Name: "api_key", Label: "API Key", Kind: schema.KindPassword,
				Required: true, Secret: true},
			{Name: "server_id", Label: "Server ID", Kind: schema.KindText,
				Default: "localhost"},
		}},
		Capabilities: dns.Capabilities{
			Remark: dns.RemarkUnsupported,
			// PowerDNS has no notion of a disabled record set; it exists or it
			// does not.
			Status:    false,
			Paginated: false,
			MinTTL:    1,
			AddZone:   true,
		},
	}, func(values map[string]string) (dns.Provider, error) {
		serverID := values["server_id"]
		if serverID == "" {
			serverID = "localhost"
		}
		return &provider{
			base:     strings.TrimSuffix(values["url"], "/"),
			apiKey:   values["api_key"],
			serverID: serverID,
			http:     dns.NewHTTPClient(),
		}, nil
	})
}

type provider struct {
	base     string
	apiKey   string
	serverID string
	http     *dns.HTTPClient
}

func (p *provider) call(ctx context.Context, method, path string, body any, out any) error {
	req := dns.Request{
		Method:  method,
		URL:     fmt.Sprintf("%s/api/v1/servers/%s%s", p.base, p.serverID, path),
		Headers: map[string]string{"X-API-Key": p.apiKey},
	}
	if body != nil {
		if err := req.JSONBody(body); err != nil {
			return err
		}
		req.Headers["X-API-Key"] = p.apiKey
	}
	return p.http.Do(ctx, req, out)
}

func (p *provider) Check(ctx context.Context) error {
	var zones []struct{}
	if err := p.call(ctx, http.MethodGet, "/zones", nil, &zones); err != nil {
		return fmt.Errorf("powerdns: unreachable or key rejected: %w", err)
	}
	return nil
}

func (p *provider) Capabilities() dns.Capabilities {
	desc, _ := dns.Describe(providerKey)
	return desc.Capabilities
}

func (p *provider) ListDomains(ctx context.Context, _ dns.ListOptions) ([]dns.Domain, error) {
	var zones []struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	}
	if err := p.call(ctx, http.MethodGet, "/zones", nil, &zones); err != nil {
		return nil, err
	}

	out := make([]dns.Domain, 0, len(zones))
	for _, zone := range zones {
		out = append(out, dns.Domain{ID: zone.ID, Name: strings.TrimSuffix(zone.Name, ".")})
	}
	return out, nil
}

// canonical renders a zone the way PowerDNS names them, with a trailing dot.
func canonical(zone string) string {
	return strings.TrimSuffix(zone, ".") + "."
}

func (p *provider) ListRecords(ctx context.Context, zone string, _ dns.ListOptions) ([]dns.Record, error) {
	var payload struct {
		RRSets []struct {
			Name    string `json:"name"`
			Type    string `json:"type"`
			TTL     int    `json:"ttl"`
			Records []struct {
				Content  string `json:"content"`
				Disabled bool   `json:"disabled"`
			} `json:"records"`
		} `json:"rrsets"`
	}

	if err := p.call(ctx, http.MethodGet, "/zones/"+canonical(zone), nil, &payload); err != nil {
		return nil, err
	}

	// PowerDNS groups by name and type, like Huawei. Flatten so it looks the
	// same as every other provider; the index makes each row addressable.
	var out []dns.Record
	for _, set := range payload.RRSets {
		for index, record := range set.Records {
			out = append(out, dns.Record{
				ID:      fmt.Sprintf("%s|%s|%d", strings.TrimSuffix(set.Name, "."), set.Type, index),
				Name:    dns.NormaliseName(set.Name, zone),
				Type:    dns.RecordType(set.Type),
				Value:   strings.Trim(record.Content, `"`),
				TTL:     set.TTL,
				Line:    dns.LineDefault,
				Enabled: !record.Disabled,
			})
		}
	}
	return out, nil
}

// patch sends an rrset change. PowerDNS models create, update and delete as
// one PATCH with a changetype, so all three share this.
func (p *provider) patch(ctx context.Context, zone string, rrset map[string]any) error {
	body := map[string]any{"rrsets": []map[string]any{rrset}}
	return p.call(ctx, http.MethodPatch, "/zones/"+canonical(zone), body, nil)
}

func (p *provider) rrsetFor(zone string, record dns.Record) (map[string]any, error) {
	if err := record.Validate(); err != nil {
		return nil, err
	}
	if record.Line != "" && record.Line != dns.LineDefault {
		return nil, fmt.Errorf("powerdns: carrier lines are not supported")
	}

	ttl := record.TTL
	if ttl <= 0 {
		ttl = 300
	}

	content := record.Value
	if record.Type == dns.TypeTXT && !strings.HasPrefix(content, `"`) {
		// PowerDNS requires TXT content to be a quoted character-string.
		content = `"` + content + `"`
	}

	return map[string]any{
		"name":       canonical(record.FQDN(zone)),
		"type":       string(record.Type),
		"ttl":        ttl,
		"changetype": "REPLACE",
		"records": []map[string]any{
			{"content": content, "disabled": !record.Enabled},
		},
	}, nil
}

func (p *provider) AddRecord(ctx context.Context, zone string, record dns.Record) (string, error) {
	record.Enabled = true
	rrset, err := p.rrsetFor(zone, record)
	if err != nil {
		return "", err
	}
	if err := p.patch(ctx, zone, rrset); err != nil {
		return "", err
	}
	return fmt.Sprintf("%s|%s|0", record.FQDN(zone), record.Type), nil
}

func (p *provider) UpdateRecord(ctx context.Context, zone string, record dns.Record) error {
	record.Enabled = true
	rrset, err := p.rrsetFor(zone, record)
	if err != nil {
		return err
	}
	return p.patch(ctx, zone, rrset)
}

func (p *provider) DeleteRecord(ctx context.Context, zone, recordID string) error {
	parts := strings.Split(recordID, "|")
	if len(parts) < 2 {
		return fmt.Errorf("powerdns: malformed record id %q", recordID)
	}

	return p.patch(ctx, zone, map[string]any{
		"name":       canonical(parts[0]),
		"type":       parts[1],
		"changetype": "DELETE",
	})
}

func (p *provider) AddDomain(ctx context.Context, zone string) error {
	body := map[string]any{"name": canonical(zone), "kind": "Native"}
	return p.call(ctx, http.MethodPost, "/zones", body, nil)
}

var (
	_ dns.Provider    = (*provider)(nil)
	_ dns.ZoneCreator = (*provider)(nil)
)
