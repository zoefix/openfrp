package server

import (
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/zoefix/openfrp/internal/tunnel/vhost"
)

type CertStore struct {
	current atomic.Pointer[certTable]

	mu    sync.Mutex
	certs map[string]*storedCert
}

type storedCert struct {
	pattern     string
	certificate *tls.Certificate
	notAfter    time.Time
	domains     []string
}

type certTable struct {
	exact map[string]*storedCert

	wildcards map[string]*storedCert
	count     int
}

func NewCertStore() *CertStore {
	store := &CertStore{certs: map[string]*storedCert{}}
	store.current.Store(&certTable{
		exact:     map[string]*storedCert{},
		wildcards: map[string]*storedCert{},
	})
	return store
}

func (s *CertStore) Install(fullchainPEM, privateKeyPEM []byte) ([]string, error) {
	pair, err := tls.X509KeyPair(fullchainPEM, privateKeyPEM)
	if err != nil {
		return nil, fmt.Errorf("server: certificate and key do not form a valid pair: %w", err)
	}

	leaf, err := x509.ParseCertificate(pair.Certificate[0])
	if err != nil {
		return nil, fmt.Errorf("server: parse certificate: %w", err)
	}
	pair.Leaf = leaf

	if len(leaf.DNSNames) == 0 {
		return nil, fmt.Errorf("server: certificate carries no DNS names")
	}

	if time.Now().After(leaf.NotAfter) {
		return nil, fmt.Errorf("server: certificate expired on %s",
			leaf.NotAfter.Format(time.RFC3339))
	}

	s.mu.Lock()
	installed := make([]string, 0, len(leaf.DNSNames))
	for _, name := range leaf.DNSNames {
		pattern := strings.ToLower(strings.TrimSuffix(name, "."))
		s.certs[pattern] = &storedCert{
			pattern:     pattern,
			certificate: &pair,
			notAfter:    leaf.NotAfter,
			domains:     leaf.DNSNames,
		}
		installed = append(installed, pattern)
	}
	s.rebuildLocked()
	s.mu.Unlock()

	return installed, nil
}

func (s *CertStore) rebuildLocked() {
	next := &certTable{
		exact:     make(map[string]*storedCert, len(s.certs)),
		wildcards: make(map[string]*storedCert, len(s.certs)),
	}

	for pattern, entry := range s.certs {
		if zone, found := strings.CutPrefix(pattern, "*."); found {
			next.wildcards[zone] = entry
		} else {
			next.exact[pattern] = entry
		}
		next.count++
	}

	s.current.Store(next)
}

func (s *CertStore) GetCertificate(hello *tls.ClientHelloInfo) (*tls.Certificate, error) {
	name := strings.ToLower(strings.TrimSuffix(hello.ServerName, "."))
	if name == "" {
		return nil, fmt.Errorf("server: no SNI in the client hello, so no certificate can be chosen")
	}

	table := s.current.Load()

	if entry, found := table.exact[name]; found {
		return entry.certificate, nil
	}

	if idx := strings.IndexByte(name, '.'); idx > 0 {
		parent := name[idx+1:]
		if entry, found := table.wildcards[parent]; found {
			return entry.certificate, nil
		}
	}

	return nil, fmt.Errorf("server: no certificate covers %q", name)
}

func (s *CertStore) Has(name string) bool {
	_, err := s.GetCertificate(&tls.ClientHelloInfo{ServerName: name})
	return err == nil
}

func (s *CertStore) Len() int { return s.current.Load().count }

type Entry struct {
	Pattern  string    `json:"pattern"`
	Domains  []string  `json:"domains"`
	NotAfter time.Time `json:"not_after"`
}

func (s *CertStore) Entries() []Entry {
	s.mu.Lock()
	defer s.mu.Unlock()

	out := make([]Entry, 0, len(s.certs))
	for pattern, entry := range s.certs {
		out = append(out, Entry{
			Pattern:  pattern,
			Domains:  entry.domains,
			NotAfter: entry.notAfter,
		})
	}
	return out
}

func (s *CertStore) TLSConfig() *tls.Config {
	return &tls.Config{
		GetCertificate: s.GetCertificate,
		MinVersion:     tls.VersionTLS12,
		NextProtos:     []string{"http/1.1"},
	}
}

func terminationRoute(route *vhost.Route) bool {
	return route != nil && route.TLSMode == "terminate"
}
