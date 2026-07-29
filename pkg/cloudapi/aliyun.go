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

type AliyunRPCRequest struct {
	Endpoint string

	Action string

	Version string

	Params map[string]string

	AccessKeyID     string
	AccessKeySecret string

	Now   time.Time
	Nonce string
}

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

	stringToSign := "GET&" + PercentEncode("/") + "&" + PercentEncode(canonical)

	mac := hmac.New(sha1.New, []byte(r.AccessKeySecret+"&"))
	mac.Write([]byte(stringToSign))
	signature := base64.StdEncoding.EncodeToString(mac.Sum(nil))

	return fmt.Sprintf("https://%s/?Signature=%s&%s",
		r.Endpoint, PercentEncode(signature), canonical), nil
}

type BaiduBCERequest struct {
	Method  string
	Host    string
	Path    string
	Query   url.Values
	Headers map[string]string

	AccessKeyID     string
	SecretAccessKey string

	ExpiresSeconds int
	Now            time.Time
}

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
