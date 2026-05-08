// Package memory_test runs the cross-implementation conformance suite from
// internal/store against the volatile MemoryStore. Adding new behaviour to
// the Store interface should grow conformance.go (in package store), not this
// file — this file is intentionally a thin entry point.
package memory_test

import (
	"testing"

	"github.com/depinonbnb/depin/internal/store"
	"github.com/depinonbnb/depin/internal/store/memory"
)

// TestMemoryStoreConformance exercises every Store interface method against
// the in-memory implementation. The factory closure returns a fresh
// MemoryStore per subtest invocation.
func TestMemoryStoreConformance(t *testing.T) {
	store.Suite(t, func(t testing.TB) store.Store {
		return memory.NewMemory()
	})
}
