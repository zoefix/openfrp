package dns

import (
	"context"
	"fmt"
	"sort"
	"sync"

	"github.com/zoefix/openfrp/pkg/schema"
)

type Provider interface {
	Check(ctx context.Context) error

	ListDomains(ctx context.Context, opts ListOptions) ([]Domain, error)

	ListRecords(ctx context.Context, zone string, opts ListOptions) ([]Record, error)

	AddRecord(ctx context.Context, zone string, record Record) (string, error)

	UpdateRecord(ctx context.Context, zone string, record Record) error

	DeleteRecord(ctx context.Context, zone, id string) error

	Capabilities() Capabilities
}

type StatusSetter interface {
	SetRecordStatus(ctx context.Context, zone, id string, enabled bool) error
}

type RemarkSetter interface {
	SetRecordRemark(ctx context.Context, zone, id, remark string) error
}

type ZoneCreator interface {
	AddDomain(ctx context.Context, zone string) error
}

type Descriptor struct {
	Key string `json:"key"`

	Label string `json:"label"`

	Form schema.Form `json:"form"`

	Capabilities Capabilities `json:"capabilities"`
}

type Factory func(values map[string]string) (Provider, error)

type registration struct {
	Descriptor
	factory Factory
}

var (
	registryMu sync.RWMutex
	registry   = map[string]registration{}
)

func Register(desc Descriptor, factory Factory) {
	registryMu.Lock()
	defer registryMu.Unlock()

	if desc.Key == "" {
		panic("dns: provider registered without a key")
	}
	if _, exists := registry[desc.Key]; exists {
		panic(fmt.Sprintf("dns: provider %q registered twice", desc.Key))
	}

	if len(desc.Capabilities.Lines) == 0 {
		desc.Capabilities.Lines = SupportedLines(desc.Key)
	}

	registry[desc.Key] = registration{Descriptor: desc, factory: factory}
}

func New(key string, values map[string]string) (Provider, error) {
	registryMu.RLock()
	entry, known := registry[key]
	registryMu.RUnlock()

	if !known {
		return nil, fmt.Errorf("dns: unknown provider %q", key)
	}

	values = entry.Form.ApplyDefaults(values)
	if err := entry.Form.Validate(values); err != nil {
		return nil, fmt.Errorf("dns: %s: %w", entry.Label, err)
	}
	return entry.factory(values)
}

func Describe(key string) (Descriptor, bool) {
	registryMu.RLock()
	defer registryMu.RUnlock()

	entry, known := registry[key]
	return entry.Descriptor, known
}

func Descriptors() []Descriptor {
	registryMu.RLock()
	out := make([]Descriptor, 0, len(registry))
	for _, entry := range registry {
		out = append(out, entry.Descriptor)
	}
	registryMu.RUnlock()

	sort.Slice(out, func(i, j int) bool { return out[i].Key < out[j].Key })
	return out
}

func Keys() []string {
	descriptors := Descriptors()
	keys := make([]string, len(descriptors))
	for i, d := range descriptors {
		keys[i] = d.Key
	}
	return keys
}
