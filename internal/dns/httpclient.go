package dns

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

const DefaultTimeout = 30 * time.Second

const maxErrorBody = 2 << 10

type HTTPClient struct {
	client *http.Client

	UserAgent string
}

func NewHTTPClient() *HTTPClient {
	return &HTTPClient{
		client:    &http.Client{Timeout: DefaultTimeout},
		UserAgent: "openfrp/dns",
	}
}

type Request struct {
	Method  string
	URL     string
	Headers map[string]string

	Body []byte
}

func (r *Request) JSONBody(v any) error {
	payload, err := json.Marshal(v)
	if err != nil {
		return fmt.Errorf("dns: encode request body: %w", err)
	}
	r.Body = payload
	if r.Headers == nil {
		r.Headers = map[string]string{}
	}
	r.Headers["Content-Type"] = "application/json"
	return nil
}

func (c *HTTPClient) Do(ctx context.Context, req Request, out any) error {
	method := req.Method
	if method == "" {
		method = http.MethodGet
	}

	var body io.Reader
	if len(req.Body) > 0 {
		body = bytes.NewReader(req.Body)
	}

	httpReq, err := http.NewRequestWithContext(ctx, method, req.URL, body)
	if err != nil {
		return fmt.Errorf("dns: build request: %w", err)
	}

	httpReq.Header.Set("User-Agent", c.UserAgent)
	httpReq.Header.Set("Accept", "application/json")
	for key, value := range req.Headers {
		httpReq.Header.Set(key, value)
	}

	resp, err := c.client.Do(httpReq)
	if err != nil {
		return fmt.Errorf("dns: %s %s: %w", method, hostOf(req.URL), err)
	}
	defer resp.Body.Close()

	payload, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return fmt.Errorf("dns: read response: %w", err)
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("dns: %s %s: HTTP %d: %s",
			method, hostOf(req.URL), resp.StatusCode, snippet(payload))
	}

	if out == nil || len(payload) == 0 {
		return nil
	}
	if err := json.Unmarshal(payload, out); err != nil {
		return fmt.Errorf("dns: decode response from %s: %w: %s",
			hostOf(req.URL), err, snippet(payload))
	}
	return nil
}

func snippet(payload []byte) string {
	text := strings.TrimSpace(string(payload))
	if len(text) > maxErrorBody {
		return text[:maxErrorBody] + "…"
	}
	return text
}

func hostOf(rawURL string) string {
	trimmed := strings.TrimPrefix(strings.TrimPrefix(rawURL, "https://"), "http://")
	if idx := strings.IndexAny(trimmed, "/?"); idx >= 0 {
		return trimmed[:idx]
	}
	return trimmed
}

func NormaliseName(fqdn, zone string) string {
	fqdn = strings.TrimSuffix(strings.ToLower(strings.TrimSpace(fqdn)), ".")
	zone = strings.TrimSuffix(strings.ToLower(strings.TrimSpace(zone)), ".")

	if fqdn == zone || fqdn == "" {
		return "@"
	}
	if strings.HasSuffix(fqdn, "."+zone) {
		return strings.TrimSuffix(fqdn, "."+zone)
	}
	return fqdn
}
