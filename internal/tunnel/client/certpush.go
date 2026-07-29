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

var ErrNoCertificate = errors.New("client: no certificate")

type Certificate struct {
	Domains       []string
	FullchainPEM  []byte
	PrivateKeyPEM []byte
	NotAfter      int64
}

type CertSource interface {
	Certificate(ctx context.Context, id int64) (Certificate, error)
}

func (c *Client) SetCertSource(source CertSource) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.certs = source
}

const certPushInterval = time.Minute

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

func (c *Client) pushOne(ctx context.Context, sess *session,
	tunnel string, material Certificate) error {

	if len(material.FullchainPEM) == 0 || len(material.PrivateKeyPEM) == 0 {
		return ErrNoCertificate
	}

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

func (c *Client) watchCertificates(ctx context.Context, sess *session) {
	if c.certSource() == nil {
		return
	}

	ticker := time.NewTicker(certPushInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():

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
