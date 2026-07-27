package dns

import (
	"context"
	"fmt"
	"sort"
	"sync"

	"github.com/zoefix/openfrp/pkg/schema"
)

// Provider talks to one DNS service's API.
//
// Every method takes the zone name rather than an opaque handle, because some
// providers key on the name and others on an ID they hand out; keeping the
// name as the currency lets each provider resolve that internally and keeps
// the caller from having to care.
type Provider interface {
	// Check verifies the credentials without changing anything. This is what
	// the "test connection" button calls.
	Check(ctx context.Context) error

	// ListDomains returns the zones this account can manage.
	ListDomains(ctx context.Context, opts ListOptions) ([]Domain, error)

	// ListRecords returns the records in a zone.
	ListRecords(ctx context.Context, zone string, opts ListOptions) ([]Record, error)

	// AddRecord creates a record and returns its provider ID.
	AddRecord(ctx context.Context, zone string, record Record) (string, error)

	// UpdateRecord modifies an existing record, identified by record.ID.
	UpdateRecord(ctx context.Context, zone string, record Record) error

	// DeleteRecord removes a record.
	DeleteRecord(ctx context.Context, zone, id string) error

	// Capabilities describes what this provider supports, so the UI can hide
	// controls rather than offer them and fail on save.
	Capabilities() Capabilities
}

// StatusSetter is implemented by providers that can pause a record without
// deleting it. Declared separately because most cannot.
type StatusSetter interface {
	SetRecordStatus(ctx context.Context, zone, id string, enabled bool) error
}

// RemarkSetter is implemented by providers whose remarks need their own call.
type RemarkSetter interface {
	SetRecordRemark(ctx context.Context, zone, id, remark string) error
}

// ZoneCreator is implemented by providers that can create a zone.
type ZoneCreator interface {
	AddDomain(ctx context.Context, zone string) error
}

// Descriptor is everything the UI needs to offer a provider.
type Descriptor struct {
	// Key is the stable identifier stored in the database.
	Key string `json:"key"`
	// Label is the display name, in the provider's own language where that is
	// what operators actually call it.
	Label string `json:"label"`
	// Form declares the credential fields.
	Form schema.Form `json:"form"`
	// Capabilities is a static description; a provider whose capabilities vary
	// by account reports the superset here and rejects at call time.
	Capabilities Capabilities `json:"capabilities"`
}

// Factory builds a provider from validated credential values.
type Factory func(values map[string]string) (Provider, error)

type registration struct {
	Descriptor
	factory Factory
}

var (
	registryMu sync.RWMutex
	registry   = map[string]registration{}
)

// Register adds a provider. It panics on a duplicate key, which can only be a
// programming error at init time.
func Register(desc Descriptor, factory Factory) {
	registryMu.Lock()
	defer registryMu.Unlock()

	if desc.Key == "" {
		panic("dns: provider registered without a key")
	}
	if _, exists := registry[desc.Key]; exists {
		panic(fmt.Sprintf("dns: provider %q registered twice", desc.Key))
	}

	// Fill the line list from the shared table so a provider cannot drift out
	// of step with the mapping that actually governs its records.
	if len(desc.Capabilities.Lines) == 0 {
		desc.Capabilities.Lines = SupportedLines(desc.Key)
	}

	registry[desc.Key] = registration{Descriptor: desc, factory: factory}
}

// New builds a provider from stored credentials.
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

// Describe returns one provider's descriptor.
func Describe(key string) (Descriptor, bool) {
	registryMu.RLock()
	defer registryMu.RUnlock()

	entry, known := registry[key]
	return entry.Descriptor, known
}

// Descriptors lists every registered provider, ordered by key so the UI is
// stable between reloads.
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

// Keys lists the registered provider keys.
func Keys() []string {
	descriptors := Descriptors()
	keys := make([]string, len(descriptors))
	for i, d := range descriptors {
		keys[i] = d.Key
	}
	return keys
}
