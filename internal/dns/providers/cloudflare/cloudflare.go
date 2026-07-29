package cloudflare

import (
	"context"
	"fmt"
	"net/url"
	"strconv"
	"strings"

	"github.com/zoefix/openfrp/internal/dns"
	"github.com/zoefix/openfrp/pkg/schema"
)

const (
	providerKey = "cloudflare"
	apiBase     = "https://api.cloudflare.com/client/v4"
)

func init() {
	dns.Register(dns.Descriptor{
		Key:   providerKey,
		Label: "Cloudflare",
		Form: schema.Form{Fields: []schema.Field{
			{Name: "auth", Label: "Authentication", Kind: schema.KindSelect, Required: true,
				Default: "token",
				Options: []schema.Option{
					{Value: "token", Label: "API Token",
						Description: "Scoped to specific zones. Strongly preferred."},
					{Value: "key", Label: "Global API Key",
						Description: "Full account access. Use only if a token will not do."},
				}},
			{Name: "api_token", Label: "API Token", Kind: schema.KindPassword,
				Required: true, Secret: true, ShowIf: "auth == 'token'",
				Help: "Needs Zone:Read and DNS:Edit on the zones you will manage."},
			{Name: "email", Label: "Account email", Kind: schema.KindText,
				Required: true, ShowIf: "auth == 'key'"},
			{Name: "api_key", Label: "Global API Key", Kind: schema.KindPassword,
				Required: true, Secret: true, ShowIf: "auth == 'key'"},
		}},
		Capabilities: dns.Capabilities{
			Remark: dns.RemarkInline,

			Status:    false,
			Paginated: true,
			Proxy:     true,
			MinTTL:    60,
		},
	}, func(values map[string]string) (dns.Provider, error) {
		return &provider{
			auth:   values["auth"],
			token:  values["api_token"],
			email:  values["email"],
			apiKey: values["api_key"],
			http:   dns.NewHTTPClient(),
		}, nil
	})
}

type provider struct {
	auth   string
	token  string
	email  string
	apiKey string
	http   *dns.HTTPClient

	zoneIDs map[string]string
}

func (p *provider) headers() map[string]string {
	if p.auth == "key" {
		return map[string]string{
			"X-Auth-Email": p.email,
			"X-Auth-Key":   p.apiKey,
		}
	}
	return map[string]string{"Authorization": "Bearer " + p.token}
}

type envelope struct {
	Success bool `json:"success"`
	Errors  []struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	} `json:"errors"`
}

func (e envelope) err() error {
	if e.Success {
		return nil
	}
	if len(e.Errors) == 0 {
		return fmt.Errorf("cloudflare: request failed with no reason given")
	}
	messages := make([]string, 0, len(e.Errors))
	for _, item := range e.Errors {
		messages = append(messages, fmt.Sprintf("%d: %s", item.Code, item.Message))
	}
	return fmt.Errorf("cloudflare: %s", strings.Join(messages, "; "))
}

func (p *provider) Check(ctx context.Context) error {
	var resp struct {
		envelope
	}
	req := dns.Request{URL: apiBase + "/zones?per_page=1", Headers: p.headers()}
	if err := p.http.Do(ctx, req, &resp); err != nil {
		return fmt.Errorf("cloudflare: credentials rejected: %w", err)
	}
	return resp.err()
}

func (p *provider) Capabilities() dns.Capabilities {
	desc, _ := dns.Describe(providerKey)
	return desc.Capabilities
}

func (p *provider) ListDomains(ctx context.Context, opts dns.ListOptions) ([]dns.Domain, error) {
	opts = opts.Normalise()

	var resp struct {
		envelope
		Result []struct {
			ID   string `json:"id"`
			Name string `json:"name"`
		} `json:"result"`
	}

	query := url.Values{
		"page":     {strconv.Itoa(opts.Page)},
		"per_page": {strconv.Itoa(opts.PageSize)},
	}
	if opts.Keyword != "" {
		query.Set("name", "contains:"+opts.Keyword)
	}

	req := dns.Request{URL: apiBase + "/zones?" + query.Encode(), Headers: p.headers()}
	if err := p.http.Do(ctx, req, &resp); err != nil {
		return nil, err
	}
	if err := resp.err(); err != nil {
		return nil, err
	}

	out := make([]dns.Domain, 0, len(resp.Result))
	for _, zone := range resp.Result {
		p.cacheZone(zone.Name, zone.ID)
		out = append(out, dns.Domain{ID: zone.ID, Name: zone.Name})
	}
	return out, nil
}

func (p *provider) cacheZone(name, id string) {
	if p.zoneIDs == nil {
		p.zoneIDs = map[string]string{}
	}
	p.zoneIDs[strings.ToLower(name)] = id
}

func (p *provider) zoneID(ctx context.Context, zone string) (string, error) {
	zone = strings.ToLower(strings.TrimSuffix(zone, "."))
	if id, cached := p.zoneIDs[zone]; cached {
		return id, nil
	}

	var resp struct {
		envelope
		Result []struct {
			ID   string `json:"id"`
			Name string `json:"name"`
		} `json:"result"`
	}

	req := dns.Request{
		URL:     apiBase + "/zones?name=" + url.QueryEscape(zone),
		Headers: p.headers(),
	}
	if err := p.http.Do(ctx, req, &resp); err != nil {
		return "", err
	}
	if err := resp.err(); err != nil {
		return "", err
	}
	if len(resp.Result) == 0 {
		return "", fmt.Errorf("cloudflare: zone %q is not in this account", zone)
	}

	p.cacheZone(resp.Result[0].Name, resp.Result[0].ID)
	return resp.Result[0].ID, nil
}

func (p *provider) ListRecords(ctx context.Context, zone string, opts dns.ListOptions) ([]dns.Record, error) {
	opts = opts.Normalise()

	id, err := p.zoneID(ctx, zone)
	if err != nil {
		return nil, err
	}

	var resp struct {
		envelope
		Result []struct {
			ID       string `json:"id"`
			Name     string `json:"name"`
			Type     string `json:"type"`
			Content  string `json:"content"`
			TTL      int    `json:"ttl"`
			Priority int    `json:"priority"`
			Comment  string `json:"comment"`
			Proxied  bool   `json:"proxied"`
		} `json:"result"`
	}

	query := url.Values{
		"page":     {strconv.Itoa(opts.Page)},
		"per_page": {strconv.Itoa(opts.PageSize)},
	}
	if opts.Type != "" {
		query.Set("type", string(opts.Type))
	}
	if opts.Keyword != "" {
		query.Set("name", "contains:"+opts.Keyword)
	}

	req := dns.Request{
		URL:     fmt.Sprintf("%s/zones/%s/dns_records?%s", apiBase, id, query.Encode()),
		Headers: p.headers(),
	}
	if err := p.http.Do(ctx, req, &resp); err != nil {
		return nil, err
	}
	if err := resp.err(); err != nil {
		return nil, err
	}

	out := make([]dns.Record, 0, len(resp.Result))
	for _, r := range resp.Result {

		proxied := r.Proxied

		out = append(out, dns.Record{
			ID:       r.ID,
			Name:     dns.NormaliseName(r.Name, zone),
			Type:     dns.RecordType(r.Type),
			Value:    r.Content,
			TTL:      r.TTL,
			Priority: r.Priority,
			Remark:   r.Comment,
			Line:     dns.LineDefault,
			Proxied:  &proxied,
			Enabled:  true,
		})
	}
	return out, nil
}

func (p *provider) recordBody(zone string, record dns.Record) (map[string]any, error) {
	if err := record.Validate(); err != nil {
		return nil, err
	}
	if record.Line != "" && record.Line != dns.LineDefault {
		return nil, fmt.Errorf("cloudflare: carrier lines are not supported")
	}

	name := record.Name
	if name == "@" || name == "" {
		name = zone
	} else if !strings.HasSuffix(name, zone) {
		name = name + "." + zone
	}

	body := map[string]any{
		"type":    string(record.Type),
		"name":    name,
		"content": record.Value,
	}

	if record.TTL >= 60 {
		body["ttl"] = record.TTL
	} else {
		body["ttl"] = 1
	}
	if record.Type == dns.TypeMX {
		body["priority"] = record.Priority
	}
	if record.Remark != "" {
		body["comment"] = record.Remark
	}

	if proxiable(record.Type) {
		body["proxied"] = record.Proxied != nil && *record.Proxied
	}
	return body, nil
}

func proxiable(kind dns.RecordType) bool {
	switch kind {
	case dns.TypeA, dns.TypeAAAA, dns.TypeCNAME:
		return true
	default:
		return false
	}
}

func (p *provider) AddRecord(ctx context.Context, zone string, record dns.Record) (string, error) {
	id, err := p.zoneID(ctx, zone)
	if err != nil {
		return "", err
	}
	body, err := p.recordBody(zone, record)
	if err != nil {
		return "", err
	}

	req := dns.Request{
		Method:  "POST",
		URL:     fmt.Sprintf("%s/zones/%s/dns_records", apiBase, id),
		Headers: p.headers(),
	}
	if err := req.JSONBody(body); err != nil {
		return "", err
	}

	var resp struct {
		envelope
		Result struct {
			ID string `json:"id"`
		} `json:"result"`
	}
	if err := p.http.Do(ctx, req, &resp); err != nil {
		return "", err
	}
	if err := resp.err(); err != nil {
		return "", err
	}
	return resp.Result.ID, nil
}

func (p *provider) UpdateRecord(ctx context.Context, zone string, record dns.Record) error {
	if record.ID == "" {
		return fmt.Errorf("cloudflare: cannot update a record without an ID")
	}
	id, err := p.zoneID(ctx, zone)
	if err != nil {
		return err
	}

	if record.Proxied == nil && proxiable(record.Type) {
		if current, err := p.recordByID(ctx, id, record.ID); err == nil {
			record.Proxied = &current
		}
	}

	body, err := p.recordBody(zone, record)
	if err != nil {
		return err
	}

	req := dns.Request{
		Method:  "PUT",
		URL:     fmt.Sprintf("%s/zones/%s/dns_records/%s", apiBase, id, record.ID),
		Headers: p.headers(),
	}
	if err := req.JSONBody(body); err != nil {
		return err
	}

	var resp struct{ envelope }
	if err := p.http.Do(ctx, req, &resp); err != nil {
		return err
	}
	return resp.err()
}

func (p *provider) DeleteRecord(ctx context.Context, zone, recordID string) error {
	id, err := p.zoneID(ctx, zone)
	if err != nil {
		return err
	}

	req := dns.Request{
		Method:  "DELETE",
		URL:     fmt.Sprintf("%s/zones/%s/dns_records/%s", apiBase, id, recordID),
		Headers: p.headers(),
	}

	var resp struct{ envelope }
	if err := p.http.Do(ctx, req, &resp); err != nil {
		return err
	}
	return resp.err()
}

var _ dns.Provider = (*provider)(nil)

func (p *provider) recordByID(ctx context.Context, zoneID, recordID string) (bool, error) {
	var resp struct {
		envelope
		Result struct {
			Proxied bool `json:"proxied"`
		} `json:"result"`
	}

	req := dns.Request{
		Method:  "GET",
		URL:     fmt.Sprintf("%s/zones/%s/dns_records/%s", apiBase, zoneID, recordID),
		Headers: p.headers(),
	}
	if err := p.http.Do(ctx, req, &resp); err != nil {
		return false, err
	}
	if err := resp.err(); err != nil {
		return false, err
	}
	return resp.Result.Proxied, nil
}
