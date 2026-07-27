package manage

import (
	"context"
	"fmt"

	"github.com/zoefix/openfrp/internal/cert"
	"github.com/zoefix/openfrp/internal/storage/repo"
	"github.com/zoefix/openfrp/internal/tunnel/client"
)

// CertSource resolves a tunnel's certificate binding out of the database.
//
// It is the join between the certificate domain and the tunnel client, and it
// lives here rather than in either of them: the client declares the narrow
// interface it needs and knows nothing about SQLite, while the certificate
// package knows nothing about tunnels.
type CertSource struct {
	orders *repo.Orders
}

// NewCertSource returns a source backed by the same database as the service.
func (s *Service) NewCertSource() *CertSource {
	return &CertSource{orders: s.orders}
}

// Certificate returns the material for an order.
//
// The domains reported are the ones on the certificate itself rather than the
// ones that were requested. They can differ — a CA may drop a name it will not
// sign — and the server routes on what it was actually given, so reporting the
// request would advertise coverage that does not exist.
func (c *CertSource) Certificate(ctx context.Context, id int64) (client.Certificate, error) {
	order, err := c.orders.Get(ctx, id)
	if err != nil {
		return client.Certificate{}, err
	}

	if len(order.Certificate) == 0 || len(order.PrivateKey) == 0 {
		return client.Certificate{}, fmt.Errorf(
			"manage: certificate %d has not been issued yet: %w", id, client.ErrNoCertificate)
	}

	material := &cert.Certificate{
		FullchainPEM:  order.Certificate,
		PrivateKeyPEM: order.PrivateKey,
	}

	domains := order.Domains
	notAfter := order.ExpiresAt
	if err := material.Populate(); err == nil {
		domains = material.Domains
		notAfter = material.NotAfter.Unix()
	}

	return client.Certificate{
		Domains:       domains,
		FullchainPEM:  order.Certificate,
		PrivateKeyPEM: order.PrivateKey,
		NotAfter:      notAfter,
	}, nil
}
