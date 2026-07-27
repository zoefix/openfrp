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

// DefaultTimeout bounds a single provider API call.
//
// Generous, because these APIs are reached from a home router over whatever
// path the operator's proxy chooses, and a certificate renewal failing on a
// slow night is worse than one that waits.
const DefaultTimeout = 30 * time.Second

// maxErrorBody caps how much of a failure response is quoted back. Enough to
// carry a provider's error message, not enough for an HTML error page to bury
// the log.
const maxErrorBody = 2 << 10

// HTTPClient is the shared transport for provider APIs.
type HTTPClient struct {
	client *http.Client
	// UserAgent identifies us to providers that log or rate-limit by client.
	UserAgent string
}

// NewHTTPClient returns a client with sane timeouts.
func NewHTTPClient() *HTTPClient {
	return &HTTPClient{
		client:    &http.Client{Timeout: DefaultTimeout},
		UserAgent: "openfrp/dns",
	}
}

// Request describes one API call.
type Request struct {
	Method  string
	URL     string
	Headers map[string]string
	// Body is sent as-is. Use JSONBody for the common case.
	Body []byte
}

// JSONBody encodes v and sets the content type.
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

// Do performs a request and decodes a JSON response into out.
//
// A non-2xx status is an error carrying the response body, because every one
// of these providers reports its real complaint there while the status code
// says only "400".
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

// hostOf extracts the host for error messages, so a signed URL's credentials
// never reach a log.
func hostOf(rawURL string) string {
	trimmed := strings.TrimPrefix(strings.TrimPrefix(rawURL, "https://"), "http://")
	if idx := strings.IndexAny(trimmed, "/?"); idx >= 0 {
		return trimmed[:idx]
	}
	return trimmed
}

// NormaliseName converts a fully qualified name into the host portion a
// provider expects, relative to its zone.
//
// Providers disagree about the apex: some want "@", some want an empty string,
// some want the zone name repeated. Each provider applies its own convention
// on top of this.
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
