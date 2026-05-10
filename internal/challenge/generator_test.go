// Package challenge: generator_test.go covers the basic API surface of the
// challenge generator and — most importantly — the concurrency regression
// flagged in Phase 2's handoff. The generator embeds a *rand.Rand which is
// explicitly NOT safe for concurrent use; the scheduler's challenge dispatcher
// (ADR-0007) fans verifications out across goroutines, so a mutex must guard
// every rng.* call. TestGenerateChallengeConcurrent fires N goroutines at a
// single Generator and relies on `go test -race` to catch any leak.
package challenge

import (
	"sync"
	"testing"

	"github.com/depinonbnb/depin/internal/types"
)

// TestGenerateChallengeBasic exercises the happy path so we have at least one
// signal that the function returns a populated challenge of the expected type.
func TestGenerateChallengeBasic(t *testing.T) {
	g := NewGenerator()
	ch := g.GenerateChallenge("node-1", types.BscFull)

	if ch == nil {
		t.Fatal("GenerateChallenge returned nil")
	}
	if ch.NodeID != "node-1" {
		t.Errorf("node id mismatch: got %q", ch.NodeID)
	}
	if ch.ID == "" {
		t.Error("challenge ID should not be empty")
	}
	if ch.ExpiresAt <= ch.CreatedAt {
		t.Errorf("ExpiresAt %d must be after CreatedAt %d", ch.ExpiresAt, ch.CreatedAt)
	}

	switch ch.ChallengeType {
	case types.BlockHash, types.BlockData, types.SyncStatus, types.StateBalance:
		// OK
	default:
		t.Errorf("unexpected challenge type %q for BscFull", ch.ChallengeType)
	}
}

// TestGenerateBatch verifies the convenience wrapper produces the requested
// number of challenges.
func TestGenerateBatch(t *testing.T) {
	g := NewGenerator()
	batch := g.GenerateBatch("node-1", types.BscArchive, 8)
	if len(batch) != 8 {
		t.Errorf("expected 8 challenges, got %d", len(batch))
	}
	seen := make(map[string]struct{}, len(batch))
	for _, ch := range batch {
		if _, dup := seen[ch.ID]; dup {
			t.Errorf("duplicate challenge id %q in batch", ch.ID)
		}
		seen[ch.ID] = struct{}{}
	}
}

// TestGenerateChallengeConcurrent is the regression test for the Phase 2 race
// condition. Without the mutex around the embedded *rand.Rand this test will
// either trip Go's race detector or, on tooling that doesn't run -race,
// occasionally panic inside math/rand. With the mutex it is rock-stable.
//
// Run with:
//
//	go test ./internal/challenge -race -run TestGenerateChallengeConcurrent
//
// The default Go test runner already runs at -race for this project.
func TestGenerateChallengeConcurrent(t *testing.T) {
	g := NewGenerator()

	// 200 goroutines × 50 challenges each = 10k Generate calls.
	const goroutines = 200
	const perGoroutine = 50

	var wg sync.WaitGroup
	wg.Add(goroutines)

	// Vary the node type so we exercise every getAvailableChallengeTypes branch
	// in parallel. types.NodeType keys map onto the generator's switch.
	nodeTypes := []types.NodeType{
		types.BscArchive,
		types.BscFull,
		types.BscFast,
		types.OpbnbFull,
		types.OpbnbFast,
	}

	errs := make(chan error, goroutines*perGoroutine)
	for i := 0; i < goroutines; i++ {
		nt := nodeTypes[i%len(nodeTypes)]
		go func(nt types.NodeType) {
			defer wg.Done()
			for j := 0; j < perGoroutine; j++ {
				ch := g.GenerateChallenge("node", nt)
				if ch == nil || ch.ID == "" {
					errs <- raceError("nil challenge or empty ID")
					return
				}
			}
		}(nt)
	}

	wg.Wait()
	close(errs)
	for err := range errs {
		t.Error(err)
	}
}

// raceError exists so the goroutines above don't have to import t directly
// (which would be racy in its own way).
type raceErrorString string

func (e raceErrorString) Error() string { return string(e) }

func raceError(msg string) error { return raceErrorString(msg) }
