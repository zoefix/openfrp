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

type KeyType string

const (
	KeyRSA2048  KeyType = "rsa2048"
	KeyRSA3072  KeyType = "rsa3072"
	KeyRSA4096  KeyType = "rsa4096"
	KeyECDSA256 KeyType = "ec256"
	KeyECDSA384 KeyType = "ec384"
)

const DefaultKeyType = KeyECDSA256

func (k KeyType) Valid() bool {
	switch k {
	case KeyRSA2048, KeyRSA3072, KeyRSA4096, KeyECDSA256, KeyECDSA384:
		return true
	}
	return false
}

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

func KeyTypes() []KeyType {
	return []KeyType{KeyECDSA256, KeyECDSA384, KeyRSA2048, KeyRSA3072, KeyRSA4096}
}
