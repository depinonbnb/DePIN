package reward_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/depinonbnb/depin/internal/reward"
	"github.com/depinonbnb/depin/internal/store/memory"
	"github.com/depinonbnb/depin/internal/types"
	"github.com/ethereum/go-ethereum/common"
)

// TestBuildCycle_NoEarners returns the sentinel error rather than a panic or a
// silent zero-leaf tree.
func TestBuildCycle_NoEarners(t *testing.T) {
	s := memory.NewMemory()
	defer s.Close()

	_, err := reward.BuildCycle(s, "cycle-1")
	if !errors.Is(err, reward.ErrNoEarners) {
		t.Errorf("expected ErrNoEarners, got %v", err)
	}
}

// TestBuildCycle_EmptyCycleIDError documents the basic input contract.
func TestBuildCycle_EmptyCycleIDError(t *testing.T) {
	s := memory.NewMemory()
	defer s.Close()
	if _, err := reward.BuildCycle(s, ""); err == nil {
		t.Error("expected error for empty cycle id")
	}
	if _, err := reward.BuildCycle(s, "   "); err == nil {
		t.Error("expected error for whitespace cycle id")
	}
}

// TestBuildCycle_AggregatesByWallet ensures multiple nodes belonging to the
// same wallet collapse into one leaf with summed points.
func TestBuildCycle_AggregatesByWallet(t *testing.T) {
	s := memory.NewMemory()
	defer s.Close()

	wallet := "0x1111111111111111111111111111111111111111"
	// Two nodes for the same wallet, each gets registration bonus + we'll
	// award uptime to push points.
	n1 := s.RegisterNode(wallet, types.BscFull, types.LocalProver, "", "")
	n2 := s.RegisterNode(wallet, types.BscArchive, types.LocalProver, "", "")
	s.AwardUptimePoints(n1.ID, 60)
	s.AwardUptimePoints(n2.ID, 60)

	// Different wallet
	otherWallet := "0x2222222222222222222222222222222222222222"
	n3 := s.RegisterNode(otherWallet, types.BscFast, types.LocalProver, "", "")
	s.AwardUptimePoints(n3.ID, 60)

	cycle, err := reward.BuildCycle(s, "cycle-7")
	if err != nil {
		t.Fatalf("BuildCycle: %v", err)
	}

	if cycle.NodeCount != 2 {
		t.Errorf("expected 2 wallets, got %d", cycle.NodeCount)
	}
	if cycle.TotalAmount.Sign() <= 0 {
		t.Error("expected positive total amount")
	}

	w := common.HexToAddress(wallet)
	pts, ok := cycle.PointsTotal[w]
	if !ok {
		t.Fatal("missing wallet entry")
	}
	// Two registration bonuses (50 + 100 = 150) plus at least one uptime
	// point per node. Just sanity-check it's larger than each bonus alone.
	if pts.Int64() < 150 {
		t.Errorf("expected aggregate >= 150 points, got %d", pts.Int64())
	}
	if !reward.VerifyProof(reward.Leaf{Wallet: w, Amount: pts}, cycle.Proofs[w], cycle.Root) {
		t.Error("VerifyProof failed for aggregate wallet")
	}
}

// TestBuildCycle_ExcludesBanned: banned nodes' points must not appear in the
// tree even if they accumulated them historically.
func TestBuildCycle_ExcludesBanned(t *testing.T) {
	s := memory.NewMemory()
	defer s.Close()

	bannedWallet := "0x3333333333333333333333333333333333333333"
	cleanWallet := "0x4444444444444444444444444444444444444444"

	banned := s.RegisterNode(bannedWallet, types.BscArchive, types.LocalProver, "", "")
	clean := s.RegisterNode(cleanWallet, types.BscFull, types.LocalProver, "", "")
	_ = clean

	// Ban one. SetNodeCheatStatus(banned) also sets is_active=0, which means
	// the banned node never makes it into GetAllActiveNodes() in the first
	// place — but the rule "exclude banned" is in BuildCycle for defence in
	// depth. Test the behaviour either way.
	s.SetNodeCheatStatus(banned.ID, types.StatusBanned, "test")

	cycle, err := reward.BuildCycle(s, "cycle-8")
	if err != nil {
		t.Fatalf("BuildCycle: %v", err)
	}

	if _, ok := cycle.PointsTotal[common.HexToAddress(bannedWallet)]; ok {
		t.Error("banned wallet leaked into the tree")
	}
	if _, ok := cycle.PointsTotal[common.HexToAddress(cleanWallet)]; !ok {
		t.Error("clean wallet missing from the tree")
	}
}

// TestBuildCycle_Persist round-trips through the store: build, persist,
// reload via GetLatestSnapshot + GetWalletProof, verify.
func TestBuildCycle_Persist(t *testing.T) {
	s := memory.NewMemory()
	defer s.Close()

	w := "0xabcdefabcdefabcdefabcdefabcdefabcdefabcd"
	n := s.RegisterNode(w, types.BscArchive, types.LocalProver, "", "")
	s.AwardUptimePoints(n.ID, 60)

	cycle, err := reward.BuildCycle(s, "cycle-42")
	if err != nil {
		t.Fatalf("BuildCycle: %v", err)
	}
	if err := cycle.Persist(s, 1234567890, ""); err != nil {
		t.Fatalf("Persist: %v", err)
	}

	latest, err := s.GetLatestSnapshot()
	if err != nil {
		t.Fatalf("GetLatestSnapshot: %v", err)
	}
	if latest == nil {
		t.Fatal("GetLatestSnapshot returned nil after Persist")
	}
	if latest.CycleID != "cycle-42" {
		t.Errorf("CycleID mismatch: got %q", latest.CycleID)
	}
	if !strings.HasPrefix(latest.Root, "0x") || len(latest.Root) != 66 {
		t.Errorf("root should be 0x-prefixed 32-byte hex; got %q", latest.Root)
	}
	if latest.NodeCount != 1 {
		t.Errorf("NodeCount mismatch: got %d", latest.NodeCount)
	}

	snap, proof, amount, err := s.GetWalletProof("cycle-42", w)
	if err != nil {
		t.Fatalf("GetWalletProof: %v", err)
	}
	if snap == nil {
		t.Fatal("GetWalletProof returned no snapshot")
	}
	if amount == nil {
		t.Fatal("GetWalletProof returned no amount")
	}

	rootBytes, err := reward.DecodeHexProof([]string{snap.Root})
	if err != nil {
		t.Fatalf("decode root: %v", err)
	}
	if !reward.VerifyProof(reward.Leaf{Wallet: common.HexToAddress(w), Amount: amount}, proof, rootBytes[0]) {
		t.Error("reloaded proof failed verification")
	}
}

// TestBuildCycle_PersistIdempotent confirms re-publishing the same cycle
// replaces the prior row.
func TestBuildCycle_PersistIdempotent(t *testing.T) {
	s := memory.NewMemory()
	defer s.Close()

	w := "0xdeadbeefdeadbeefdeadbeefdeadbeefdeadbeef"
	n := s.RegisterNode(w, types.BscArchive, types.LocalProver, "", "")
	s.AwardUptimePoints(n.ID, 60)

	c1, err := reward.BuildCycle(s, "cycle-x")
	if err != nil {
		t.Fatalf("BuildCycle: %v", err)
	}
	if err := c1.Persist(s, 1000, ""); err != nil {
		t.Fatalf("first persist: %v", err)
	}

	// Award more points and rebuild — same CycleID, different root.
	s.AwardUptimePoints(n.ID, 600)

	c2, err := reward.BuildCycle(s, "cycle-x")
	if err != nil {
		t.Fatalf("rebuild: %v", err)
	}
	if err := c2.Persist(s, 2000, ""); err != nil {
		t.Fatalf("second persist: %v", err)
	}

	latest, err := s.GetLatestSnapshot()
	if err != nil {
		t.Fatalf("GetLatestSnapshot: %v", err)
	}
	if latest.PublishedAt != 2000 {
		t.Errorf("expected republish timestamp 2000, got %d", latest.PublishedAt)
	}
}
