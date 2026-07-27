// Package cloudflare talks to the Cloudflare API for the parts of a tunnel
// this router has to own.
//
// A Cloudflare tunnel is not one of this project's servers. There is no
// openfrps at the other end, no address to dial and no shared token: the
// cloudflared process here connects outward to Cloudflare's edge, and
// Cloudflare routes a hostname down that connection. It is offered alongside
// the servers because that is where an operator looks for "somewhere to
// publish this", not because it is one.
//
// What that costs is written down where it is enforced rather than here: a
// tunnel published this way is HTTP only, its TLS is terminated by Cloudflare,
// and the visitor's address arrives in a header instead of the PROXY protocol.
package cloudflare

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// APIBase is Cloudflare's v4 endpoint. Overridable so tests can answer.
const APIBase = "https://api.cloudflare.com/client/v4"

// EdgeSuffix is what a hostname is pointed at to reach a tunnel. The record is
// a CNAME to <tunnel-id>.cfargotunnel.com, which resolves only inside
// Cloudflare — which is why the record has to be proxied to work at all.
const EdgeSuffix = ".cfargotunnel.com"

// Client is an authenticated Cloudflare API caller.
type Client struct {
	token string
	base  string
	http  *http.Client
}

// New returns a client using an API token.
//
// Only the token form is supported. The global API key grants everything on
// the account, and asking for one to publish a website is asking for far more
// than the job needs.
func New(token string) *Client {
	return &Client{
		token: strings.TrimSpace(token),
		base:  APIBase,
		http:  &http.Client{Timeout: 30 * time.Second},
	}
}

// SetBase points the client at another endpoint. For tests.
func (c *Client) SetBase(base string) { c.base = strings.TrimSuffix(base, "/") }

// Account is one Cloudflare account the token can see.
type Account struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// Tunnel is a created tunnel.
type Tunnel struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	AccountID string `json:"-"`

	// Secret is the value the credentials file is built from. It is known
	// only because this client generated it: Cloudflare does not hand it back
	// after creation, so a tunnel whose secret is lost has to be recreated.
	Secret string `json:"-"`
}

// Hostname is the CNAME target that routes a name into this tunnel.
func (t Tunnel) Hostname() string { return t.ID + EdgeSuffix }

// envelope is Cloudflare's uniform response wrapper.
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
		// 10000 is what Cloudflare returns for a token that lacks a
		// permission, and on its own it says only "Authentication error",
		// which reads as a wrong token rather than a token missing one box.
		if item.Code == 10000 {
			messages = append(messages, fmt.Sprintf(
				"%d: %s (the token may be missing a permission this needs)",
				item.Code, item.Message))
			continue
		}
		messages = append(messages, fmt.Sprintf("%d: %s", item.Code, item.Message))
	}
	return fmt.Errorf("cloudflare: %s", strings.Join(messages, "; "))
}

// do performs one API call and decodes result into out.
func (c *Client) do(ctx context.Context, method, path string, body, out any) error {
	var reader io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			return err
		}
		reader = bytes.NewReader(encoded)
	}

	req, err := http.NewRequestWithContext(ctx, method, c.base+path, reader)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("cloudflare: %s %s: %w", method, path, err)
	}
	defer resp.Body.Close()

	// Read it all before deciding: Cloudflare returns its own error envelope
	// with a 4xx status, and that envelope says which permission is missing
	// where the status code says only that something was wrong.
	payload, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return fmt.Errorf("cloudflare: reading the reply to %s %s: %w", method, path, err)
	}

	var wrapper envelope
	if err := json.Unmarshal(payload, &wrapper); err != nil {
		return fmt.Errorf("cloudflare: %s %s returned %s, which is not an API reply",
			method, path, resp.Status)
	}
	if err := wrapper.err(); err != nil {
		return err
	}
	if resp.StatusCode >= 400 {
		return fmt.Errorf("cloudflare: %s %s: %s", method, path, resp.Status)
	}

	if out == nil {
		return nil
	}
	return json.Unmarshal(payload, out)
}

// Accounts lists the accounts this token can act on.
//
// A token usually reaches exactly one, and the setup uses it without asking.
// Asking would be a question with one possible answer for almost everybody,
// and an unanswerable one for the operator who does not know which of their
// accounts a token was scoped to.
func (c *Client) Accounts(ctx context.Context) ([]Account, error) {
	var resp struct {
		envelope
		Result []Account `json:"result"`
	}
	if err := c.do(ctx, http.MethodGet, "/accounts?per_page=50", nil, &resp); err != nil {
		return nil, err
	}
	return resp.Result, nil
}

// CreateTunnel makes a tunnel and returns it with the secret it was made with.
//
// The secret is generated here rather than left to Cloudflare, because the
// credentials file cloudflared needs is built from it and Cloudflare will not
// hand it back afterwards. Generating it means the file can be written without
// a second call that might fail with the tunnel already made.
func (c *Client) CreateTunnel(ctx context.Context, accountID, name string) (Tunnel, error) {
	if accountID == "" {
		return Tunnel{}, fmt.Errorf("cloudflare: creating a tunnel needs an account")
	}
	if strings.TrimSpace(name) == "" {
		return Tunnel{}, fmt.Errorf("cloudflare: a tunnel needs a name")
	}

	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return Tunnel{}, fmt.Errorf("cloudflare: generating a tunnel secret: %w", err)
	}
	secret := base64.StdEncoding.EncodeToString(raw)

	var resp struct {
		envelope
		Result Tunnel `json:"result"`
	}
	body := map[string]any{
		"name":          name,
		"tunnel_secret": secret,
		// The configuration lives on this router, in a file rendered from the
		// tunnel list, rather than in Cloudflare's dashboard. Two places
		// holding the same routing table is one more than can be kept true.
		"config_src": "local",
	}
	err := c.do(ctx, http.MethodPost,
		"/accounts/"+url.PathEscape(accountID)+"/cfd_tunnel", body, &resp)
	if err != nil {
		return Tunnel{}, err
	}
	if resp.Result.ID == "" {
		return Tunnel{}, fmt.Errorf("cloudflare: the tunnel was created without an id")
	}

	resp.Result.AccountID = accountID
	resp.Result.Secret = secret
	return resp.Result, nil
}

// ListTunnels returns the account's tunnels that are not deleted.
func (c *Client) ListTunnels(ctx context.Context, accountID string) ([]Tunnel, error) {
	var resp struct {
		envelope
		Result []Tunnel `json:"result"`
	}
	path := "/accounts/" + url.PathEscape(accountID) + "/cfd_tunnel?is_deleted=false&per_page=50"
	if err := c.do(ctx, http.MethodGet, path, nil, &resp); err != nil {
		return nil, err
	}
	return resp.Result, nil
}

// DeleteTunnel removes one.
func (c *Client) DeleteTunnel(ctx context.Context, accountID, tunnelID string) error {
	return c.do(ctx, http.MethodDelete,
		"/accounts/"+url.PathEscape(accountID)+"/cfd_tunnel/"+url.PathEscape(tunnelID),
		nil, nil)
}

// Credentials is the file cloudflared authenticates with.
//
// The field names are cloudflared's, capitalised as it spells them; it reads
// this file itself and will not recognise them written any other way.
type Credentials struct {
	AccountTag   string `json:"AccountTag"`
	TunnelID     string `json:"TunnelID"`
	TunnelSecret string `json:"TunnelSecret"`
}

// Credentials builds the file contents for a created tunnel.
func (t Tunnel) Credentials() Credentials {
	return Credentials{
		AccountTag:   t.AccountID,
		TunnelID:     t.ID,
		TunnelSecret: t.Secret,
	}
}
