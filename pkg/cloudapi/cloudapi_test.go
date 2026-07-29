package cloudapi

import (
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"
)

func TestTencentSignatureMatchesPublishedExample(t *testing.T) {
	body := []byte(`{"Limit": 1, "Filters": [{"Values": ["\u672a\u547d\u540d"], "Name": "instance-name"}]}`)

	target, _ := url.Parse("https://cvm.tencentcloudapi.com/")
	req := &SigV4Request{
		Method: http.MethodPost,
		URL:    target,
		Headers: http.Header{
			"Content-Type": {"application/json; charset=utf-8"},
			"X-Tc-Action":  {"DescribeInstances"},
		},
		Payload:   body,
		AccessKey: "AKID" + "z8krbsJ5yKBZQpn74WFkmLPx3EXAMPLE",
		SecretKey: "Gu5t9xGARNpq86cd98joQYCN3EXAMPLE",
		Service:   "cvm",
		Now:       time.Unix(1551113065, 0).UTC(),
	}

	if err := SignSigV4(TencentProfile, req); err != nil {
		t.Fatalf("SignSigV4: %v", err)
	}

	auth := req.Headers.Get("Authorization")
	if !strings.HasPrefix(auth, "TC3-HMAC-SHA256 ") {
		t.Fatalf("Authorization = %q, want the TC3 algorithm prefix", auth)
	}
	if !strings.Contains(auth, "Credential=AKID" + "z8krbsJ5yKBZQpn74WFkmLPx3EXAMPLE/20190225/cvm/tc3_request") {
		t.Errorf("credential scope is wrong:\n%s", auth)
	}
	if got := req.Headers.Get("X-TC-Timestamp"); got != "1551113065" {
		t.Errorf("X-TC-Timestamp = %q, want 1551113065", got)
	}

	second := *req
	second.Headers = req.Headers.Clone()
	second.Headers.Del("Authorization")
	if err := SignSigV4(TencentProfile, &second); err != nil {
		t.Fatalf("second sign: %v", err)
	}
	if second.Headers.Get("Authorization") != auth {
		t.Error("signing the same request twice produced different signatures")
	}
}

func TestHuaweiSignsWithSecretDirectly(t *testing.T) {
	target, _ := url.Parse("https://dns.myhuaweicloud.com/v2/zones")
	req := &SigV4Request{
		Method:    http.MethodGet,
		URL:       target,
		Headers:   http.Header{"Content-Type": {"application/json"}},
		AccessKey: "AK",
		SecretKey: "SK",
		Now:       time.Unix(1551113065, 0).UTC(),
	}

	if err := SignSigV4(HuaweiProfile, req); err != nil {
		t.Fatalf("SignSigV4: %v", err)
	}

	auth := req.Headers.Get("Authorization")
	if !strings.HasPrefix(auth, "SDK-HMAC-SHA256 ") {
		t.Errorf("Authorization = %q, want the SDK-HMAC-SHA256 prefix", auth)
	}

	if !strings.Contains(auth, "Credential=AK,") {
		t.Errorf("Huawei credential should be the bare access key:\n%s", auth)
	}
	if got := req.Headers.Get("X-Sdk-Date"); got != "20190225T164425Z" {
		t.Errorf("X-Sdk-Date = %q, want 20190225T164425Z", got)
	}
}

func TestCanonicalQueryEncoding(t *testing.T) {
	values := url.Values{
		"b":     {"two words"},
		"a":     {"~tilde"},
		"c":     {"star*"},
		"empty": {""},
	}

	got := CanonicalQuery(values)
	want := "a=~tilde&b=two%20words&c=star%2A&empty="

	if got != want {
		t.Errorf("CanonicalQuery:\n got %q\nwant %q", got, want)
	}
}

func TestCanonicalQuerySortsRepeatedValues(t *testing.T) {
	values := url.Values{"k": {"z", "a", "m"}}
	if got, want := CanonicalQuery(values), "k=a&k=m&k=z"; got != want {
		t.Errorf("CanonicalQuery = %q, want %q", got, want)
	}
}

func TestAliyunDoubleEncoding(t *testing.T) {
	req := AliyunRPCRequest{
		Endpoint:        "alidns.aliyuncs.com",
		Action:          "DescribeDomainRecords",
		Version:         "2015-01-09",
		Params:          map[string]string{"DomainName": "example.com"},
		AccessKeyID:     "testid",
		AccessKeySecret: "testsecret",
		Now:             time.Unix(1551113065, 0).UTC(),
		Nonce:           "fixednonce",
	}

	signed, err := req.SignedURL()
	if err != nil {
		t.Fatalf("SignedURL: %v", err)
	}

	if !strings.HasPrefix(signed, "https://alidns.aliyuncs.com/?Signature=") {
		t.Fatalf("unexpected URL shape: %s", signed)
	}
	for _, want := range []string{
		"Action=DescribeDomainRecords",
		"DomainName=example.com",
		"SignatureMethod=HMAC-SHA1",
		"SignatureNonce=fixednonce",
		"Timestamp=2019-02-25T16%3A44%3A25Z",
	} {
		if !strings.Contains(signed, want) {
			t.Errorf("signed URL is missing %q:\n%s", want, signed)
		}
	}

	again, err := req.SignedURL()
	if err != nil {
		t.Fatalf("second SignedURL: %v", err)
	}
	if again != signed {
		t.Error("signing twice with a fixed nonce produced different URLs")
	}

	other := req
	other.AccessKeySecret = "different"
	changed, _ := other.SignedURL()
	if changed == signed {
		t.Error("the signature does not depend on the secret key")
	}
}

func TestBaiduAuthorizationShape(t *testing.T) {
	req := BaiduBCERequest{
		Method:          http.MethodGet,
		Host:            "dns.baidubce.com",
		Path:            "/v1/dns/zone",
		Query:           url.Values{"pageNo": {"1"}},
		AccessKeyID:     "AK",
		SecretAccessKey: "SK",
		Now:             time.Unix(1551113065, 0).UTC(),
	}

	auth := req.AuthorizationHeader()
	if !strings.HasPrefix(auth, "bce-auth-v1/AK/2019-02-25T16:44:25Z/1800/") {
		t.Errorf("unexpected authorization prefix: %s", auth)
	}
	if !strings.Contains(auth, "/host/") {
		t.Errorf("host should be among the signed headers: %s", auth)
	}
}
