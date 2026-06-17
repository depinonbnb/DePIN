package memory

import (
	"testing"

	"github.com/depinonbnb/depin/internal/types"
)

func TestOneNodePerIP(t *testing.T) {
	s := NewMemory()
	defer s.Close()

	n1 := s.RegisterNode("0xa", types.BscFull, types.ExposedRPC, "http://x", "")
	s.SetNodeIP(n1.ID, "1.2.3.4")

	if !s.HasActiveNodeFromIP("1.2.3.4") {
		t.Fatal("should detect an active node from the registered IP")
	}
	if s.HasActiveNodeFromIP("5.6.7.8") {
		t.Fatal("a different IP should report no node")
	}
	if s.HasActiveNodeFromIP("") {
		t.Fatal("empty IP should always be false")
	}

	// Banning frees the address (banned sets IsActive=false).
	s.SetNodeCheatStatus(n1.ID, types.StatusBanned, "test")
	if s.HasActiveNodeFromIP("1.2.3.4") {
		t.Fatal("a banned node should no longer hold its IP")
	}
}
