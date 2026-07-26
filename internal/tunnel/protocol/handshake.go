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

// DefaultAuthSkew bounds how far a login timestamp may sit from the server's
// clock. It caps the window in which a captured AuthKey can be replayed, while
// staying loose enough to tolerate the clock drift that is routine on routers
// without a battery-backed RTC.
const DefaultAuthSkew = 15 * time.Minute

// AuthKey derives the proof of token possession for a given timestamp.
//
// The token itself never crosses the wire. An observer sees only an HMAC bound
// to one timestamp, which the receiver will refuse outside the skew window.
func AuthKey(token string, timestamp int64) string {
	mac := hmac.New(sha256.New, []byte(token))
	mac.Write([]byte(strconv.FormatInt(timestamp, 10)))
	return hex.EncodeToString(mac.Sum(nil))
}

// VerifyAuth checks a presented key against the expected token.
//
// It returns ErrAuthFailed for both a bad key and a stale timestamp, on
// purpose: distinguishing them would tell an attacker which half to keep
// working on.
func VerifyAuth(token, presented string, timestamp int64, now time.Time, skew time.Duration) error {
	if token == "" {
		// Authentication disabled. Only sane on a trusted network, and the
		// server logs a warning about it at startup.
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
	// Constant-time comparison: a byte-wise early return would leak the
	// correct prefix through timing.
	if !hmac.Equal([]byte(expected), []byte(presented)) {
		return fmt.Errorf("%w", ErrAuthFailed)
	}
	return nil
}

// CheckVersion reports whether a peer's protocol version is compatible.
//
// Compatibility is exact for now. Once there is a v2 in the wild this becomes
// a range check, which is why callers go through this function rather than
// comparing against Version directly.
func CheckVersion(peer int) error {
	if peer != Version {
		return fmt.Errorf("%w: peer speaks v%d, this build speaks v%d",
			ErrVersionMismatch, peer, Version)
	}
	return nil
}

// NewRunID mints the identifier that ties a client's work connections to its
// control connection, and lets a reconnecting client reclaim its identity
// rather than appearing as a second client.
func NewRunID() (string, error) {
	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("protocol: generate run id: %w", err)
	}
	return hex.EncodeToString(buf), nil
}
