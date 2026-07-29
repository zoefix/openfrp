package proxy

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"sort"
	"sync"

	"github.com/zoefix/openfrp/pkg/netutil"

	"github.com/zoefix/openfrp/internal/tunnel/protocol"
	"github.com/zoefix/openfrp/internal/tunnel/vhost"
)

type WorkConnSource interface {
	GetWorkConn(ctx context.Context, proxyName, sourceAddr string) (net.Conn, error)
}

type Limits interface {
	Rates(tunnel string) (toClient, toVisitor *netutil.Limiter)

	Exhausted(tunnel string) bool

	Spend(tunnel string, bytes int64)
}

type Recorder interface {
	RecordTransfer(name string, in, out int64, spliced bool)
	RecordProgress(name string, in, out int64)
	RecordClose(name string, spliced bool)
}

type Proxy interface {
	Name() string

	RemotePort() int

	Run(ctx context.Context) error

	Close() error
}

type RouteRegistrar interface {
	Add(patterns []string, route vhost.Route) error
	Remove(runID, proxyName string)

	RemoveClient(runID string)
}

type Options struct {
	Spec   protocol.ProxySpec
	Source WorkConnSource
	Logger *slog.Logger

	RunID string

	BindAddr string

	AcceptLoops int

	ReusePort bool

	Recorder Recorder

	Limits Limits

	Routes RouteRegistrar

	VhostHTTPPort  int
	VhostHTTPSPort int
}

type Factory func(Options) (Proxy, error)

var (
	registryMu sync.RWMutex
	registry   = map[string]Factory{}
)

func Register(kind string, f Factory) {
	registryMu.Lock()
	defer registryMu.Unlock()

	if _, exists := registry[kind]; exists {
		panic(fmt.Sprintf("proxy: kind %q registered twice", kind))
	}
	registry[kind] = f
}

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
