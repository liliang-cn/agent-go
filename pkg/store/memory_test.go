package store

import (
	"context"
	"path/filepath"
	"sync"
	"testing"
)

func TestNewMemoryStoreConcurrentOpen(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "memory.db")

	const workers = 6
	errCh := make(chan error, workers)
	var wg sync.WaitGroup

	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()

			store, err := NewMemoryStore(dbPath)
			if err != nil {
				errCh <- err
				return
			}
			defer store.Close()

			if err := store.InitSchema(context.Background()); err != nil {
				errCh <- err
				return
			}

			if _, _, err := store.List(context.Background(), 10, 0); err != nil {
				errCh <- err
				return
			}
		}()
	}

	wg.Wait()
	close(errCh)

	for err := range errCh {
		if err != nil {
			t.Fatalf("concurrent memory store open failed: %v", err)
		}
	}
}
