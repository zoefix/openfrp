package proxy

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/zoefix/openfrp/internal/tunnel/vhost"
)

func init() {
	Register("http", func(o Options) (Proxy, error) {
		return newVhostProxy(o, vhost.SchemeHTTP)
	})
	Register("https", func(o Options) (Proxy, error) {
		return newVhostProxy(o, vhost.SchemeHTTPS)
	})
}

// vhostProxy publishes a tunnel on a shared domain-routed listener.
//
// Unlike a tcp proxy it binds nothing of its own. The server runs one HTTP and
// one HTTPS listener for every client, and this type's whole job is to claim
// its domains in the routing table and release them again. That is what lets
// hundreds of tunnels share ports 80 and 443.
type vhostProxy struct {
	name    string
	runID   string
	scheme  vhost.Scheme
	domains []string
	tlsMode string
	port    int

	routes RouteRegistrar
	logger *slog.Logger

	registered bool
}

func newVhostProxy(opts Options, scheme vhost.Scheme) (Proxy, error) {
	spec := opts.Spec

	if len(spec.Domains) == 0 {
		return nil, fmt.Errorf("proxy %q: a %s tunnel needs at least one domain",
			spec.Name, scheme)
	}
	if opts.Routes == nil {
		return nil, fmt.Errorf(
			"proxy %q: this server has no %s vhost listener configured; "+
				"set vhost_%s_port to publish domain-routed tunnels",
			spec.Name, scheme, scheme)
	}

	// Validate every pattern up front so a bad domain is reported against the
	// tunnel that owns it, not as an opaque failure later.
	for _, domain := range spec.Domains {
		if _, err := vhost.ParsePattern(domain); err != nil {
			return nil, fmt.Errorf("proxy %q: %w", spec.Name, err)
		}
	}

	port := opts.VhostHTTPPort
	if scheme == vhost.SchemeHTTPS {
		port = opts.VhostHTTPSPort
	}

	return &vhostProxy{
		name:    spec.Name,
		runID:   opts.RunID,
		scheme:  scheme,
		domains: spec.Domains,
		tlsMode: spec.TLSMode,
		port:    port,
		routes:  opts.Routes,
		logger:  opts.Logger.With("proxy", spec.Name, "kind", string(scheme)),
	}, nil
}

func (p *vhostProxy) Name() string { return p.name }

// RemotePort reports the shared vhost port this tunnel answers on. It is not
// exclusive to this tunnel, unlike a tcp proxy's port.
func (p *vhostProxy) RemotePort() int { return p.port }

// Listen claims the domains. Registration happens here rather than in Run so a
// conflict is reported in the publish response, before the client is told the
// tunnel is live.
func (p *vhostProxy) Listen(context.Context) error {
	if err := p.routes.Add(p.domains, vhost.Route{
		RunID:     p.runID,
		ProxyName: p.name,
		TLSMode:   p.tlsMode,
	}); err != nil {
		return err
	}
	p.registered = true

	p.logger.Info("domains claimed", "domains", p.domains, "port", p.port)
	return nil
}

// Run blocks until cancelled. The shared listener does the serving; there is
// nothing per-proxy to accept.
func (p *vhostProxy) Run(ctx context.Context) error {
	if !p.registered {
		if err := p.Listen(ctx); err != nil {
			return err
		}
	}
	<-ctx.Done()
	return nil
}

func (p *vhostProxy) Close() error {
	if p.registered {
		p.routes.Remove(p.runID, p.name)
		p.registered = false
	}
	return nil
}

var _ binder = (*vhostProxy)(nil)
