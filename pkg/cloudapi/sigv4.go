// Package cloudapi implements the request-signing schemes the Chinese cloud
// providers use.
//
// This layer exists because the signing algorithm, not the business call, is
// the hard part of talking to these APIs — and because several providers share
// one scheme. Tencent, Huawei, Volcengine and JDCloud all use a variant of
// AWS Signature Version 4, differing only in the algorithm name, the header
// prefix and the credential scope terminator. Writing SigV4 once and
// parameterising it turns four fiddly implementations into four small tables.
//
// Everything here is transport-level and free of business logic: a signer
// takes a request and credentials and produces headers. The DNS providers on
// top supply the URLs and payloads.
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

// SigV4Profile parameterises the AWS-derived signing schemes.
type SigV4Profile struct {
	// Algorithm is the value of the Authorization prefix, e.g.
	// "TC3-HMAC-SHA256" for Tencent or "SDK-HMAC-SHA256" for Huawei.
	Algorithm string
	// KeyPrefix seeds the derived signing key, e.g. "TC3" for Tencent. Empty
	// means the secret is used directly, which is what Huawei does.
	KeyPrefix string
	// Terminator ends the credential scope, e.g. "tc3_request".
	Terminator string
	// DateHeader carries the request timestamp.
	DateHeader string
	// DateFormat renders DateHeader. Huawei uses a compact form; Tencent uses
	// a Unix timestamp and is handled by TimestampHeader instead.
	DateFormat string
	// TimestampHeader carries a Unix timestamp when the scheme wants one.
	TimestampHeader string
	// ScopeDate includes the yyyymmdd date in the credential scope.
	ScopeDate bool
	// ScopeRegionService adds region and service to the credential scope.
	ScopeRegionService bool
	// SignedHeaderPrefix limits which headers are signed. Empty signs the
	// canonical set below.
	SignedHeaderPrefix string
}

// SigV4Request is everything needed to sign one call.
type SigV4Request struct {
	Method  string
	URL     *url.URL
	Headers http.Header
	Payload []byte

	AccessKey string
	SecretKey string
	Region    string
	Service   string

	// Now is injectable so signing is testable against published fixtures.
	Now time.Time
}

// SignSigV4 adds the Authorization header for an AWS-derived scheme.
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

// canonicalRequest builds the canonical form and the signed-header list.
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

// timestampForSigning is what goes in the second line of the string to sign.
func timestampForSigning(profile SigV4Profile, now time.Time) string {
	if profile.TimestampHeader != "" {
		return fmt.Sprintf("%d", now.Unix())
	}
	if profile.DateFormat != "" {
		return now.Format(profile.DateFormat)
	}
	return now.Format("20060102T150405Z")
}

// deriveKey walks the chained-HMAC key derivation.
func deriveKey(profile SigV4Profile, now time.Time, req *SigV4Request) []byte {
	if profile.KeyPrefix == "" {
		// Huawei signs directly with the secret rather than deriving.
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

// CanonicalQuery renders query parameters in the sorted, percent-encoded form
// every one of these schemes expects.
//
// url.Values.Encode is close but not identical: it escapes a space as '+'
// where the signing specs require "%20", and a mismatch there produces a
// signature error with no indication of which byte was wrong.
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

// PercentEncode applies RFC 3986 unreserved-character rules.
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

// Profiles for the providers that share this scheme.
var (
	// TencentProfile is TC3-HMAC-SHA256, used by DNSPod and EdgeOne.
	TencentProfile = SigV4Profile{
		Algorithm:          "TC3-HMAC-SHA256",
		KeyPrefix:          "TC3",
		Terminator:         "tc3_request",
		TimestampHeader:    "X-TC-Timestamp",
		ScopeDate:          true,
		ScopeRegionService: false,
	}

	// HuaweiProfile is SDK-HMAC-SHA256. It signs with the secret directly and
	// uses a compact date header rather than a Unix timestamp.
	HuaweiProfile = SigV4Profile{
		Algorithm:  "SDK-HMAC-SHA256",
		DateHeader: "X-Sdk-Date",
		DateFormat: "20060102T150405Z",
	}

	// VolcengineProfile follows AWS closely, including region and service in
	// the credential scope.
	VolcengineProfile = SigV4Profile{
		Algorithm:          "HMAC-SHA256",
		Terminator:         "request",
		DateHeader:         "X-Date",
		DateFormat:         "20060102T150405Z",
		ScopeDate:          true,
		ScopeRegionService: true,
	}

	// JDCloudProfile is JDCLOUD2-HMAC-SHA256.
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
