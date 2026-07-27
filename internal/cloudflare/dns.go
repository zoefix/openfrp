package cloudflare

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strings"
)

// zone is a Cloudflare zone.
type zone struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// record is one DNS record.
type record struct {
	ID      string `json:"id"`
	Type    string `json:"type"`
	Name    string `json:"name"`
	Content string `json:"content"`
	Proxied bool   `json:"proxied"`
}

// Zones lists the zones the token can edit.
func (c *Client) Zones(ctx context.Context) ([]string, error) {
	var resp struct {
		envelope
		Result []zone `json:"result"`
	}
	if err := c.do(ctx, http.MethodGet, "/zones?per_page=50", nil, &resp); err != nil {
		return nil, err
	}

	names := make([]string, 0, len(resp.Result))
	for _, z := range resp.Result {
		names = append(names, z.Name)
	}
	return names, nil
}

// zoneFor finds the zone a hostname belongs to.
//
// Matched by longest suffix rather than by asking for the name directly: a
// hostname is not a zone, and a.b.example.com lives in example.com, or in
// b.example.com if the account happens to hold that as a zone of its own. The
// longer match is the right one, because that is the zone whose records
// actually serve the name.
func (c *Client) zoneFor(ctx context.Context, hostname string) (zone, error) {
	var resp struct {
		envelope
		Result []zone `json:"result"`
	}
	if err := c.do(ctx, http.MethodGet, "/zones?per_page=50", nil, &resp); err != nil {
		return zone{}, err
	}

	name := strings.TrimSuffix(strings.ToLower(strings.TrimPrefix(hostname, "*.")), ".")

	var best zone
	for _, z := range resp.Result {
		candidate := strings.ToLower(z.Name)
		if name != candidate && !strings.HasSuffix(name, "."+candidate) {
			continue
		}
		if len(candidate) > len(best.Name) {
			best = z
		}
	}

	if best.ID == "" {
		return zone{}, fmt.Errorf(
			"cloudflare: no zone in this account covers %s — add the domain to "+
				"Cloudflare first, or check the token reaches the right account",
			hostname)
	}
	return best, nil
}

// RouteHostname points a hostname at a tunnel, replacing whatever was there.
//
// The record has to be proxied: the target resolves only inside Cloudflare, so
// an unproxied CNAME publishes a name that answers with an address nothing can
// reach. That is not a preference to be honoured, so it is not offered as one.
func (c *Client) RouteHostname(ctx context.Context, hostname string, tunnel Tunnel) error {
	z, err := c.zoneFor(ctx, hostname)
	if err != nil {
		return err
	}

	target := tunnel.Hostname()
	name := strings.TrimSuffix(strings.ToLower(hostname), ".")

	existing, err := c.findRecord(ctx, z.ID, name)
	if err != nil {
		return err
	}

	body := map[string]any{
		"type":    "CNAME",
		"name":    name,
		"content": target,
		"proxied": true,
		"comment": "openfrp tunnel " + tunnel.Name,
	}

	if existing.ID == "" {
		return c.do(ctx, http.MethodPost,
			"/zones/"+url.PathEscape(z.ID)+"/dns_records", body, nil)
	}

	// An existing record that already says the right thing is left alone, so
	// a re-run does not churn the zone's history for nothing.
	if existing.Type == "CNAME" && existing.Content == target && existing.Proxied {
		return nil
	}

	// A record of another type has to go: Cloudflare will not turn an A record
	// into a CNAME in place, and leaving it would keep serving the old address.
	if existing.Type != "CNAME" {
		if err := c.do(ctx, http.MethodDelete,
			"/zones/"+url.PathEscape(z.ID)+"/dns_records/"+url.PathEscape(existing.ID),
			nil, nil); err != nil {
			return err
		}
		return c.do(ctx, http.MethodPost,
			"/zones/"+url.PathEscape(z.ID)+"/dns_records", body, nil)
	}

	return c.do(ctx, http.MethodPut,
		"/zones/"+url.PathEscape(z.ID)+"/dns_records/"+url.PathEscape(existing.ID),
		body, nil)
}

// findRecord returns the record for a name, or a zero record if there is none.
func (c *Client) findRecord(ctx context.Context, zoneID, name string) (record, error) {
	var resp struct {
		envelope
		Result []record `json:"result"`
	}
	path := "/zones/" + url.PathEscape(zoneID) + "/dns_records?name=" + url.QueryEscape(name)
	if err := c.do(ctx, http.MethodGet, path, nil, &resp); err != nil {
		return record{}, err
	}
	if len(resp.Result) == 0 {
		return record{}, nil
	}
	return resp.Result[0], nil
}
