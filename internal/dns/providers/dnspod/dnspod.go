package dnspod

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/zoefix/openfrp/internal/dns"
	"github.com/zoefix/openfrp/pkg/cloudapi"
	"github.com/zoefix/openfrp/pkg/schema"
)

const (
	providerKey = "dnspod"
	endpoint    = "dnspod.tencentcloudapi.com"
	service     = "dnspod"
	apiVersion  = "2021-03-23"
)

func init() {
	dns.Register(dns.Descriptor{
		Key:   providerKey,
		Label: "腾讯云 DNSPod",
		Form: schema.Form{Fields: []schema.Field{
			{Name: "secret_id", Label: "SecretId", Kind: schema.KindText, Required: true},
			{Name: "secret_key", Label: "SecretKey",
				Kind: schema.KindPassword, Required: true, Secret: true},
		}},
		Capabilities: dns.Capabilities{
			Remark:    dns.RemarkInline,
			Status:    true,
			Log:       true,
			Weight:    true,
			Paginated: true,
			MinTTL:    600,
		},
	}, func(values map[string]string) (dns.Provider, error) {
		return &provider{
			secretID:  values["secret_id"],
			secretKey: values["secret_key"],
			http:      dns.NewHTTPClient(),
		}, nil
	})
}

type provider struct {
	secretID  string
	secretKey string
	http      *dns.HTTPClient
}

func (p *provider) call(ctx context.Context, action string, payload map[string]any, out any) error {
	req := dns.Request{Method: http.MethodPost, URL: "https://" + endpoint + "/"}
	if err := req.JSONBody(payload); err != nil {
		return err
	}

	target, err := url.Parse(req.URL)
	if err != nil {
		return err
	}

	signing := &cloudapi.SigV4Request{
		Method: http.MethodPost,
		URL:    target,
		Headers: http.Header{
			"Content-Type": {"application/json; charset=utf-8"},
			"X-TC-Action":  {action},
			"X-TC-Version": {apiVersion},
		},
		Payload:   req.Body,
		AccessKey: p.secretID,
		SecretKey: p.secretKey,
		Service:   service,
	}
	if err := cloudapi.SignSigV4(cloudapi.TencentProfile, signing); err != nil {
		return err
	}

	req.Headers = map[string]string{}
	for name := range signing.Headers {
		req.Headers[name] = signing.Headers.Get(name)
	}

	var envelope struct {
		Response json.RawMessage `json:"Response"`
	}
	if err := p.http.Do(ctx, req, &envelope); err != nil {
		return err
	}

	var apiError struct {
		Error *struct {
			Code    string `json:"Code"`
			Message string `json:"Message"`
		} `json:"Error"`
	}
	if err := json.Unmarshal(envelope.Response, &apiError); err != nil {
		return fmt.Errorf("dnspod: decode response: %w", err)
	}
	if apiError.Error != nil {
		return fmt.Errorf("dnspod: %s: %s", apiError.Error.Code, apiError.Error.Message)
	}

	if out == nil {
		return nil
	}
	if err := json.Unmarshal(envelope.Response, out); err != nil {
		return fmt.Errorf("dnspod: decode response: %w", err)
	}
	return nil
}

func (p *provider) Check(ctx context.Context) error {
	if err := p.call(ctx, "DescribeDomainList", map[string]any{"Limit": 1}, nil); err != nil {
		return fmt.Errorf("dnspod: credentials rejected: %w", err)
	}
	return nil
}

func (p *provider) Capabilities() dns.Capabilities {
	desc, _ := dns.Describe(providerKey)
	return desc.Capabilities
}

func (p *provider) ListDomains(ctx context.Context, opts dns.ListOptions) ([]dns.Domain, error) {
	opts = opts.Normalise()

	payload := map[string]any{
		"Offset": (opts.Page - 1) * opts.PageSize,
		"Limit":  opts.PageSize,
	}
	if opts.Keyword != "" {
		payload["Keyword"] = opts.Keyword
	}

	var resp struct {
		DomainList []struct {
			DomainID    uint64 `json:"DomainId"`
			Name        string `json:"Name"`
			RecordCount int    `json:"RecordCount"`
		} `json:"DomainList"`
	}
	if err := p.call(ctx, "DescribeDomainList", payload, &resp); err != nil {
		return nil, err
	}

	out := make([]dns.Domain, 0, len(resp.DomainList))
	for _, d := range resp.DomainList {
		out = append(out, dns.Domain{
			ID:          strconv.FormatUint(d.DomainID, 10),
			Name:        d.Name,
			RecordCount: d.RecordCount,
		})
	}
	return out, nil
}

func (p *provider) ListRecords(ctx context.Context, zone string, opts dns.ListOptions) ([]dns.Record, error) {
	opts = opts.Normalise()

	payload := map[string]any{
		"Domain": zone,
		"Offset": (opts.Page - 1) * opts.PageSize,
		"Limit":  opts.PageSize,
	}
	if opts.Keyword != "" {
		payload["Keyword"] = opts.Keyword
	}
	if opts.Type != "" {
		payload["RecordType"] = string(opts.Type)
	}

	var resp struct {
		RecordList []struct {
			RecordID uint64 `json:"RecordId"`
			Name     string `json:"Name"`
			Type     string `json:"Type"`
			Value    string `json:"Value"`
			TTL      int    `json:"TTL"`
			MX       int    `json:"MX"`
			Line     string `json:"Line"`
			LineID   string `json:"LineId"`
			Status   string `json:"Status"`
			Remark   string `json:"Remark"`
			Weight   *int   `json:"Weight"`
		} `json:"RecordList"`
	}
	if err := p.call(ctx, "DescribeRecordList", payload, &resp); err != nil {

		if strings.Contains(err.Error(), "ResourceNotFound") {
			return nil, nil
		}
		return nil, err
	}

	out := make([]dns.Record, 0, len(resp.RecordList))
	for _, r := range resp.RecordList {
		weight := 0
		if r.Weight != nil {
			weight = *r.Weight
		}
		out = append(out, dns.Record{
			ID:       strconv.FormatUint(r.RecordID, 10),
			Name:     r.Name,
			Type:     dns.RecordType(r.Type),
			Value:    r.Value,
			TTL:      r.TTL,
			Priority: r.MX,
			Weight:   weight,
			Line:     dns.LineFromProvider(providerKey, r.LineID),
			Remark:   r.Remark,
			Enabled:  r.Status == "ENABLE",
		})
	}
	return out, nil
}

func (p *provider) recordPayload(zone string, record dns.Record) (map[string]any, error) {
	if err := record.Validate(); err != nil {
		return nil, err
	}

	line, ok := dns.ProviderLine(providerKey, record.Line)
	if !ok {
		return nil, fmt.Errorf("dnspod: does not support the %s line", record.Line.Label())
	}

	payload := map[string]any{
		"Domain":     zone,
		"SubDomain":  record.Name,
		"RecordType": string(record.Type),
		"Value":      record.Value,
		"RecordLine": lineName(record.Line),
		"LineId":     line,
	}
	if record.TTL > 0 {
		payload["TTL"] = record.TTL
	}
	if record.Type == dns.TypeMX {
		payload["MX"] = record.Priority
	}
	if record.Remark != "" {
		payload["Remark"] = record.Remark
	}
	return payload, nil
}

func lineName(line dns.Line) string {
	switch line {
	case dns.LineTelecom:
		return "电信"
	case dns.LineUnicom:
		return "联通"
	case dns.LineMobile:
		return "移动"
	case dns.LineOverseas:
		return "境外"
	default:
		return "默认"
	}
}

func (p *provider) AddRecord(ctx context.Context, zone string, record dns.Record) (string, error) {
	payload, err := p.recordPayload(zone, record)
	if err != nil {
		return "", err
	}

	var resp struct {
		RecordID uint64 `json:"RecordId"`
	}
	if err := p.call(ctx, "CreateRecord", payload, &resp); err != nil {
		return "", err
	}
	return strconv.FormatUint(resp.RecordID, 10), nil
}

func (p *provider) UpdateRecord(ctx context.Context, zone string, record dns.Record) error {
	if record.ID == "" {
		return fmt.Errorf("dnspod: cannot update a record without an ID")
	}
	payload, err := p.recordPayload(zone, record)
	if err != nil {
		return err
	}
	id, err := strconv.ParseUint(record.ID, 10, 64)
	if err != nil {
		return fmt.Errorf("dnspod: invalid record id %q", record.ID)
	}
	payload["RecordId"] = id

	return p.call(ctx, "ModifyRecord", payload, nil)
}

func (p *provider) DeleteRecord(ctx context.Context, zone, recordID string) error {
	id, err := strconv.ParseUint(recordID, 10, 64)
	if err != nil {
		return fmt.Errorf("dnspod: invalid record id %q", recordID)
	}
	return p.call(ctx, "DeleteRecord", map[string]any{"Domain": zone, "RecordId": id}, nil)
}

func (p *provider) SetRecordStatus(ctx context.Context, zone, recordID string, enabled bool) error {
	id, err := strconv.ParseUint(recordID, 10, 64)
	if err != nil {
		return fmt.Errorf("dnspod: invalid record id %q", recordID)
	}
	status := "DISABLE"
	if enabled {
		status = "ENABLE"
	}
	return p.call(ctx, "ModifyRecordStatus",
		map[string]any{"Domain": zone, "RecordId": id, "Status": status}, nil)
}

func (p *provider) AddDomain(ctx context.Context, zone string) error {
	return p.call(ctx, "CreateDomain", map[string]any{"Domain": zone}, nil)
}

var (
	_ dns.Provider     = (*provider)(nil)
	_ dns.StatusSetter = (*provider)(nil)
	_ dns.ZoneCreator  = (*provider)(nil)
)
