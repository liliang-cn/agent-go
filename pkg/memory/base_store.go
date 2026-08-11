package memory

import "github.com/liliang-cn/agent-go/v3/pkg/domain"

// BaseStore is an embeddable domain.MemoryStore whose every method returns
// ErrMemoryStoreUnsupported. Embed it and override only the operations your
// backend can actually serve — an 18-method interface should not be the price
// of admission for a store that only knows how to write, search and read.
//
//	type MyStore struct{ memory.BaseStore }
//
//	func (s *MyStore) Store(ctx context.Context, m *domain.Memory) error { ... }
//	func (s *MyStore) SearchByText(ctx context.Context, q string, k int) ([]*domain.MemoryWithScore, error) { ... }
//	func (s *MyStore) Get(ctx context.Context, id string) (*domain.Memory, error) { ... }
//
// The underlying type lives in pkg/domain so that pkg/store (which pkg/memory
// itself imports) can embed it without an import cycle.
type BaseStore = domain.UnsupportedMemoryStore

// ErrMemoryStoreUnsupported is returned by every BaseStore method that has not
// been overridden. Callers should degrade on it, not fail.
var ErrMemoryStoreUnsupported = domain.ErrMemoryStoreUnsupported

// IsUnsupported reports whether err is a backend declining an operation it does
// not implement.
func IsUnsupported(err error) bool { return domain.IsMemoryStoreUnsupported(err) }
