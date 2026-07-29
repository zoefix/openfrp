package huawei

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/zoefix/openfrp/internal/dns"
	"github.com/zoefix/openfrp/pkg/cloudapi"
	"github.com/zoefix/openfrp/pkg/schema"
)

const providerKey = "huawei"

func init() {
	dns.Register(dns.Descriptor{
		Key:   providerKey,
		Label: "华为云 DNS",
		Form: schema.Form{Fields: []schema.Field{
			{Name: "access_key_id", Label: "Access Key Id", Kind: schema.KindText, Required: true},
			{Name: "secret_access_key", Label: "Secret Access Key",
				Kind: schema.KindPassword, Required: true, Secret: true},
			{Name: "endpoint", Label: "Endpoint", Kind: schema.KindText,
				Default: "dns.myhuaweicloud.com",
				Help:    "Change only if your account uses a regional endpoint."},
		}},
		Capabilities: dns.Capabilities{
			Remark: dns.RemarkInline,
			Status: true,

			Paginated: true,
			MinTTL:    1,
		},
	}, func(values map[string]string) (dns.Provider, error) {
		endpoint := values["endpoint"]
		if endpoint == "" {
			endpoint = "dns.myhuaweicloud.com"
		}
		return &provider{
			keyID:    values["access_key_id"],
			secret:   values["secret_access_key"],
			endpoint: endpoint,
			http:     dns.NewHTTPClient(),
		}, nil
	})
}

type provider struct {
	keyID    string
	secret   string
	endpoint string
	http     *dns.HTTPClient

	zoneIDs map[string]string
}

func (p *provider) call(ctx context.Context, method, path string, query url.Values, body any, out any) error {
	rawURL := "https://" + p.endpoint + path
	if len(query) > 0 {
		rawURL += "?" + query.Encode()
	}

	req := dns.Request{Method: method, URL: rawURL}
	if body != nil {
		if err := req.JSONBody(body); err != nil {
			return err
		}
	}

	target, err := url.Parse(rawURL)
	if err != nil {
		return err
	}

	headers := http.Header{}
	if len(req.Body) > 0 {
		headers.Set("Content-Type", "application/json")
	}

	signing := &cloudapi.SigV4Request{
		Method:    method,
		URL:       target,
		Headers:   headers,
		Payload:   req.Body,
		AccessKey: p.keyID,
		SecretKey: p.secret,
	}
	if err := cloudapi.SignSigV4(cloudapi.HuaweiProfile, signing); err != nil {
		return err
	}

	req.Headers = map[string]string{}
	for name := range signing.Headers {
		req.Headers[name] = signing.Headers.Get(name)
	}

	return p.http.Do(ctx, req, out)
}

func (p *provider) Check(ctx context.Context) error {
	var resp struct{}
	if err := p.call(ctx, http.MethodGet, "/v2/zones",
		url.Values{"limit": {"1"}}, nil, &resp); err != nil {
		return fmt.Errorf("huawei: credentials rejected: %w", err)
	}
	return nil
}

func (p *provider) Capabilities() dns.Capabilities {
	desc, _ := dns.Describe(providerKey)
	return desc.Capabilities
}

func (p *provider) ListDomains(ctx context.Context, opts dns.ListOptions) ([]dns.Domain, error) {
	opts = opts.Normalise()

	var resp struct {
		Zones []struct {
			ID          string `json:"id"`
			Name        string `json:"name"`
			RecordCount int    `json:"record_num"`
		} `json:"zones"`
	}

	query := url.Values{
		"limit":  {strconv.Itoa(opts.PageSize)},
		"offset": {strconv.Itoa((opts.Page - 1) * opts.PageSize)},
	}
	if opts.Keyword != "" {
		query.Set("name", opts.Keyword)
	}

	if err := p.call(ctx, http.MethodGet, "/v2/zones", query, nil, &resp); err != nil {
		return nil, err
	}

	out := make([]dns.Domain, 0, len(resp.Zones))
	for _, zone := range resp.Zones {
		name := strings.TrimSuffix(zone.Name, ".")
		p.cacheZone(name, zone.ID)
		out = append(out, dns.Domain{ID: zone.ID, Name: name, RecordCount: zone.RecordCount})
	}
	return out, nil
}

func (p *provider) cacheZone(name, id string) {
	if p.zoneIDs == nil {
		p.zoneIDs = map[string]string{}
	}
	p.zoneIDs[strings.ToLower(strings.TrimSuffix(name, "."))] = id
}

func (p *provider) zoneID(ctx context.Context, zone string) (string, error) {
	key := strings.ToLower(strings.TrimSuffix(zone, "."))
	if id, cached := p.zoneIDs[key]; cached {
		return id, nil
	}

	var resp struct {
		Zones []struct {
			ID   string `json:"id"`
			Name string `json:"name"`
		} `json:"zones"`
	}
	query := url.Values{"name": {key + "."}}
	if err := p.call(ctx, http.MethodGet, "/v2/zones", query, nil, &resp); err != nil {
		return "", err
	}

	for _, candidate := range resp.Zones {
		if strings.TrimSuffix(strings.ToLower(candidate.Name), ".") == key {
			p.cacheZone(key, candidate.ID)
			return candidate.ID, nil
		}
	}
	return "", fmt.Errorf("huawei: zone %q is not in this account", zone)
}

func (p *provider) ListRecords(ctx context.Context, zone string, opts dns.ListOptions) ([]dns.Record, error) {
	opts = opts.Normalise()

	id, err := p.zoneID(ctx, zone)
	if err != nil {
		return nil, err
	}

	var resp struct {
		Recordsets []struct {
			ID          string   `json:"id"`
			Name        string   `json:"name"`
			Type        string   `json:"type"`
			TTL         int      `json:"ttl"`
			Records     []string `json:"records"`
			Line        string   `json:"line"`
			Status      string   `json:"status"`
			Description string   `json:"description"`
			Weight      int      `json:"weight"`
		} `json:"recordsets"`
	}

	query := url.Values{
		"limit":  {strconv.Itoa(opts.PageSize)},
		"offset": {strconv.Itoa((opts.Page - 1) * opts.PageSize)},
	}
	if opts.Type != "" {
		query.Set("type", string(opts.Type))
	}

	if err := p.call(ctx, http.MethodGet, "/v2.1/zones/"+id+"/recordsets", query, nil, &resp); err != nil {
		return nil, err
	}

	var out []dns.Record
	for _, set := range resp.Recordsets {
		for index, value := range set.Records {
			out = append(out, dns.Record{
				ID:      fmt.Sprintf("%s#%d", set.ID, index),
				Name:    dns.NormaliseName(set.Name, zone),
				Type:    dns.RecordType(set.Type),
				Value:   strings.Trim(value, `"`),
				TTL:     set.TTL,
				Weight:  set.Weight,
				Line:    dns.LineFromProvider(providerKey, set.Line),
				Remark:  set.Description,
				Enabled: set.Status != "DISABLE",
			})
		}
	}
	return out, nil
}

func setID(id string) string {
	if idx := strings.IndexByte(id, '#'); idx >= 0 {
		return id[:idx]
	}
	return id
}

func (p *provider) AddRecord(ctx context.Context, zone string, record dns.Record) (string, error) {
	if err := record.Validate(); err != nil {
		return "", err
	}
	id, err := p.zoneID(ctx, zone)
	if err != nil {
		return "", err
	}

	line, ok := dns.ProviderLine(providerKey, record.Line)
	if !ok {
		return "", fmt.Errorf("huawei: does not support the %s line", record.Line.Label())
	}

	ttl := record.TTL
	if ttl <= 0 {
		ttl = 300
	}

	body := map[string]any{
		"name":    record.FQDN(zone) + ".",
		"type":    string(record.Type),
		"ttl":     ttl,
		"records": []string{quoteForType(record)},
		"line":    line,
	}
	if record.Remark != "" {
		body["description"] = record.Remark
	}

	var resp struct {
		ID string `json:"id"`
	}
	if err := p.call(ctx, http.MethodPost,
		"/v2.1/zones/"+id+"/recordsets", nil, body, &resp); err != nil {
		return "", err
	}
	return resp.ID + "#0", nil
}

func quoteForType(record dns.Record) string {
	if record.Type != dns.TypeTXT {
		return record.Value
	}
	if strings.HasPrefix(record.Value, `"`) {
		return record.Value
	}
	return `"` + record.Value + `"`
}

func (p *provider) UpdateRecord(ctx context.Context, zone string, record dns.Record) error {
	if record.ID == "" {
		return fmt.Errorf("huawei: cannot update a record without an ID")
	}
	if err := record.Validate(); err != nil {
		return err
	}

	id, err := p.zoneID(ctx, zone)
	if err != nil {
		return err
	}

	ttl := record.TTL
	if ttl <= 0 {
		ttl = 300
	}

	body := map[string]any{
		"ttl":     ttl,
		"records": []string{quoteForType(record)},
	}
	if record.Remark != "" {
		body["description"] = record.Remark
	}

	return p.call(ctx, http.MethodPut,
		"/v2.1/zones/"+id+"/recordsets/"+setID(record.ID), nil, body, nil)
}

func (p *provider) DeleteRecord(ctx context.Context, zone, recordID string) error {
	id, err := p.zoneID(ctx, zone)
	if err != nil {
		return err
	}
	return p.call(ctx, http.MethodDelete,
		"/v2.1/zones/"+id+"/recordsets/"+setID(recordID), nil, nil, nil)
}

func (p *provider) SetRecordStatus(ctx context.Context, zone, recordID string, enabled bool) error {
	status := "DISABLE"
	if enabled {
		status = "ENABLE"
	}
	return p.call(ctx, http.MethodPut,
		"/v2.1/recordsets/"+setID(recordID)+"/statuses/set",
		nil, map[string]any{"status": status}, nil)
}

var (
	_ dns.Provider     = (*provider)(nil)
	_ dns.StatusSetter = (*provider)(nil)
)
