package cert

import (
	"context"
	"crypto"
	"crypto/ecdsa"
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/go-acme/lego/v4/certificate"
	"github.com/go-acme/lego/v4/lego"
	"github.com/go-acme/lego/v4/registration"
)

const (
	DirectoryLetsEncrypt        = "https://acme-v02.api.letsencrypt.org/directory"
	DirectoryLetsEncryptStaging = "https://acme-staging-v02.api.letsencrypt.org/directory"
	DirectoryZeroSSL            = "https://acme.zerossl.com/v2/DV90"
	DirectoryGoogle             = "https://dv.acme-v02.api.pki.goog/directory"
)

type CA struct {
	Key       string `json:"key"`
	Label     string `json:"label"`
	Directory string `json:"directory"`

	RequiresEAB bool `json:"requires_eab"`
}

func CAs() []CA {
	return []CA{
		{Key: "letsencrypt", Label: "Let's Encrypt", Directory: DirectoryLetsEncrypt},
		{Key: "letsencrypt-staging", Label: "Let's Encrypt (staging)",
			Directory: DirectoryLetsEncryptStaging},
		{Key: "zerossl", Label: "ZeroSSL", Directory: DirectoryZeroSSL, RequiresEAB: true},
		{Key: "google", Label: "Google Trust Services", Directory: DirectoryGoogle, RequiresEAB: true},
	}
}

func LookupCA(key string) (CA, bool) {
	for _, ca := range CAs() {
		if ca.Key == key {
			return ca, true
		}
	}
	return CA{}, false
}

type Account struct {
	Email        string `json:"email"`
	PrivateKey   []byte `json:"private_key"`
	Registration []byte `json:"registration,omitempty"`

	EABKeyID string `json:"eab_key_id,omitempty"`
	EABHMAC  string `json:"eab_hmac,omitempty"`

	key          crypto.PrivateKey
	registration *registration.Resource
}

func (a *Account) GetEmail() string { return a.Email }

func (a *Account) GetRegistration() *registration.Resource { return a.registration }

func (a *Account) GetPrivateKey() crypto.PrivateKey { return a.key }

func (a *Account) ensureKey() error {
	if a.key != nil {
		return nil
	}

	if len(a.PrivateKey) > 0 {
		block, _ := pem.Decode(a.PrivateKey)
		if block == nil {
			return fmt.Errorf("cert: account key is not valid PEM")
		}
		key, err := x509.ParseECPrivateKey(block.Bytes)
		if err != nil {
			return fmt.Errorf("cert: parse account key: %w", err)
		}
		a.key = key
		return nil
	}

	generated, err := KeyECDSA256.GenerateKey()
	if err != nil {
		return fmt.Errorf("cert: generate account key: %w", err)
	}

	ecKey, ok := generated.(*ecdsa.PrivateKey)
	if !ok {
		return fmt.Errorf("cert: unexpected account key type %T", generated)
	}
	a.key = ecKey

	encoded, err := x509.MarshalECPrivateKey(ecKey)
	if err != nil {
		return fmt.Errorf("cert: encode account key: %w", err)
	}
	a.PrivateKey = pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: encoded})
	return nil
}

type IssueRequest struct {
	Domains []string

	CA string

	KeyType KeyType

	Account *Account

	Solver ChallengeSolver

	HTTPSolver HTTPSolver

	PreferredChain string
}

type Issuer struct {
	logger *slog.Logger
}

func NewIssuer(logger *slog.Logger) *Issuer {
	if logger == nil {
		logger = slog.Default()
	}
	return &Issuer{logger: logger}
}

func (i *Issuer) Issue(ctx context.Context, req IssueRequest) (*Certificate, error) {
	domains := NormaliseDomains(req.Domains)
	if len(domains) == 0 {
		return nil, fmt.Errorf("cert: no domains requested")
	}

	if NeedsDNSChallenge(domains) && req.Solver == nil {
		wildcards := make([]string, 0, 1)
		for _, domain := range domains {
			if strings.HasPrefix(domain, "*.") {
				wildcards = append(wildcards, domain)
			}
		}
		return nil, fmt.Errorf(
			"cert: %s requires a DNS-01 challenge, so a DNS provider must be "+
				"configured; HTTP validation cannot prove control of names that "+
				"do not exist yet", strings.Join(wildcards, ", "))
	}

	if req.Solver == nil && req.HTTPSolver == nil {
		return nil, fmt.Errorf(
			"cert: no way to answer the authority's challenge; supply a DNS " +
				"solver or an HTTP one")
	}

	ca, err := resolveCA(req.CA)
	if err != nil {
		return nil, err
	}

	account := req.Account
	if account == nil {
		return nil, fmt.Errorf("cert: no ACME account supplied")
	}
	if err := account.ensureKey(); err != nil {
		return nil, err
	}

	keyType, err := req.KeyType.legoKeyType()
	if err != nil {
		return nil, err
	}

	config := lego.NewConfig(account)
	config.CADirURL = ca.Directory
	config.Certificate.KeyType = keyType

	config.Certificate.Timeout = 15 * time.Minute

	client, err := lego.NewClient(config)
	if err != nil {
		return nil, fmt.Errorf("cert: create ACME client: %w", err)
	}

	switch {
	case req.Solver != nil:

		solver := legoSolver{ctx: ctx, inner: req.Solver}
		if err := client.Challenge.SetDNS01Provider(solver); err != nil {
			return nil, fmt.Errorf("cert: configure DNS challenge: %w", err)
		}

	case req.HTTPSolver != nil:
		solver := legoHTTPSolver{ctx: ctx, inner: req.HTTPSolver}
		if err := client.Challenge.SetHTTP01Provider(solver); err != nil {
			return nil, fmt.Errorf("cert: configure HTTP challenge: %w", err)
		}
	}

	if err := i.register(ctx, client, account, ca); err != nil {
		return nil, err
	}

	i.logger.Info("requesting certificate",
		"domains", domains, "ca", ca.Label, "key_type", req.KeyType.Label())

	resource, err := client.Certificate.Obtain(certificate.ObtainRequest{
		Domains:        domains,
		Bundle:         true,
		PreferredChain: req.PreferredChain,
	})
	if err != nil {
		return nil, fmt.Errorf("cert: obtain certificate for %s: %w",
			strings.Join(domains, ", "), err)
	}

	issued := &Certificate{
		Domains:       domains,
		FullchainPEM:  resource.Certificate,
		PrivateKeyPEM: resource.PrivateKey,
	}
	if err := issued.Populate(); err != nil {
		return nil, err
	}

	i.logger.Info("certificate issued",
		"domains", domains, "expires", issued.NotAfter.Format(time.RFC3339),
		"issuer", issued.Issuer)

	return issued, nil
}

func (i *Issuer) register(_ context.Context, client *lego.Client, account *Account, ca CA) error {
	if account.registration != nil {
		return nil
	}

	if ca.RequiresEAB {
		if account.EABKeyID == "" || account.EABHMAC == "" {
			return fmt.Errorf(
				"cert: %s requires external account binding; supply the key ID "+
					"and HMAC from your account dashboard", ca.Label)
		}
		resource, err := client.Registration.RegisterWithExternalAccountBinding(
			registration.RegisterEABOptions{
				TermsOfServiceAgreed: true,
				Kid:                  account.EABKeyID,
				HmacEncoded:          account.EABHMAC,
			})
		if err != nil {
			return fmt.Errorf("cert: register with %s: %w", ca.Label, err)
		}
		account.registration = resource
		return nil
	}

	resource, err := client.Registration.Register(
		registration.RegisterOptions{TermsOfServiceAgreed: true})
	if err != nil {
		return fmt.Errorf("cert: register with %s: %w", ca.Label, err)
	}
	account.registration = resource
	return nil
}

func resolveCA(value string) (CA, error) {
	if value == "" {
		value = "letsencrypt"
	}

	if strings.HasPrefix(value, "http://") || strings.HasPrefix(value, "https://") {

		for _, ca := range CAs() {
			if ca.Directory == value {
				return ca, nil
			}
		}
		return CA{Key: value, Label: value, Directory: value}, nil
	}

	ca, known := LookupCA(value)
	if !known {
		return CA{}, fmt.Errorf("cert: unknown certificate authority %q", value)
	}
	return ca, nil
}
