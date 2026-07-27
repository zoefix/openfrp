package cloudapi

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha1"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"net/url"
	"strings"
	"time"
)

// AliyunRPCRequest signs a call to one of Alibaba Cloud's older RPC-style
// endpoints, which is what the DNS, ESA and CDN APIs still use.
//
// The scheme predates SigV4 and is unusual in two ways worth flagging, because
// both produce a signature error that says nothing about which part was wrong:
//
//   - The string to sign wraps a percent-encoded copy of the whole canonical
//     query, so every parameter ends up encoded twice.
//   - The HMAC key is the secret with a trailing "&" appended.
type AliyunRPCRequest struct {
	// Endpoint is the host, e.g. "alidns.aliyuncs.com".
	Endpoint string
	// Action is the RPC method, e.g. "DescribeDomainRecords".
	Action string
	// Version is the API version, e.g. "2015-01-09".
	Version string
	// Params carries the call's own arguments. The common parameters are
	// added here.
	Params map[string]string

	AccessKeyID     string
	AccessKeySecret string

	// Now and Nonce are injectable so the signature is testable.
	Now   time.Time
	Nonce string
}

// SignedURL returns the fully signed GET URL for the call.
func (r AliyunRPCRequest) SignedURL() (string, error) {
	if r.Endpoint == "" || r.Action == "" || r.Version == "" {
		return "", fmt.Errorf("cloudapi: aliyun request needs an endpoint, action and version")
	}

	now := r.Now
	if now.IsZero() {
		now = time.Now()
	}

	nonce := r.Nonce
	if nonce == "" {
		buf := make([]byte, 16)
		if _, err := rand.Read(buf); err != nil {
			return "", fmt.Errorf("cloudapi: generate nonce: %w", err)
		}
		nonce = hex.EncodeToString(buf)
	}

	values := url.Values{}
	for key, value := range r.Params {
		if value != "" {
			values.Set(key, value)
		}
	}

	values.Set("Action", r.Action)
	values.Set("Version", r.Version)
	values.Set("Format", "JSON")
	values.Set("AccessKeyId", r.AccessKeyID)
	values.Set("SignatureMethod", "HMAC-SHA1")
	values.Set("SignatureVersion", "1.0")
	values.Set("SignatureNonce", nonce)
	values.Set("Timestamp", now.UTC().Format("2006-01-02T15:04:05Z"))

	canonical := CanonicalQuery(values)

	// The canonical query is percent-encoded again as a whole before being
	// signed. This double encoding is the single most common reason an
	// otherwise correct Aliyun signature is rejected.
	stringToSign := "GET&" + PercentEncode("/") + "&" + PercentEncode(canonical)

	mac := hmac.New(sha1.New, []byte(r.AccessKeySecret+"&"))
	mac.Write([]byte(stringToSign))
	signature := base64.StdEncoding.EncodeToString(mac.Sum(nil))

	return fmt.Sprintf("https://%s/?Signature=%s&%s",
		r.Endpoint, PercentEncode(signature), canonical), nil
}

// BaiduBCERequest signs a Baidu Cloud call.
//
// Baidu uses its own bce-auth-v1 scheme: an authorization string that embeds
// the timestamp and expiry, with the signing key derived from that same
// prefix. It resembles SigV4 without matching it closely enough to share code.
type BaiduBCERequest struct {
	Method  string
	Host    string
	Path    string
	Query   url.Values
	Headers map[string]string

	AccessKeyID     string
	SecretAccessKey string

	// ExpiresSeconds bounds how long the signature stays valid.
	ExpiresSeconds int
	Now            time.Time
}

// AuthorizationHeader returns the value for the Authorization header.
func (r BaiduBCERequest) AuthorizationHeader() string {
	now := r.Now
	if now.IsZero() {
		now = time.Now()
	}
	expires := r.ExpiresSeconds
	if expires <= 0 {
		expires = 1800
	}

	prefix := fmt.Sprintf("bce-auth-v1/%s/%s/%d",
		r.AccessKeyID, now.UTC().Format("2006-01-02T15:04:05Z"), expires)

	signingKey := hex.EncodeToString(hmacSHA256([]byte(r.SecretAccessKey), []byte(prefix)))

	path := r.Path
	if path == "" {
		path = "/"
	}

	// Baidu signs the host header plus anything explicitly supplied, lowercased
	// and sorted.
	headers := map[string]string{"host": r.Host}
	for key, value := range r.Headers {
		headers[strings.ToLower(key)] = value
	}

	names := make([]string, 0, len(headers))
	for name := range headers {
		names = append(names, name)
	}
	sortStrings(names)

	var canonicalHeaders strings.Builder
	for i, name := range names {
		if i > 0 {
			canonicalHeaders.WriteByte('\n')
		}
		canonicalHeaders.WriteString(PercentEncode(name))
		canonicalHeaders.WriteByte(':')
		canonicalHeaders.WriteString(PercentEncode(strings.TrimSpace(headers[name])))
	}

	canonicalRequest := strings.Join([]string{
		strings.ToUpper(r.Method),
		path,
		CanonicalQuery(r.Query),
		canonicalHeaders.String(),
	}, "\n")

	signature := hex.EncodeToString(hmacSHA256([]byte(signingKey), []byte(canonicalRequest)))

	return fmt.Sprintf("%s/%s/%s", prefix, strings.Join(names, ";"), signature)
}

func sortStrings(values []string) {
	for i := 1; i < len(values); i++ {
		for j := i; j > 0 && values[j] < values[j-1]; j-- {
			values[j], values[j-1] = values[j-1], values[j]
		}
	}
}
