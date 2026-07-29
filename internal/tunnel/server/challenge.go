package server

import (
	"fmt"
	"net"
	"strings"
	"sync"
	"time"
)

const challengePrefix = "/.well-known/acme-challenge/"

const challengeTTL = time.Hour

type ChallengeStore struct {
	mu     sync.RWMutex
	tokens map[string]challenge
}

type challenge struct {
	keyAuth  string
	domain   string
	runID    string
	deadline time.Time
}

func NewChallengeStore() *ChallengeStore {
	return &ChallengeStore{tokens: map[string]challenge{}}
}

func (s *ChallengeStore) Publish(runID, domain, token, keyAuth string) error {
	if token == "" || keyAuth == "" {
		return fmt.Errorf("server: an HTTP challenge needs a token and a key authorisation")
	}

	if strings.ContainsAny(token, "/?#\\ ") {
		return fmt.Errorf("server: %q is not a usable challenge token", token)
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	s.expireLocked()
	s.tokens[token] = challenge{
		keyAuth:  keyAuth,
		domain:   domain,
		runID:    runID,
		deadline: time.Now().Add(challengeTTL),
	}
	return nil
}

func (s *ChallengeStore) Withdraw(runID, token string) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if existing, ok := s.tokens[token]; ok && existing.runID == runID {
		delete(s.tokens, token)
	}
}

func (s *ChallengeStore) Answer(path string) (string, bool) {
	if !strings.HasPrefix(path, challengePrefix) {
		return "", false
	}

	token := strings.TrimPrefix(path, challengePrefix)
	if token == "" || strings.Contains(token, "/") {
		return "", false
	}

	s.mu.RLock()
	defer s.mu.RUnlock()

	found, ok := s.tokens[token]
	if !ok || time.Now().After(found.deadline) {
		return "", false
	}
	return found.keyAuth, true
}

func (s *ChallengeStore) Len() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.tokens)
}

func (s *ChallengeStore) expireLocked() {
	now := time.Now()
	for token, existing := range s.tokens {
		if now.After(existing.deadline) {
			delete(s.tokens, token)
		}
	}
}

func answerChallenge(conn net.Conn, keyAuth string) {
	fmt.Fprintf(conn,
		"HTTP/1.1 200 OK\r\n"+
			"Content-Type: text/plain\r\n"+
			"Content-Length: %d\r\n"+
			"Connection: close\r\n\r\n%s",
		len(keyAuth), keyAuth)
}
