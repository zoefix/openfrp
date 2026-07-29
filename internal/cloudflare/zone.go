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

var api = "https://api.cloudflare.com/client/v4"

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

const EdgeSuffix = ".cfargotunnel.com"
