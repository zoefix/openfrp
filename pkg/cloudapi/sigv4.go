package cloudapi

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"
)

type SigV4Profile struct {
	Algorithm string

	KeyPrefix string

	Terminator string

	DateHeader string

	DateFormat string

	TimestampHeader string

	ScopeDate bool

	ScopeRegionService bool

	SignedHeaderPrefix string
}

type SigV4Request struct {
	Method  string
	URL     *url.URL
	Headers http.Header
	Payload []byte

	AccessKey string
	SecretKey string
	Region    string
	Service   string

	Now time.Time
}

func SignSigV4(profile SigV4Profile, req *SigV4Request) error {
	if req.Headers == nil {
		req.Headers = http.Header{}
	}
	now := req.Now
	if now.IsZero() {
		now = time.Now()
	}
	now = now.UTC()

	if req.URL == nil {
		return fmt.Errorf("cloudapi: no URL to sign")
	}
	req.Headers.Set("Host", req.URL.Host)

	if profile.DateHeader != "" && profile.DateFormat != "" {
		req.Headers.Set(profile.DateHeader, now.Format(profile.DateFormat))
	}
	if profile.TimestampHeader != "" {
		req.Headers.Set(profile.TimestampHeader, fmt.Sprintf("%d", now.Unix()))
	}

	canonicalRequest, signedHeaders := canonicalRequest(profile, req)
	hashedCanonical := sha256Hex([]byte(canonicalRequest))

	scope := credentialScope(profile, now, req)

	stringToSign := strings.Join([]string{
		profile.Algorithm,
		timestampForSigning(profile, now),
		scope,
		hashedCanonical,
	}, "\n")

	signingKey := deriveKey(profile, now, req)
	signature := hex.EncodeToString(hmacSHA256(signingKey, []byte(stringToSign)))

	credential := req.AccessKey
	if scope != "" {
		credential += "/" + scope
	}

	req.Headers.Set("Authorization", fmt.Sprintf(
		"%s Credential=%s, SignedHeaders=%s, Signature=%s",
		profile.Algorithm, credential, signedHeaders, signature))

	return nil
}

func canonicalRequest(profile SigV4Profile, req *SigV4Request) (string, string) {
	path := req.URL.EscapedPath()
	if path == "" {
		path = "/"
	}

	names := make([]string, 0, len(req.Headers))
	for name := range req.Headers {
		lower := strings.ToLower(name)
		if lower == "authorization" {
			continue
		}
		if profile.SignedHeaderPrefix != "" &&
			lower != "host" &&
			lower != "content-type" &&
			!strings.HasPrefix(lower, profile.SignedHeaderPrefix) {
			continue
		}
		names = append(names, lower)
	}
	sort.Strings(names)

	var canonicalHeaders strings.Builder
	for _, name := range names {
		value := strings.TrimSpace(req.Headers.Get(name))
		canonicalHeaders.WriteString(name)
		canonicalHeaders.WriteByte(':')
		canonicalHeaders.WriteString(value)
		canonicalHeaders.WriteByte('\n')
	}

	signedHeaders := strings.Join(names, ";")

	return strings.Join([]string{
		strings.ToUpper(req.Method),
		path,
		CanonicalQuery(req.URL.Query()),
		canonicalHeaders.String(),
		signedHeaders,
		sha256Hex(req.Payload),
	}, "\n"), signedHeaders
}

func credentialScope(profile SigV4Profile, now time.Time, req *SigV4Request) string {
	var parts []string
	if profile.ScopeDate {
		parts = append(parts, now.Format("20060102"))
	}
	if profile.ScopeRegionService {
		if req.Region != "" {
			parts = append(parts, req.Region)
		}
		if req.Service != "" {
			parts = append(parts, req.Service)
		}
	} else if req.Service != "" {
		parts = append(parts, req.Service)
	}
	if profile.Terminator != "" {
		parts = append(parts, profile.Terminator)
	}
	return strings.Join(parts, "/")
}

func timestampForSigning(profile SigV4Profile, now time.Time) string {
	if profile.TimestampHeader != "" {
		return fmt.Sprintf("%d", now.Unix())
	}
	if profile.DateFormat != "" {
		return now.Format(profile.DateFormat)
	}
	return now.Format("20060102T150405Z")
}

func deriveKey(profile SigV4Profile, now time.Time, req *SigV4Request) []byte {
	if profile.KeyPrefix == "" {

		return []byte(req.SecretKey)
	}

	key := []byte(profile.KeyPrefix + req.SecretKey)
	if profile.ScopeDate {
		key = hmacSHA256(key, []byte(now.Format("20060102")))
	}
	if profile.ScopeRegionService && req.Region != "" {
		key = hmacSHA256(key, []byte(req.Region))
	}
	if req.Service != "" {
		key = hmacSHA256(key, []byte(req.Service))
	}
	if profile.Terminator != "" {
		key = hmacSHA256(key, []byte(profile.Terminator))
	}
	return key
}

func CanonicalQuery(values url.Values) string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)

	var out strings.Builder
	for _, key := range keys {
		items := append([]string(nil), values[key]...)
		sort.Strings(items)
		for _, item := range items {
			if out.Len() > 0 {
				out.WriteByte('&')
			}
			out.WriteString(PercentEncode(key))
			out.WriteByte('=')
			out.WriteString(PercentEncode(item))
		}
	}
	return out.String()
}

func PercentEncode(value string) string {
	escaped := url.QueryEscape(value)
	escaped = strings.ReplaceAll(escaped, "+", "%20")
	escaped = strings.ReplaceAll(escaped, "*", "%2A")
	escaped = strings.ReplaceAll(escaped, "%7E", "~")
	return escaped
}

func hmacSHA256(key, data []byte) []byte {
	mac := hmac.New(sha256.New, key)
	mac.Write(data)
	return mac.Sum(nil)
}

func sha256Hex(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

var (
	TencentProfile = SigV4Profile{
		Algorithm:          "TC3-HMAC-SHA256",
		KeyPrefix:          "TC3",
		Terminator:         "tc3_request",
		TimestampHeader:    "X-TC-Timestamp",
		ScopeDate:          true,
		ScopeRegionService: false,
	}

	HuaweiProfile = SigV4Profile{
		Algorithm:  "SDK-HMAC-SHA256",
		DateHeader: "X-Sdk-Date",
		DateFormat: "20060102T150405Z",
	}

	VolcengineProfile = SigV4Profile{
		Algorithm:          "HMAC-SHA256",
		Terminator:         "request",
		DateHeader:         "X-Date",
		DateFormat:         "20060102T150405Z",
		ScopeDate:          true,
		ScopeRegionService: true,
	}

	JDCloudProfile = SigV4Profile{
		Algorithm:          "JDCLOUD2-HMAC-SHA256",
		KeyPrefix:          "JDCLOUD2",
		Terminator:         "jdcloud2_request",
		DateHeader:         "X-Jdcloud-Date",
		DateFormat:         "20060102T150405Z",
		ScopeDate:          true,
		ScopeRegionService: true,
	}
)
