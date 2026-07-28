package cloudflare

import (
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"os"
	"strings"
)

// tokenBlock is the PEM block cloudflared appends to the login credential.
const tokenBlock = "ARGO TUNNEL TOKEN"

// Credential is what a completed login granted.
//
// cloudflared writes it into cert.pem alongside the origin certificate, and
// uses it for exactly what is needed here: which zone was authorised, and a
// token scoped to it. Reading it back means the zone does not have to be
// asked for a second time, in a field where the operator could get it wrong.
type Credential struct {
	ZoneID    string `json:"zoneID"`
	AccountID string `json:"accountID"`
	APIToken  string `json:"apiToken"`
}

// ReadCredential parses the login credential.
func (c CLI) ReadCredential() (Credential, error) {
	raw, err := os.ReadFile(c.CertPath())
	if err != nil {
		return Credential{}, fmt.Errorf("cloudflare: %w", err)
	}
	return parseCredential(raw)
}

func parseCredential(raw []byte) (Credential, error) {
	rest := raw
	for {
		var block *pem.Block
		block, rest = pem.Decode(rest)
		if block == nil {
			break
		}
		if block.Type != tokenBlock {
			continue
		}

		// The payload is base64 inside the PEM body, which pem.Decode has
		// already decoded once — so what comes out here is the JSON itself.
		var credential Credential
		if err := json.Unmarshal(block.Bytes, &credential); err != nil {
			// Older cloudflared wrapped it in a second layer of base64.
			decoded, decodeErr := base64.StdEncoding.DecodeString(
				strings.TrimSpace(string(block.Bytes)))
			if decodeErr != nil {
				return Credential{}, fmt.Errorf(
					"cloudflare: the login credential holds a token this cannot read: %w", err)
			}
			if err := json.Unmarshal(decoded, &credential); err != nil {
				return Credential{}, fmt.Errorf(
					"cloudflare: the login credential holds a token this cannot read: %w", err)
			}
		}

		if credential.ZoneID == "" || credential.APIToken == "" {
			return Credential{}, fmt.Errorf(
				"cloudflare: the login credential names no zone; authorise again " +
					"and pick a domain rather than the whole account")
		}
		return credential, nil
	}

	// Deliberately does not quote the file: it holds a private key.
	return Credential{}, fmt.Errorf(
		"cloudflare: the login credential carries no %s block", tokenBlock)
}
