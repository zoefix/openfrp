// Package cert issues and manages TLS certificates.
//
// Issuance runs on the router rather than the server. That is unusual for a
// tunnel product and it is deliberate: the router is where the DNS credentials
// already live, it needs no inbound connectivity for a DNS-01 challenge, and
// it keeps the public server free of any credential worth stealing. The server
// receives only the finished certificate.
package cert

import (
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"fmt"

	"github.com/go-acme/lego/v4/certcrypto"
)

// KeyType is the algorithm and size of a certificate's private key.
type KeyType string

const (
	KeyRSA2048  KeyType = "rsa2048"
	KeyRSA3072  KeyType = "rsa3072"
	KeyRSA4096  KeyType = "rsa4096"
	KeyECDSA256 KeyType = "ec256"
	KeyECDSA384 KeyType = "ec384"
)

// DefaultKeyType is ECDSA P-256.
//
// Smaller and faster than RSA at equivalent strength, and universally
// supported by anything that can speak TLS 1.2. RSA remains available for the
// rare embedded client that still cannot do elliptic curve.
const DefaultKeyType = KeyECDSA256

// Valid reports whether k is a recognised key type.
func (k KeyType) Valid() bool {
	switch k {
	case KeyRSA2048, KeyRSA3072, KeyRSA4096, KeyECDSA256, KeyECDSA384:
		return true
	}
	return false
}

// Label returns a human description.
func (k KeyType) Label() string {
	switch k {
	case KeyRSA2048:
		return "RSA 2048"
	case KeyRSA3072:
		return "RSA 3072"
	case KeyRSA4096:
		return "RSA 4096"
	case KeyECDSA256:
		return "ECDSA P-256"
	case KeyECDSA384:
		return "ECDSA P-384"
	default:
		return string(k)
	}
}

// legoKeyType maps onto lego's own enumeration.
func (k KeyType) legoKeyType() (certcrypto.KeyType, error) {
	switch k {
	case KeyRSA2048:
		return certcrypto.RSA2048, nil
	case KeyRSA3072:
		return certcrypto.RSA3072, nil
	case KeyRSA4096:
		return certcrypto.RSA4096, nil
	case KeyECDSA256, "":
		return certcrypto.EC256, nil
	case KeyECDSA384:
		return certcrypto.EC384, nil
	default:
		return "", fmt.Errorf("cert: unknown key type %q", k)
	}
}

// GenerateKey creates a private key of this type.
func (k KeyType) GenerateKey() (crypto.PrivateKey, error) {
	switch k {
	case KeyRSA2048:
		return rsa.GenerateKey(rand.Reader, 2048)
	case KeyRSA3072:
		return rsa.GenerateKey(rand.Reader, 3072)
	case KeyRSA4096:
		return rsa.GenerateKey(rand.Reader, 4096)
	case KeyECDSA256, "":
		return ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	case KeyECDSA384:
		return ecdsa.GenerateKey(elliptic.P384(), rand.Reader)
	default:
		return nil, fmt.Errorf("cert: unknown key type %q", k)
	}
}

// KeyTypes lists the supported key types in display order.
func KeyTypes() []KeyType {
	return []KeyType{KeyECDSA256, KeyECDSA384, KeyRSA2048, KeyRSA3072, KeyRSA4096}
}
