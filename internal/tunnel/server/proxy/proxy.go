// Package proxy publishes one client tunnel on the server.
//
// Each proxy kind lives in its own file and registers itself through Register,
// so supporting a new kind means adding a file rather than editing a switch
// somewhere else.
package proxy

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"sort"
	"sync"

	"github.com/zoefix/openfrp/internal/tunnel/protocol"
	"github.com/zoefix/openfrp/internal/tunnel/vhost"
)

// WorkConnSource supplies connections back to the owning client.
//
// This interface is declared here, on the consuming side, rather than in the
// server package. That is what lets proxy stay free of any import of server
// and keeps the dependency pointing one way.
type WorkConnSource interface {
	// GetWorkConn returns a connection to the client, already told which proxy
	// it serves. The returned connection carries raw payload from that point
	// on, so it must not be wrapped in anything that would cost splice(2).
	GetWorkConn(ctx context.Context, proxyName, sourceAddr string) (net.Conn, error)
}

// Proxy is one published tunnel.
type Proxy interface {
	// Name is the client-assigned identifier, unique within a session.
	Name() string
	// RemotePort is the bound TCP or UDP port, or zero for a domain-routed
	// proxy that shares a vhost listener.
	RemotePort() int
	// Run serves until ctx is cancelled or a fatal error occurs.
	Run(ctx context.Context) error
	// Close releases the proxy's listeners.
	Close() error
}

// RouteRegistrar publishes and withdraws domain routes.
//
// Declared here, on the consuming side, so a proxy depends only on the two
// operations it actually performs rather than on the whole router.
type RouteRegistrar interface {
	Add(patterns []string, route vhost.Route) error
	Remove(runID, proxyName string)
	// RemoveClient withdraws every route a client owns, which is what a
	// disconnect needs. Remove alone cannot express it: it matches on both the
	// run ID and the proxy name.
	RemoveClient(runID string)
}

// Options carries everything a proxy needs at construction.
type Options struct {
	Spec   protocol.ProxySpec
	Source WorkConnSource
	Logger *slog.Logger

	// RunID identifies the owning client, so domain routes can be attributed
	// and withdrawn when it disconnects.
	RunID string

	// BindAddr is the address the server binds port-based proxies on.
	BindAddr string
	// AcceptLoops is how many SO_REUSEPORT accept loops a port-based proxy
	// should run. Zero means one per CPU.
	AcceptLoops int
	// ReusePort enables the multi-accept path.
	ReusePort bool

	// Routes is where domain-routed proxies register themselves. Nil means the
	// server has no vhost listener configured, and publishing an http or https
	// tunnel will be refused with that explanation.
	Routes RouteRegistrar
	// VhostHTTPPort and VhostHTTPSPort are reported back to the client so it
	// can tell the user where the tunnel is reachable.
	VhostHTTPPort  int
	VhostHTTPSPort int
}

// Factory builds a proxy of one kind.
type Factory func(Options) (Proxy, error)

var (
	registryMu sync.RWMutex
	registry   = map[string]Factory{}
)

// Register adds a proxy kind. It panics on a duplicate, because that can only
// be a programming error at init time.
func Register(kind string, f Factory) {
	registryMu.Lock()
	defer registryMu.Unlock()

	if _, exists := registry[kind]; exists {
		panic(fmt.Sprintf("proxy: kind %q registered twice", kind))
	}
	registry[kind] = f
}

// New builds a proxy for the kind named in opts.Spec.
func New(opts Options) (Proxy, error) {
	registryMu.RLock()
	factory, known := registry[opts.Spec.Kind]
	registryMu.RUnlock()

	if !known {
		return nil, fmt.Errorf("proxy: unsupported kind %q", opts.Spec.Kind)
	}
	if opts.Source == nil {
		return nil, fmt.Errorf("proxy: %q: no work connection source", opts.Spec.Name)
	}
	if opts.Logger == nil {
		opts.Logger = slog.Default()
	}
	return factory(opts)
}

// Kinds lists the registered proxy kinds, sorted for stable output.
func Kinds() []string {
	registryMu.RLock()
	defer registryMu.RUnlock()

	kinds := make([]string, 0, len(registry))
	for kind := range registry {
		kinds = append(kinds, kind)
	}
	sort.Strings(kinds)
	return kinds
}
