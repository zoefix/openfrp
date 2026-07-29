package protocol

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strconv"
	"time"
)

const DefaultAuthSkew = 15 * time.Minute

func AuthKey(token string, timestamp int64) string {
	mac := hmac.New(sha256.New, []byte(token))
	mac.Write([]byte(strconv.FormatInt(timestamp, 10)))
	return hex.EncodeToString(mac.Sum(nil))
}

func VerifyAuth(token, presented string, timestamp int64, now time.Time, skew time.Duration) error {
	if token == "" {

		return nil
	}

	if skew <= 0 {
		skew = DefaultAuthSkew
	}
	drift := now.Sub(time.Unix(timestamp, 0))
	if drift < 0 {
		drift = -drift
	}
	if drift > skew {
		return fmt.Errorf("%w", ErrAuthFailed)
	}

	expected := AuthKey(token, timestamp)

	if !hmac.Equal([]byte(expected), []byte(presented)) {
		return fmt.Errorf("%w", ErrAuthFailed)
	}
	return nil
}

func CheckVersion(peer int) error {
	if peer != Version {
		return fmt.Errorf("%w: peer speaks v%d, this build speaks v%d",
			ErrVersionMismatch, peer, Version)
	}
	return nil
}

func NewRunID() (string, error) {
	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("protocol: generate run id: %w", err)
	}
	return hex.EncodeToString(buf), nil
}
