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

// certPEM builds a login credential with the token block cloudflared writes.
func certPEM(payload string) []byte {
	encoded := base64.StdEncoding.EncodeToString([]byte(payload))

	var out strings.Builder
	out.WriteString("-----BEGIN PRIVATE KEY-----\n")
	out.WriteString(base64.StdEncoding.EncodeToString([]byte("not a key")) + "\n")
	out.WriteString("-----END PRIVATE KEY-----\n")
	out.WriteString("-----BEGIN ARGO TUNNEL TOKEN-----\n")
	out.WriteString(encoded + "\n")
	out.WriteString("-----END ARGO TUNNEL TOKEN-----\n")
	return []byte(out.String())
}

func TestCredentialIsReadFromTheLoginFile(t *testing.T) {
	raw := certPEM(`{"zoneID":"z1","accountID":"a1","apiToken":"cfut_secret"}`)

	credential, err := parseCredential(raw)
	if err != nil {
		t.Fatalf("parseCredential: %v", err)
	}
	if credential.ZoneID != "z1" || credential.APIToken != "cfut_secret" {
		t.Errorf("read %+v", credential)
	}
}

// A credential without a zone cannot supply a suffix, and saying so points at
// the choice that produced it.
func TestACredentialWithoutAZoneSaysSo(t *testing.T) {
	_, err := parseCredential(certPEM(`{"accountID":"a1","apiToken":"t"}`))
	if err == nil {
		t.Fatal("a credential naming no zone was accepted")
	}
	if !strings.Contains(err.Error(), "authorise again") {
		t.Errorf("the error does not say what to do: %v", err)
	}
}

// The file holds a private key, so a parse failure must not quote it back.
func TestAParseFailureDoesNotQuoteTheFile(t *testing.T) {
	secret := "-----BEGIN PRIVATE KEY-----\nc2VjcmV0\n-----END PRIVATE KEY-----\n"

	_, err := parseCredential([]byte(secret))
	if err == nil {
		t.Fatal("a credential with no token block was accepted")
	}
	if strings.Contains(err.Error(), "c2VjcmV0") {
		t.Errorf("the error quotes the key material: %v", err)
	}
}

func withAPI(t *testing.T, routes map[string]string) (Credential, map[string]int) {
	t.Helper()

	calls := map[string]int{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer tok" {
			t.Errorf("token sent as %q", got)
		}
		key := r.Method + " " + r.URL.Path
		calls[key]++

		if body, ok := routes[key]; ok {
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(body))
			return
		}
		w.WriteHeader(http.StatusNotFound)
		w.Write([]byte(`{"success":false,"errors":[{"code":7003,"message":"no route"}]}`))
	}))
	t.Cleanup(server.Close)

	previous := api
	api = server.URL
	t.Cleanup(func() { api = previous })

	return Credential{ZoneID: "z1", APIToken: "tok"}, calls
}

func TestZoneNameIsLookedUpNotAskedFor(t *testing.T) {
	credential, _ := withAPI(t, map[string]string{
		"GET /zones/z1": `{"success":true,"result":{"id":"z1","name":"example.com"}}`,
	})

	name, err := credential.ZoneName(context.Background())
	if err != nil {
		t.Fatalf("ZoneName: %v", err)
	}
	if name != "example.com" {
		t.Errorf("zone read as %q", name)
	}
}

func TestDeleteRecordRemovesTheTunnelCNAME(t *testing.T) {
	credential, calls := withAPI(t, map[string]string{
		"GET /zones/z1/dns_records": `{"success":true,"result":[` +
			`{"id":"r1","type":"CNAME","content":"tid.cfargotunnel.com"}]}`,
		"DELETE /zones/z1/dns_records/r1": `{"success":true}`,
	})

	removed, err := credential.DeleteRecord(context.Background(), "old.example.com")
	if err != nil {
		t.Fatalf("DeleteRecord: %v", err)
	}
	if !removed {
		t.Error("the record was not reported as removed")
	}
	if calls["DELETE /zones/z1/dns_records/r1"] != 1 {
		t.Error("the record was not deleted")
	}
}

// A record somebody else put there is not this feature's to remove, however
// much the name matches.
func TestDeleteRecordLeavesRecordsItDidNotMake(t *testing.T) {
	credential, calls := withAPI(t, map[string]string{
		"GET /zones/z1/dns_records": `{"success":true,"result":[` +
			`{"id":"r1","type":"A","content":"203.0.113.10"},` +
			`{"id":"r2","type":"CNAME","content":"somewhere.else.net"}]}`,
	})

	removed, err := credential.DeleteRecord(context.Background(), "kept.example.com")
	if err != nil {
		t.Fatalf("DeleteRecord: %v", err)
	}
	if removed {
		t.Error("a record this did not create was reported as removed")
	}
	for key, count := range calls {
		if strings.HasPrefix(key, "DELETE") && count > 0 {
			t.Errorf("%s was called on a record this did not create", key)
		}
	}
}

// A name with nothing at it is not an error: the record may already be gone,
// which is the state being asked for.
func TestDeleteRecordAcceptsAnAbsentRecord(t *testing.T) {
	credential, _ := withAPI(t, map[string]string{
		"GET /zones/z1/dns_records": `{"success":true,"result":[]}`,
	})

	removed, err := credential.DeleteRecord(context.Background(), "gone.example.com")
	if err != nil {
		t.Fatalf("DeleteRecord: %v", err)
	}
	if removed {
		t.Error("an absent record was reported as removed")
	}
}

func TestARefusalIsReported(t *testing.T) {
	credential, _ := withAPI(t, map[string]string{
		"GET /zones/z1": `{"success":false,"errors":[{"code":9109,"message":"no access"}]}`,
	})

	if _, err := credential.ZoneName(context.Background()); err == nil {
		t.Fatal("a refused lookup reported success")
	}
}

// The payload is JSON once decoded from the PEM body; some versions wrapped it
// in a second layer of base64.
func TestADoublyEncodedTokenIsStillRead(t *testing.T) {
	inner, _ := json.Marshal(Credential{ZoneID: "z9", APIToken: "t9"})
	doubled := base64.StdEncoding.EncodeToString(inner)

	credential, err := parseCredential(certPEM(doubled))
	if err != nil {
		t.Fatalf("parseCredential: %v", err)
	}
	if credential.ZoneID != "z9" {
		t.Errorf("read %+v", credential)
	}
}
