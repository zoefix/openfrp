package cloudflare

import (
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"os"
	"strings"
)

const tokenBlock = "ARGO TUNNEL TOKEN"

type Credential struct {
	ZoneID    string `json:"zoneID"`
	AccountID string `json:"accountID"`
	APIToken  string `json:"apiToken"`
}

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

		var credential Credential
		if err := json.Unmarshal(block.Bytes, &credential); err != nil {

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

	return Credential{}, fmt.Errorf(
		"cloudflare: the login credential carries no %s block", tokenBlock)
}
