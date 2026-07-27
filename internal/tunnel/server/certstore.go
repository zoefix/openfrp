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

// CertStore holds the certificates used for edge TLS termination and swaps
// them without disturbing traffic.
//
// This is the mechanism behind the project's clearest advantage over frp.
// frps loads its certificates once at startup, so rotating one means
// restarting the process and dropping every connected client
// (github.com/fatedier/frp#2946). Here a certificate arrives over the control
// connection, is validated, and replaces the previous one through a single
// atomic pointer swap — connections already in flight are untouched, and the
// next handshake picks up the new material.
type CertStore struct {
	// current is swapped wholesale. Readers never take a lock, which matters
	// because this is consulted inside the TLS handshake of every inbound
	// connection.
	current atomic.Pointer[certTable]

	mu    sync.Mutex
	certs map[string]*storedCert
}

type storedCert struct {
	// pattern is the normalised name this entry was filed under.
	pattern     string
	certificate *tls.Certificate
	notAfter    time.Time
	domains     []string
}

// certTable is an immutable snapshot.
type certTable struct {
	// exact maps a hostname to its certificate.
	exact map[string]*storedCert
	// wildcards maps a parent zone to a certificate covering "*.zone".
	wildcards map[string]*storedCert
	count     int
}

// NewCertStore returns an empty store.
func NewCertStore() *CertStore {
	store := &CertStore{certs: map[string]*storedCert{}}
	store.current.Store(&certTable{
		exact:     map[string]*storedCert{},
		wildcards: map[string]*storedCert{},
	})
	return store
}

// Install validates and stores a certificate, replacing any it supersedes.
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
	// An already-expired certificate is refused rather than installed and left
	// to fail every handshake — the push is the moment to say so.
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

// rebuildLocked recomputes the immutable table and publishes it.
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

// GetCertificate implements tls.Config.GetCertificate.
//
// Matching follows the same single-label wildcard rule the router uses, so a
// name that routes to a tunnel is exactly a name whose certificate covers it.
// Diverging here would produce a browser certificate error that looks like a
// routing bug and is not.
func (s *CertStore) GetCertificate(hello *tls.ClientHelloInfo) (*tls.Certificate, error) {
	name := strings.ToLower(strings.TrimSuffix(hello.ServerName, "."))
	if name == "" {
		return nil, fmt.Errorf("server: no SNI in the client hello, so no certificate can be chosen")
	}

	table := s.current.Load()

	if entry, found := table.exact[name]; found {
		return entry.certificate, nil
	}

	// A wildcard covers exactly one label, so only the immediate parent zone
	// is considered.
	if idx := strings.IndexByte(name, '.'); idx > 0 {
		parent := name[idx+1:]
		if entry, found := table.wildcards[parent]; found {
			return entry.certificate, nil
		}
	}

	return nil, fmt.Errorf("server: no certificate covers %q", name)
}

// Has reports whether a hostname can be served.
func (s *CertStore) Has(name string) bool {
	_, err := s.GetCertificate(&tls.ClientHelloInfo{ServerName: name})
	return err == nil
}

// Len reports how many patterns are covered.
func (s *CertStore) Len() int { return s.current.Load().count }

// Entry describes one stored certificate, for the status view.
type Entry struct {
	Pattern  string    `json:"pattern"`
	Domains  []string  `json:"domains"`
	NotAfter time.Time `json:"not_after"`
}

// Entries returns a snapshot of what is installed.
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

// TLSConfig returns a config that serves from this store.
// TLSConfig is the configuration used for edge termination.
//
// HTTP/1.1 only, deliberately. Terminating here decrypts the stream and then
// relays the plaintext to the tunnel unchanged — this is a byte pipe, not an
// HTTP server. Advertising h2 promises the visitor a protocol on this end of a
// connection whose other end was never asked whether it speaks it, and the
// visitor's h2 frames then land on whatever the LAN service happens to be.
//
// That is not theoretical: against an nginx backend it negotiated h2, forwarded
// the HTTP/2 preface, and nginx answered 421 Misdirected Request. Over HTTP/1.1
// the same request returned 200. A browser shows the 421, so the failure looks
// like a broken site rather than a protocol the proxy should not have offered.
//
// Offering h2 would mean either knowing the backend speaks h2c, or terminating
// HTTP/2 properly and re-issuing HTTP/1.1 requests — which would make this a
// real proxy and cost the splice path that edge termination already gives up
// enough of.
func (s *CertStore) TLSConfig() *tls.Config {
	return &tls.Config{
		GetCertificate: s.GetCertificate,
		MinVersion:     tls.VersionTLS12,
		NextProtos:     []string{"http/1.1"},
	}
}

// terminationRoute reports whether a route wants edge termination.
func terminationRoute(route *vhost.Route) bool {
	return route != nil && route.TLSMode == "terminate"
}
