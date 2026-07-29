package cloudflare

import (
	"encoding/json"
	"testing"

	"github.com/zoefix/openfrp/internal/dns"
)

func boolPtr(v bool) *bool { return &v }

func TestRecordBodyAlwaysSendsProxied(t *testing.T) {
	p := &provider{}

	cases := []struct {
		name    string
		record  dns.Record
		want    bool
		present bool
	}{
		{
			name: "proxied A record keeps the flag",
			record: dns.Record{
				Name: "www", Type: dns.TypeA, Value: "203.0.113.10",
				Proxied: boolPtr(true), Enabled: true,
			},
			want: true, present: true,
		},
		{
			name: "unproxied A record says so explicitly",
			record: dns.Record{
				Name: "tunnel", Type: dns.TypeA, Value: "203.0.113.10",
				Proxied: boolPtr(false), Enabled: true,
			},
			want: false, present: true,
		},
		{
			name: "unspecified defaults to off rather than being omitted",
			record: dns.Record{
				Name: "new", Type: dns.TypeA, Value: "203.0.113.10", Enabled: true,
			},
			want: false, present: true,
		},
		{

			name: "TXT records omit the flag entirely",
			record: dns.Record{
				Name: "_acme-challenge", Type: dns.TypeTXT,
				Value: "token", Proxied: boolPtr(false), Enabled: true,
			},
			present: false,
		},
		{
			name: "CNAME can be proxied",
			record: dns.Record{
				Name: "alias", Type: dns.TypeCNAME, Value: "example.com",
				Proxied: boolPtr(true), Enabled: true,
			},
			want: true, present: true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			body, err := p.recordBody("example.com", tc.record)
			if err != nil {
				t.Fatalf("recordBody: %v", err)
			}

			value, present := body["proxied"]
			if present != tc.present {
				t.Fatalf("proxied present = %v, want %v (body %v)", present, tc.present, body)
			}
			if !tc.present {
				return
			}
			if value != tc.want {
				t.Errorf("proxied = %v, want %v", value, tc.want)
			}
		})
	}
}

func TestProxiedSurvivesJSONRoundTrip(t *testing.T) {
	for _, want := range []bool{true, false} {
		encoded, err := json.Marshal(dns.Record{
			Name: "www", Type: dns.TypeA, Value: "203.0.113.10",
			Proxied: boolPtr(want), Enabled: true,
		})
		if err != nil {
			t.Fatal(err)
		}

		var decoded dns.Record
		if err := json.Unmarshal(encoded, &decoded); err != nil {
			t.Fatal(err)
		}

		if decoded.Proxied == nil {
			t.Fatalf("proxied=%v was lost in transit: %s", want, encoded)
		}
		if *decoded.Proxied != want {
			t.Errorf("proxied round-tripped to %v, want %v", *decoded.Proxied, want)
		}
	}
}
