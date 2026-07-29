package aliyun

import (
	"context"
	"fmt"
	"strconv"

	"github.com/zoefix/openfrp/internal/dns"
	"github.com/zoefix/openfrp/pkg/cloudapi"
	"github.com/zoefix/openfrp/pkg/schema"
)

const (
	providerKey = "aliyun"
	endpoint    = "alidns.aliyuncs.com"
	apiVersion  = "2015-01-09"
)

func init() {
	dns.Register(dns.Descriptor{
		Key:   providerKey,
		Label: "阿里云 DNS",
		Form: schema.Form{Fields: []schema.Field{
			{Name: "access_key_id", Label: "AccessKey ID", Kind: schema.KindText, Required: true},
			{Name: "access_key_secret", Label: "AccessKey Secret",
				Kind: schema.KindPassword, Required: true, Secret: true},
		}},
		Capabilities: dns.Capabilities{
			Remark:    dns.RemarkSeparate,
			Status:    true,
			Log:       true,
			Weight:    true,
			Redirect:  true,
			Paginated: true,
			MinTTL:    600,
		},
	}, func(values map[string]string) (dns.Provider, error) {
		return &provider{
			keyID:  values["access_key_id"],
			secret: values["access_key_secret"],
			http:   dns.NewHTTPClient(),
		}, nil
	})
}

type provider struct {
	keyID  string
	secret string
	http   *dns.HTTPClient
}

func (p *provider) call(ctx context.Context, action string, params map[string]string, out any) error {
	req := cloudapi.AliyunRPCRequest{
		Endpoint:        endpoint,
		Action:          action,
		Version:         apiVersion,
		Params:          params,
		AccessKeyID:     p.keyID,
		AccessKeySecret: p.secret,
	}

	signed, err := req.SignedURL()
	if err != nil {
		return err
	}
	return p.http.Do(ctx, dns.Request{URL: signed}, out)
}

func (p *provider) Check(ctx context.Context) error {
	var resp struct {
		TotalCount int `json:"TotalCount"`
	}
	if err := p.call(ctx, "DescribeDomains", map[string]string{"PageSize": "1"}, &resp); err != nil {
		return fmt.Errorf("aliyun: credentials rejected: %w", err)
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
		Domains struct {
			Domain []struct {
				DomainName  string `json:"DomainName"`
				DomainID    string `json:"DomainId"`
				RecordCount int    `json:"RecordCount"`
			} `json:"Domain"`
		} `json:"Domains"`
	}

	params := map[string]string{
		"PageNumber": strconv.Itoa(opts.Page),
		"PageSize":   strconv.Itoa(opts.PageSize),
	}
	if opts.Keyword != "" {
		params["KeyWord"] = opts.Keyword
	}

	if err := p.call(ctx, "DescribeDomains", params, &resp); err != nil {
		return nil, err
	}

	out := make([]dns.Domain, 0, len(resp.Domains.Domain))
	for _, d := range resp.Domains.Domain {
		out = append(out, dns.Domain{ID: d.DomainID, Name: d.DomainName, RecordCount: d.RecordCount})
	}
	return out, nil
}

func (p *provider) ListRecords(ctx context.Context, zone string, opts dns.ListOptions) ([]dns.Record, error) {
	opts = opts.Normalise()

	var resp struct {
		DomainRecords struct {
			Record []struct {
				RecordID string `json:"RecordId"`
				RR       string `json:"RR"`
				Type     string `json:"Type"`
				Value    string `json:"Value"`
				TTL      int    `json:"TTL"`
				Priority int    `json:"Priority"`
				Line     string `json:"Line"`
				Status   string `json:"Status"`
				Remark   string `json:"Remark"`
				Weight   int    `json:"Weight"`
			} `json:"Record"`
		} `json:"DomainRecords"`
	}

	params := map[string]string{
		"DomainName": zone,
		"PageNumber": strconv.Itoa(opts.Page),
		"PageSize":   strconv.Itoa(opts.PageSize),
	}
	if opts.Keyword != "" {
		params["KeyWord"] = opts.Keyword
	}
	if opts.Type != "" {
		params["TypeKeyWord"] = string(opts.Type)
	}

	if err := p.call(ctx, "DescribeDomainRecords", params, &resp); err != nil {
		return nil, err
	}

	out := make([]dns.Record, 0, len(resp.DomainRecords.Record))
	for _, r := range resp.DomainRecords.Record {
		out = append(out, dns.Record{
			ID:       r.RecordID,
			Name:     r.RR,
			Type:     dns.RecordType(r.Type),
			Value:    r.Value,
			TTL:      r.TTL,
			Priority: r.Priority,
			Weight:   r.Weight,
			Line:     dns.LineFromProvider(providerKey, r.Line),
			Remark:   r.Remark,

			Enabled: r.Status != "DISABLE",
		})
	}
	return out, nil
}

func (p *provider) recordParams(record dns.Record) (map[string]string, error) {
	if err := record.Validate(); err != nil {
		return nil, err
	}

	line, ok := dns.ProviderLine(providerKey, record.Line)
	if !ok {
		return nil, fmt.Errorf("aliyun: does not support the %s line", record.Line.Label())
	}

	params := map[string]string{
		"RR":    record.Name,
		"Type":  string(record.Type),
		"Value": record.Value,
		"Line":  line,
	}
	if record.TTL > 0 {
		params["TTL"] = strconv.Itoa(record.TTL)
	}
	if record.Type == dns.TypeMX {
		params["Priority"] = strconv.Itoa(record.Priority)
	}
	return params, nil
}

func (p *provider) AddRecord(ctx context.Context, zone string, record dns.Record) (string, error) {
	params, err := p.recordParams(record)
	if err != nil {
		return "", err
	}
	params["DomainName"] = zone

	var resp struct {
		RecordID string `json:"RecordId"`
	}
	if err := p.call(ctx, "AddDomainRecord", params, &resp); err != nil {
		return "", err
	}

	if record.Remark != "" && resp.RecordID != "" {
		p.SetRecordRemark(ctx, zone, resp.RecordID, record.Remark)
	}
	return resp.RecordID, nil
}

func (p *provider) UpdateRecord(ctx context.Context, zone string, record dns.Record) error {
	if record.ID == "" {
		return fmt.Errorf("aliyun: cannot update a record without an ID")
	}

	params, err := p.recordParams(record)
	if err != nil {
		return err
	}
	params["RecordId"] = record.ID

	if err := p.call(ctx, "UpdateDomainRecord", params, nil); err != nil {
		return err
	}
	if record.Remark != "" {
		p.SetRecordRemark(ctx, zone, record.ID, record.Remark)
	}
	return nil
}

func (p *provider) DeleteRecord(ctx context.Context, _ string, id string) error {
	return p.call(ctx, "DeleteDomainRecord", map[string]string{"RecordId": id}, nil)
}

func (p *provider) SetRecordStatus(ctx context.Context, _ string, id string, enabled bool) error {
	status := "Disable"
	if enabled {
		status = "Enable"
	}
	return p.call(ctx, "SetDomainRecordStatus",
		map[string]string{"RecordId": id, "Status": status}, nil)
}

func (p *provider) SetRecordRemark(ctx context.Context, _ string, id, remark string) error {
	return p.call(ctx, "UpdateDomainRecordRemark",
		map[string]string{"RecordId": id, "Remark": remark}, nil)
}

func (p *provider) AddDomain(ctx context.Context, zone string) error {
	return p.call(ctx, "AddDomain", map[string]string{"DomainName": zone}, nil)
}

var (
	_ dns.Provider     = (*provider)(nil)
	_ dns.StatusSetter = (*provider)(nil)
	_ dns.RemarkSetter = (*provider)(nil)
	_ dns.ZoneCreator  = (*provider)(nil)
)
