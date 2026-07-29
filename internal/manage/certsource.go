package manage

import (
	"context"
	"fmt"

	"github.com/zoefix/openfrp/internal/cert"
	"github.com/zoefix/openfrp/internal/storage/repo"
	"github.com/zoefix/openfrp/internal/tunnel/client"
)

type CertSource struct {
	orders *repo.Orders
}

func (s *Service) NewCertSource() *CertSource {
	return &CertSource{orders: s.orders}
}

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
