package west

import (
	"context"
	"crypto/md5"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/zoefix/openfrp/internal/dns"
	"github.com/zoefix/openfrp/pkg/schema"
)

const (
	providerKey = "west"
	apiBase     = "https://api.west.cn/API/v2"
)

func init() {
	dns.Register(dns.Descriptor{
		Key:   providerKey,
		Label: "西部数码",
		Form: schema.Form{Fields: []schema.Field{
			{Name: "username", Label: "用户名", Kind: schema.KindText, Required: true},
			{Name: "api_password", Label: "API 密码",
				Kind: schema.KindPassword, Required: true, Secret: true,
				Help: "The API password from the console, not the account login password."},
		}},
		Capabilities: dns.Capabilities{
			Remark:    dns.RemarkUnsupported,
			Status:    true,
			Paginated: true,
			MinTTL:    60,
		},
	}, func(values map[string]string) (dns.Provider, error) {
		return &provider{
			username: values["username"],
			password: values["api_password"],
			http:     dns.NewHTTPClient(),
		}, nil
	})
}

type provider struct {
	username string
	password string
	http     *dns.HTTPClient
}

func (p *provider) authParams() url.Values {
	timestamp := strconv.FormatInt(time.Now().UnixMilli(), 10)
	sum := md5.Sum([]byte(p.username + p.password + timestamp))

	return url.Values{
		"username": {p.username},
		"time":     {timestamp},
		"token":    {hex.EncodeToString(sum[:])},
	}
}

type response struct {
	Result   int    `json:"result"`
	Msg      string `json:"msg"`
	ClientID string `json:"clientid"`
}

func (r response) ok() error {
	if r.Result == 200 {
		return nil
	}
	return fmt.Errorf("west: %d: %s", r.Result, r.Msg)
}

func (p *provider) call(ctx context.Context, path string, params url.Values, out any) error {
	values := p.authParams()
	for key, items := range params {
		for _, item := range items {
			values.Add(key, item)
		}
	}

	req := dns.Request{
		Method:  http.MethodPost,
		URL:     apiBase + path,
		Headers: map[string]string{"Content-Type": "application/x-www-form-urlencoded"},
		Body:    []byte(values.Encode()),
	}
	return p.http.Do(ctx, req, out)
}

func (p *provider) Check(ctx context.Context) error {
	var resp struct {
		response
	}
	params := url.Values{"act": {"getdomains"}, "limit": {"1"}, "page": {"1"}}
	if err := p.call(ctx, "/domain/", params, &resp); err != nil {
		return fmt.Errorf("west: credentials rejected: %w", err)
	}
	return resp.ok()
}

func (p *provider) Capabilities() dns.Capabilities {
	desc, _ := dns.Describe(providerKey)
	return desc.Capabilities
}

func (p *provider) ListDomains(ctx context.Context, opts dns.ListOptions) ([]dns.Domain, error) {
	opts = opts.Normalise()

	var resp struct {
		response
		Data struct {
			Items []struct {
				Domain string `json:"domain"`
			} `json:"items"`
		} `json:"data"`
	}

	params := url.Values{
		"act":   {"getdomains"},
		"page":  {strconv.Itoa(opts.Page)},
		"limit": {strconv.Itoa(opts.PageSize)},
	}
	if err := p.call(ctx, "/domain/", params, &resp); err != nil {
		return nil, err
	}
	if err := resp.ok(); err != nil {
		return nil, err
	}

	out := make([]dns.Domain, 0, len(resp.Data.Items))
	for _, item := range resp.Data.Items {
		out = append(out, dns.Domain{Name: item.Domain})
	}
	return out, nil
}

func (p *provider) ListRecords(ctx context.Context, zone string, opts dns.ListOptions) ([]dns.Record, error) {
	opts = opts.Normalise()

	var resp struct {
		response
		Data struct {
			Items []struct {
				ID    int    `json:"id"`
				Item  string `json:"item"`
				Type  string `json:"type"`
				Value string `json:"value"`
				TTL   int    `json:"ttl"`
				Level int    `json:"level"`
				Line  string `json:"line"`
				Pause int    `json:"pause"`
			} `json:"items"`
		} `json:"data"`
	}

	params := url.Values{
		"act":    {"getdnsrecord"},
		"domain": {zone},
		"page":   {strconv.Itoa(opts.Page)},
		"limit":  {strconv.Itoa(opts.PageSize)},
	}
	if err := p.call(ctx, "/domain/", params, &resp); err != nil {
		return nil, err
	}
	if err := resp.ok(); err != nil {
		return nil, err
	}

	out := make([]dns.Record, 0, len(resp.Data.Items))
	for _, item := range resp.Data.Items {
		out = append(out, dns.Record{
			ID:       strconv.Itoa(item.ID),
			Name:     item.Item,
			Type:     dns.RecordType(strings.ToUpper(item.Type)),
			Value:    item.Value,
			TTL:      item.TTL,
			Priority: item.Level,
			Line:     dns.LineFromProvider(providerKey, item.Line),

			Enabled: item.Pause != 1,
		})
	}
	return out, nil
}

func (p *provider) recordParams(zone string, record dns.Record) (url.Values, error) {
	if err := record.Validate(); err != nil {
		return nil, err
	}

	line, ok := dns.ProviderLine(providerKey, record.Line)
	if !ok {
		return nil, fmt.Errorf("west: does not support the %s line", record.Line.Label())
	}

	ttl := record.TTL
	if ttl <= 0 {
		ttl = 900
	}

	params := url.Values{
		"domain": {zone},
		"host":   {record.Name},
		"type":   {string(record.Type)},
		"value":  {record.Value},
		"ttl":    {strconv.Itoa(ttl)},
	}
	if line != "" {
		params.Set("line", line)
	}
	if record.Type == dns.TypeMX {
		params.Set("level", strconv.Itoa(record.Priority))
	}
	return params, nil
}

func (p *provider) AddRecord(ctx context.Context, zone string, record dns.Record) (string, error) {
	params, err := p.recordParams(zone, record)
	if err != nil {
		return "", err
	}
	params.Set("act", "adddnsrecord")

	var resp struct {
		response
		Data struct {
			ID int `json:"id"`
		} `json:"data"`
	}
	if err := p.call(ctx, "/domain/", params, &resp); err != nil {
		return "", err
	}
	if err := resp.ok(); err != nil {
		return "", err
	}
	return strconv.Itoa(resp.Data.ID), nil
}

func (p *provider) UpdateRecord(ctx context.Context, zone string, record dns.Record) error {
	if record.ID == "" {
		return fmt.Errorf("west: cannot update a record without an ID")
	}
	params, err := p.recordParams(zone, record)
	if err != nil {
		return err
	}
	params.Set("act", "moddnsrecord")
	params.Set("id", record.ID)

	var resp struct{ response }
	if err := p.call(ctx, "/domain/", params, &resp); err != nil {
		return err
	}
	return resp.ok()
}

func (p *provider) DeleteRecord(ctx context.Context, zone, recordID string) error {
	params := url.Values{
		"act":    {"deldnsrecord"},
		"domain": {zone},
		"id":     {recordID},
	}

	var resp struct{ response }
	if err := p.call(ctx, "/domain/", params, &resp); err != nil {
		return err
	}
	return resp.ok()
}

func (p *provider) SetRecordStatus(ctx context.Context, zone, recordID string, enabled bool) error {
	pause := "1"
	if enabled {
		pause = "0"
	}
	params := url.Values{
		"act":    {"pausednsrecord"},
		"domain": {zone},
		"id":     {recordID},
		"pause":  {pause},
	}

	var resp struct{ response }
	if err := p.call(ctx, "/domain/", params, &resp); err != nil {
		return err
	}
	return resp.ok()
}

var (
	_ dns.Provider     = (*provider)(nil)
	_ dns.StatusSetter = (*provider)(nil)
)
