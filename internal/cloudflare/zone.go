package cloudflare

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// api is the Cloudflare endpoint. A variable so tests can answer.
var api = "https://api.cloudflare.com/client/v4"

// envelope is Cloudflare's uniform wrapper.
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
		return fmt.Errorf("cloudflare: the request was refused with no reason given")
	}

	messages := make([]string, 0, len(e.Errors))
	for _, item := range e.Errors {
		messages = append(messages, fmt.Sprintf("%d: %s", item.Code, item.Message))
	}
	return fmt.Errorf("cloudflare: %s", strings.Join(messages, "; "))
}

// call makes one API request with the token from the login credential.
func (c Credential) call(ctx context.Context, method, path string, out any) error {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, method, api+path, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+c.APIToken)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("cloudflare: %s %s: %w", method, path, err)
	}
	defer resp.Body.Close()

	payload, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return err
	}

	var wrapper envelope
	if err := json.Unmarshal(payload, &wrapper); err != nil {
		return fmt.Errorf("cloudflare: %s %s returned %s", method, path, resp.Status)
	}
	if err := wrapper.err(); err != nil {
		return err
	}
	if out == nil {
		return nil
	}
	return json.Unmarshal(payload, out)
}

// ZoneName is the domain the login was authorised for.
//
// Looked up rather than asked for. The operator already chose it, in
// Cloudflare's own dialog, and asking again would be asking them to retype a
// decision they have made — with a typo turning every hostname into one that
// resolves nowhere.
func (c Credential) ZoneName(ctx context.Context) (string, error) {
	var resp struct {
		envelope
		Result struct {
			Name string `json:"name"`
		} `json:"result"`
	}
	if err := c.call(ctx, http.MethodGet,
		"/zones/"+url.PathEscape(c.ZoneID), &resp); err != nil {
		return "", err
	}
	if resp.Result.Name == "" {
		return "", fmt.Errorf("cloudflare: zone %s has no name", c.ZoneID)
	}
	return resp.Result.Name, nil
}

// DeleteRecord removes the DNS record for a hostname.
//
// Used when a tunnel is deleted or renamed: the CNAME it was published under
// would otherwise stay, pointing at a tunnel that no longer serves it, and the
// name would answer with Cloudflare's own error rather than not resolving.
//
// Only a CNAME into a tunnel is removed. A record of any other kind at that
// name was put there by somebody, for something, and this has no business
// deciding it is rubbish.
func (c Credential) DeleteRecord(ctx context.Context, hostname string) (bool, error) {
	name := strings.TrimSuffix(strings.ToLower(hostname), ".")

	var listed struct {
		envelope
		Result []struct {
			ID      string `json:"id"`
			Type    string `json:"type"`
			Content string `json:"content"`
		} `json:"result"`
	}
	path := "/zones/" + url.PathEscape(c.ZoneID) +
		"/dns_records?name=" + url.QueryEscape(name)
	if err := c.call(ctx, http.MethodGet, path, &listed); err != nil {
		return false, err
	}

	for _, record := range listed.Result {
		if record.Type != "CNAME" || !strings.HasSuffix(record.Content, EdgeSuffix) {
			continue
		}
		err := c.call(ctx, http.MethodDelete,
			"/zones/"+url.PathEscape(c.ZoneID)+"/dns_records/"+url.PathEscape(record.ID),
			nil)
		if err != nil {
			return false, err
		}
		return true, nil
	}
	return false, nil
}

// EdgeSuffix is what a hostname points at to reach a tunnel.
const EdgeSuffix = ".cfargotunnel.com"
