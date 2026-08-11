package agent

import "github.com/liliang-cn/agent-go/v3/pkg/domain"

// ============================================================
// Pluggable memory backends
// ============================================================
//
// A memory backend can be plugged in at three levels, in decreasing order of
// how much the framework still does for you:
//
//  1. RegisterMemoryStore(name, factory) — the framework resolves
//     `store_type = "<name>"` from config to your factory and wraps the result
//     in the standard memory service. This is the one to reach for.
//  2. WithMemoryStore(store) — hand over an already-constructed
//     domain.MemoryStore instance; skips the registry and the factory.
//  3. WithMemoryService(service) — replace the memory service outright,
//     including the retrieval/injection policy. The escape hatch.
//
// Backends that only implement a few of domain.MemoryStore's 18 methods should
// embed memory.BaseStore, which answers ErrMemoryStoreUnsupported for the rest.

// MemoryStoreConfig is what a registered factory receives once the builder has
// resolved paths, DSN and options for a run.
type MemoryStoreConfig = domain.MemoryStoreConfig

// MemoryStoreFactory builds a domain.MemoryStore from a MemoryStoreConfig.
type MemoryStoreFactory = domain.MemoryStoreFactory

// RegisterMemoryStore registers a memory backend under a store_type name.
// Afterwards, `store_type = "<name>"` in agentgo.toml — or
// WithMemory(WithMemoryStoreType("<name>")) — selects it.
//
// Registration is concurrency-safe and strict: a blank name, a nil factory, a
// built-in name ("file", "cortex", "memoryflow", "graphflow"), or a name that
// is already registered returns an error rather than silently overwriting.
// Call UnregisterMemoryStore first to replace one on purpose.
//
//	func init() {
//	    _ = agent.RegisterMemoryStore("redis", func(cfg agent.MemoryStoreConfig) (domain.MemoryStore, error) {
//	        return newRedisStore(cfg.DSN)
//	    })
//	}
func RegisterMemoryStore(name string, factory MemoryStoreFactory) error {
	return domain.RegisterMemoryStore(name, factory)
}

// MustRegisterMemoryStore is RegisterMemoryStore for package init().
func MustRegisterMemoryStore(name string, factory MemoryStoreFactory) {
	domain.MustRegisterMemoryStore(name, factory)
}

// UnregisterMemoryStore removes a registration and reports whether one existed.
func UnregisterMemoryStore(name string) bool { return domain.UnregisterMemoryStore(name) }

// LookupMemoryStore returns the factory registered under name.
func LookupMemoryStore(name string) (MemoryStoreFactory, bool) { return domain.LookupMemoryStore(name) }

// RegisteredMemoryStores lists the registered plugin store_type names, sorted.
func RegisteredMemoryStores() []string { return domain.RegisteredMemoryStores() }

// WithMemoryService replaces the whole memory service — retrieval, injection
// and storage policy included. Nothing in buildMemoryService runs; store_type,
// memory path and reflect threshold are all ignored. Prefer
// RegisterMemoryStore or WithMemoryStore unless you really want to own the
// injection policy too.
func (b *Builder) WithMemoryService(svc domain.MemoryService) *Builder {
	b.enableMemory = true
	b.memoryService = svc
	return b
}
