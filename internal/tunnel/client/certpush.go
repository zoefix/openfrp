package client

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"sync"
	"time"

	"github.com/zoefix/openfrp/internal/tunnel/protocol"
)

// ErrNoCertificate means the binding points at something that is not there —
// deleted, or not yet issued.
var ErrNoCertificate = errors.New("client: no certificate")

// Certificate is the material a tunnel terminating TLS needs the server to
// hold.
type Certificate struct {
	Domains       []string
	FullchainPEM  []byte
	PrivateKeyPEM []byte
	NotAfter      int64
}

// CertSource supplies certificates by the id a tunnel is bound to.
//
// Declared here, on the consuming side, so the tunnel client does not depend
// on the storage or management packages. cmd/ injects an implementation backed
// by SQLite; tests inject a map.
type CertSource interface {
	Certificate(ctx context.Context, id int64) (Certificate, error)
}

// SetCertSource attaches the source used to resolve tunnel certificate
// bindings. Without one, tunnels that terminate TLS are left to the server's
// existing certificates.
func (c *Client) SetCertSource(source CertSource) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.certs = source
}

// certPushInterval is how often bound certificates are re-read and pushed if
// they changed.
//
// Polling rather than being told: renewal runs in a separate process — the job
// worker — which has no control connection of its own. The alternative is
// restarting this daemon to pick up a renewed certificate, which would drop
// every tunnel to deliver a rotation whose entire purpose is not dropping
// anything.
const certPushInterval = time.Minute

// pushCertificates sends the certificate bound to each tunnel.
//
// Only tunnels with a binding are pushed. A tunnel that terminates TLS without
// one is a configuration the operator has not finished, and inventing a
// certificate for it would be a guess.
func (c *Client) pushCertificates(ctx context.Context, sess *session) {
	source := c.certSource()
	if source == nil {
		return
	}

	for _, tunnel := range c.cfg.Tunnels {
		if !tunnel.Enabled || tunnel.CertID == 0 {
			continue
		}

		material, err := source.Certificate(ctx, int64(tunnel.CertID))
		if err != nil {
			// Not fatal: the tunnel still serves, the server just has no
			// certificate for it. Saying so once per attempt beats failing the
			// whole connection over one misconfigured tunnel.
			c.logger.Warn("no certificate for tunnel",
				"tunnel", tunnel.Name, "cert_id", tunnel.CertID, "error", err)
			continue
		}

		if err := c.pushOne(ctx, sess, tunnel.Name, material); err != nil {
			c.logger.Warn("push certificate",
				"tunnel", tunnel.Name, "error", err)
		}
	}
}

// pushOne sends one certificate, skipping material the server already has.
func (c *Client) pushOne(ctx context.Context, sess *session,
	tunnel string, material Certificate) error {

	if len(material.FullchainPEM) == 0 || len(material.PrivateKeyPEM) == 0 {
		return ErrNoCertificate
	}

	// Re-pushing an unchanged certificate is harmless but not free: it costs a
	// control round trip and a store rebuild on the server every minute, per
	// tunnel. The digest covers the key as well, so a re-key with an identical
	// chain still counts as a change.
	digest := sha256.Sum256(append(append([]byte{},
		material.FullchainPEM...), material.PrivateKeyPEM...))
	fingerprint := hex.EncodeToString(digest[:])

	if c.alreadyPushed(tunnel, fingerprint) {
		return nil
	}

	if err := sess.codec.Write(&protocol.CertPush{
		Domains:       material.Domains,
		FullchainPEM:  material.FullchainPEM,
		PrivateKeyPEM: material.PrivateKeyPEM,
		NotAfter:      material.NotAfter,
	}); err != nil {
		return err
	}

	c.recordPushed(tunnel, fingerprint)
	c.logger.Info("pushed certificate",
		"tunnel", tunnel, "domains", material.Domains,
		"expires", time.Unix(material.NotAfter, 0).Format(time.RFC3339))
	return nil
}

// watchCertificates re-pushes bound certificates as they are renewed.
func (c *Client) watchCertificates(ctx context.Context, sess *session) {
	if c.certSource() == nil {
		return
	}

	ticker := time.NewTicker(certPushInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			// serve cancels this context when the session ends, so a
			// reconnect gets a fresh watcher rather than two.
			return
		case <-ticker.C:
			c.pushCertificates(ctx, sess)
		}
	}
}

func (c *Client) certSource() CertSource {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.certs
}

// pushed remembers what the current server session already holds, so a
// reconnect re-pushes and a steady state does not.
type pushed struct {
	mu     sync.Mutex
	byName map[string]string
}

func (p *pushed) seen(tunnel, fingerprint string) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.byName[tunnel] == fingerprint
}

func (p *pushed) record(tunnel, fingerprint string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.byName == nil {
		p.byName = map[string]string{}
	}
	p.byName[tunnel] = fingerprint
}

func (p *pushed) reset() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.byName = nil
}

func (c *Client) alreadyPushed(tunnel, fingerprint string) bool {
	return c.pushedCerts.seen(tunnel, fingerprint)
}

func (c *Client) recordPushed(tunnel, fingerprint string) {
	c.pushedCerts.record(tunnel, fingerprint)
}
