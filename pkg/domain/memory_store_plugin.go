package domain

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
)

// ErrMemoryStoreUnsupported is returned by a MemoryStore method the backend
// cannot honour. It is the honest answer to "this backend has no such
// capability" — callers are expected to degrade rather than to treat it as a
// hard failure. UnsupportedMemoryStore returns it from every method, so an
// embedder only has to override the parts its backend really implements.
var ErrMemoryStoreUnsupported = errors.New("memory store: operation not supported by this backend")

// IsMemoryStoreUnsupported reports whether err came from a backend declining an
// operation it does not implement.
func IsMemoryStoreUnsupported(err error) bool {
	return errors.Is(err, ErrMemoryStoreUnsupported)
}

// UnsupportedMemoryStore implements every MemoryStore method by returning
// ErrMemoryStoreUnsupported. Embed it to write a backend that only implements
// the handful of operations it can actually serve:
//
//	type MyStore struct{ domain.UnsupportedMemoryStore }
//	func (s *MyStore) Store(...)  { ... }
//	func (s *MyStore) SearchByText(...) { ... }
//	func (s *MyStore) Get(...) { ... }
//
// It is exported from pkg/memory as memory.BaseStore, which is the name the
// docs use; both are the same type.
type UnsupportedMemoryStore struct{}

var _ MemoryStore = (*UnsupportedMemoryStore)(nil)

func (UnsupportedMemoryStore) Store(context.Context, *Memory) error { return ErrMemoryStoreUnsupported }

func (UnsupportedMemoryStore) Search(context.Context, []float64, int, float64) ([]*MemoryWithScore, error) {
	return nil, ErrMemoryStoreUnsupported
}

func (UnsupportedMemoryStore) SearchBySession(context.Context, string, []float64, int) ([]*MemoryWithScore, error) {
	return nil, ErrMemoryStoreUnsupported
}

func (UnsupportedMemoryStore) SearchByScope(context.Context, []float64, []MemoryScope, int) ([]*MemoryWithScore, error) {
	return nil, ErrMemoryStoreUnsupported
}

func (UnsupportedMemoryStore) StoreWithScope(context.Context, *Memory, MemoryScope) error {
	return ErrMemoryStoreUnsupported
}

func (UnsupportedMemoryStore) SearchByText(context.Context, string, int) ([]*MemoryWithScore, error) {
	return nil, ErrMemoryStoreUnsupported
}

func (UnsupportedMemoryStore) Get(context.Context, string) (*Memory, error) {
	return nil, ErrMemoryStoreUnsupported
}

func (UnsupportedMemoryStore) Update(context.Context, *Memory) error {
	return ErrMemoryStoreUnsupported
}

func (UnsupportedMemoryStore) IncrementAccess(context.Context, string) error {
	return ErrMemoryStoreUnsupported
}

func (UnsupportedMemoryStore) GetByType(context.Context, MemoryType, int) ([]*Memory, error) {
	return nil, ErrMemoryStoreUnsupported
}

func (UnsupportedMemoryStore) List(context.Context, int, int) ([]*Memory, int, error) {
	return nil, 0, ErrMemoryStoreUnsupported
}

func (UnsupportedMemoryStore) Delete(context.Context, string) error {
	return ErrMemoryStoreUnsupported
}

func (UnsupportedMemoryStore) Clear(context.Context) error { return ErrMemoryStoreUnsupported }

func (UnsupportedMemoryStore) DeleteBySession(context.Context, string) error {
	return ErrMemoryStoreUnsupported
}

func (UnsupportedMemoryStore) ConfigureBank(context.Context, string, *MemoryBankConfig) error {
	return ErrMemoryStoreUnsupported
}

func (UnsupportedMemoryStore) Reflect(context.Context, string) (string, error) {
	return "", ErrMemoryStoreUnsupported
}

func (UnsupportedMemoryStore) AddMentalModel(context.Context, *MentalModel) error {
	return ErrMemoryStoreUnsupported
}

func (UnsupportedMemoryStore) InitSchema(context.Context) error { return ErrMemoryStoreUnsupported }

// ============================================================
// Memory store registry
// ============================================================

// MemoryStoreConfig is everything a registered factory is handed when the
// builder resolves `store_type = "<name>"`. Path and DSN are the two common
// shapes (a local file/directory, or a connection string); Options carries
// anything else the backend needs, so a plugin never has to extend this struct.
type MemoryStoreConfig struct {
	// Name is the registered store_type that selected this factory.
	Name string

	// Path is the resolved local path for file/db-backed backends. Empty for
	// backends that are not local.
	Path string

	// DSN is the connection string for networked backends (an endpoint, a URL,
	// a libsql/postgres DSN...).
	DSN string

	// Options is free-form backend configuration, sourced from
	// agent.WithMemoryOption / agentgo.toml `[memory.options]`.
	Options map[string]string

	// Embedder and Generator are the services the agent was built with, in case
	// the backend wants to vectorise or summarise locally. Either may be nil.
	Embedder  Embedder
	Generator Generator
}

// Option returns the value of an option key, or "" when unset.
func (c MemoryStoreConfig) Option(key string) string { return c.Options[key] }

// OptionOr returns the value of an option key, or fallback when unset/blank.
func (c MemoryStoreConfig) OptionOr(key, fallback string) string {
	if v := strings.TrimSpace(c.Options[key]); v != "" {
		return v
	}
	return fallback
}

// MemoryStoreFactory builds a MemoryStore from a resolved configuration.
type MemoryStoreFactory func(cfg MemoryStoreConfig) (MemoryStore, error)

var memoryStoreRegistry = struct {
	sync.RWMutex
	factories map[string]MemoryStoreFactory
}{factories: map[string]MemoryStoreFactory{}}

// builtinMemoryStoreTypes are the names owned by pkg/config's switch. A plugin
// may not shadow them — the built-in branch would win anyway, so silently
// accepting the registration would be a lie.
var builtinMemoryStoreTypes = map[string]bool{
	"file":              true,
	"cortex":            true,
	"memoryflow":        true,
	"graphflow":         true,
	"cortex-memoryflow": true,
	"cortex-graphflow":  true,
	"vector":            true,
}

func normalizeMemoryStoreName(name string) string {
	return strings.ToLower(strings.TrimSpace(name))
}

// RegisterMemoryStore registers a memory backend under a store_type name.
// Registration is concurrency-safe and strict: a blank name, a nil factory, a
// built-in name, or a duplicate registration is an error rather than a silent
// overwrite, so two plugins fighting over one name is visible at startup.
// Use UnregisterMemoryStore to replace one deliberately.
func RegisterMemoryStore(name string, factory MemoryStoreFactory) error {
	key := normalizeMemoryStoreName(name)
	if key == "" {
		return errors.New("register memory store: name is required")
	}
	if factory == nil {
		return fmt.Errorf("register memory store %q: factory is nil", name)
	}
	if builtinMemoryStoreTypes[key] {
		return fmt.Errorf("register memory store %q: name is reserved for a built-in store type", name)
	}

	memoryStoreRegistry.Lock()
	defer memoryStoreRegistry.Unlock()
	if _, exists := memoryStoreRegistry.factories[key]; exists {
		return fmt.Errorf("register memory store %q: already registered", name)
	}
	memoryStoreRegistry.factories[key] = factory
	return nil
}

// MustRegisterMemoryStore is RegisterMemoryStore for package init(), where
// there is nobody to hand an error to.
func MustRegisterMemoryStore(name string, factory MemoryStoreFactory) {
	if err := RegisterMemoryStore(name, factory); err != nil {
		panic(err)
	}
}

// UnregisterMemoryStore removes a registration. It reports whether one existed.
func UnregisterMemoryStore(name string) bool {
	key := normalizeMemoryStoreName(name)
	memoryStoreRegistry.Lock()
	defer memoryStoreRegistry.Unlock()
	_, existed := memoryStoreRegistry.factories[key]
	delete(memoryStoreRegistry.factories, key)
	return existed
}

// LookupMemoryStore returns the factory registered under name.
func LookupMemoryStore(name string) (MemoryStoreFactory, bool) {
	key := normalizeMemoryStoreName(name)
	memoryStoreRegistry.RLock()
	defer memoryStoreRegistry.RUnlock()
	f, ok := memoryStoreRegistry.factories[key]
	return f, ok
}

// MemoryStoreRegistered reports whether a store_type name resolves to a plugin.
func MemoryStoreRegistered(name string) bool {
	_, ok := LookupMemoryStore(name)
	return ok
}

// RegisteredMemoryStores lists the registered plugin names, sorted.
func RegisteredMemoryStores() []string {
	memoryStoreRegistry.RLock()
	defer memoryStoreRegistry.RUnlock()
	names := make([]string, 0, len(memoryStoreRegistry.factories))
	for name := range memoryStoreRegistry.factories {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}
