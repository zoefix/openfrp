package cloudflare

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// fakeAPI stands in for Cloudflare. Handlers are keyed by "METHOD /path".
type fakeAPI struct {
	t        *testing.T
	replies  map[string]string
	requests map[string]json.RawMessage
	server   *httptest.Server
}

func newFakeAPI(t *testing.T) *fakeAPI {
	t.Helper()

	api := &fakeAPI{
		t:        t,
		replies:  map[string]string{},
		requests: map[string]json.RawMessage{},
	}

	api.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer test-token" {
			t.Errorf("token sent as %q", got)
		}

		key := r.Method + " " + r.URL.Path
		if body, ok := api.replies[key]; ok {
			if r.Body != nil {
				var captured json.RawMessage
				json.NewDecoder(r.Body).Decode(&captured)
				api.requests[key] = captured
			}
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(body))
			return
		}

		w.WriteHeader(http.StatusNotFound)
		w.Write([]byte(`{"success":false,"errors":[{"code":7003,"message":"no route"}]}`))
	}))
	t.Cleanup(api.server.Close)

	return api
}

func (f *fakeAPI) client() *Client {
	c := New("test-token")
	c.SetBase(f.server.URL)
	return c
}

func TestCreateTunnelKeepsTheSecretItGenerated(t *testing.T) {
	api := newFakeAPI(t)
	api.replies["POST /accounts/acct1/cfd_tunnel"] =
		`{"success":true,"result":{"id":"11111111-2222-3333-4444-555555555555","name":"home"}}`

	tunnel, err := api.client().CreateTunnel(context.Background(), "acct1", "home")
	if err != nil {
		t.Fatalf("CreateTunnel: %v", err)
	}

	// Cloudflare does not return the secret, so a tunnel whose secret was not
	// kept here cannot be authenticated against and has to be made again.
	if tunnel.Secret == "" {
		t.Fatal("the tunnel came back without the secret it was made with")
	}
	raw, err := base64.StdEncoding.DecodeString(tunnel.Secret)
	if err != nil {
		t.Fatalf("the secret is not base64: %v", err)
	}
	if len(raw) != 32 {
		t.Errorf("the secret is %d bytes, want 32", len(raw))
	}

	sent := map[string]any{}
	json.Unmarshal(api.requests["POST /accounts/acct1/cfd_tunnel"], &sent)
	if sent["tunnel_secret"] != tunnel.Secret {
		t.Error("the secret sent to Cloudflare is not the one that was kept")
	}
	if sent["config_src"] != "local" {
		t.Errorf("config_src sent as %v, want local — the routing table lives here",
			sent["config_src"])
	}

	if tunnel.AccountID != "acct1" {
		t.Errorf("account recorded as %q", tunnel.AccountID)
	}
	want := "11111111-2222-3333-4444-555555555555.cfargotunnel.com"
	if tunnel.Hostname() != want {
		t.Errorf("edge hostname %q, want %q", tunnel.Hostname(), want)
	}
}

// The credentials file is read by cloudflared, which spells the keys its own
// way and does not recognise them written any other.
func TestCredentialsUseTheNamesCloudflaredReads(t *testing.T) {
	tunnel := Tunnel{ID: "tid", AccountID: "acct", Secret: "c2VjcmV0"}

	encoded, err := json.Marshal(tunnel.Credentials())
	if err != nil {
		t.Fatal(err)
	}

	for _, key := range []string{`"AccountTag"`, `"TunnelID"`, `"TunnelSecret"`} {
		if !strings.Contains(string(encoded), key) {
			t.Errorf("the credentials file has no %s field: %s", key, encoded)
		}
	}
}

// A refusal for a missing permission arrives as a plain "Authentication
// error", which reads as a mistyped token — the one thing it is not.
func TestAPermissionErrorSaysItMightBeAPermission(t *testing.T) {
	api := newFakeAPI(t)
	api.replies["GET /accounts"] =
		`{"success":false,"errors":[{"code":10000,"message":"Authentication error"}]}`

	_, err := api.client().Accounts(context.Background())
	if err == nil {
		t.Fatal("a refused request reported no error")
	}
	if !strings.Contains(err.Error(), "permission") {
		t.Errorf("error does not mention a permission: %v", err)
	}
}

func TestRouteHostnameCreatesAProxiedCNAME(t *testing.T) {
	api := newFakeAPI(t)
	api.replies["GET /zones"] = `{"success":true,"result":[{"id":"z1","name":"example.com"}]}`
	api.replies["GET /zones/z1/dns_records"] = `{"success":true,"result":[]}`
	api.replies["POST /zones/z1/dns_records"] = `{"success":true,"result":{"id":"r1"}}`

	tunnel := Tunnel{ID: "tid", Name: "home"}
	if err := api.client().RouteHostname(context.Background(), "app.example.com", tunnel); err != nil {
		t.Fatalf("RouteHostname: %v", err)
	}

	sent := map[string]any{}
	json.Unmarshal(api.requests["POST /zones/z1/dns_records"], &sent)

	if sent["type"] != "CNAME" {
		t.Errorf("record type %v, want CNAME", sent["type"])
	}
	if sent["content"] != "tid.cfargotunnel.com" {
		t.Errorf("record points at %v", sent["content"])
	}
	// The target resolves only inside Cloudflare. Unproxied, the name answers
	// with an address no visitor can reach.
	if sent["proxied"] != true {
		t.Error("the record was created unproxied, which cannot serve the tunnel")
	}
}

// A record that already says the right thing is left alone.
func TestRouteHostnameLeavesACorrectRecordAlone(t *testing.T) {
	api := newFakeAPI(t)
	api.replies["GET /zones"] = `{"success":true,"result":[{"id":"z1","name":"example.com"}]}`
	api.replies["GET /zones/z1/dns_records"] = `{"success":true,"result":[` +
		`{"id":"r1","type":"CNAME","name":"app.example.com",` +
		`"content":"tid.cfargotunnel.com","proxied":true}]}`

	tunnel := Tunnel{ID: "tid", Name: "home"}
	if err := api.client().RouteHostname(context.Background(), "app.example.com", tunnel); err != nil {
		t.Fatalf("RouteHostname: %v", err)
	}

	// No write of any kind: the fake answers 404 for anything unregistered, so
	// a PUT or POST here would have failed the call above.
	if _, wrote := api.requests["PUT /zones/z1/dns_records/r1"]; wrote {
		t.Error("an already correct record was rewritten")
	}
}

// The zone that serves a name is the longest one that matches it.
func TestZoneForPrefersTheMoreSpecificZone(t *testing.T) {
	api := newFakeAPI(t)
	api.replies["GET /zones"] = `{"success":true,"result":[` +
		`{"id":"broad","name":"example.com"},` +
		`{"id":"narrow","name":"lab.example.com"}]}`

	got, err := api.client().zoneFor(context.Background(), "app.lab.example.com")
	if err != nil {
		t.Fatalf("zoneFor: %v", err)
	}
	if got.ID != "narrow" {
		t.Errorf("chose zone %q, want the more specific lab.example.com", got.ID)
	}
}

// A wildcard is routed by the zone under it, not by the star.
func TestZoneForHandlesAWildcard(t *testing.T) {
	api := newFakeAPI(t)
	api.replies["GET /zones"] = `{"success":true,"result":[{"id":"z1","name":"example.com"}]}`

	got, err := api.client().zoneFor(context.Background(), "*.example.com")
	if err != nil {
		t.Fatalf("zoneFor: %v", err)
	}
	if got.ID != "z1" {
		t.Errorf("chose zone %q", got.ID)
	}
}

// A name the account does not hold has to say so in terms that point at the
// fix, since the token being scoped to another account looks identical.
func TestZoneForSaysWhenTheAccountDoesNotHoldTheName(t *testing.T) {
	api := newFakeAPI(t)
	api.replies["GET /zones"] = `{"success":true,"result":[{"id":"z1","name":"example.com"}]}`

	_, err := api.client().zoneFor(context.Background(), "app.elsewhere.net")
	if err == nil {
		t.Fatal("a name outside every zone was accepted")
	}
	if !strings.Contains(err.Error(), "app.elsewhere.net") {
		t.Errorf("the error does not name the host: %v", err)
	}
}
