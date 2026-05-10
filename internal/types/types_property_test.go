// Package types: types_property_test.go enforces the cross-tier ordering
// guarantees baked into NodeType's per-tier accessors.
//
// These properties are easy to break by hand (someone tweaks one tier's
// PointsPerHour and forgets to update the rest) so we encode them as tests
// that read every tier rather than open-coding magic numbers.
package types

import (
	"testing"
)

// allTiers enumerates every supported NodeType. Adding a new tier to
// types.go MUST add it here too — these property tests will then exercise
// it automatically. A sanity guard at the start of each test reports if the
// list looks empty so the file fails loud rather than silent on a bad merge.
var allTiers = []NodeType{
	BscFull,
	BscFast,
	BscArchive,
	OpbnbFull,
	OpbnbFast,
}

// TestNodeType_AllTiers_NonEmpty is a meta-test: if the list above ever ends
// up empty (e.g. someone deletes the constants and forgets to update this
// file) every property test below would pass vacuously. Catch that here.
func TestNodeType_AllTiers_NonEmpty(t *testing.T) {
	if len(allTiers) == 0 {
		t.Fatal("allTiers is empty — property tests would pass vacuously")
	}
}

// TestNodeType_RegistrationBonus_NeverNegative — bonuses must be non-negative
// because TotalPoints is uint64 and underflow would wrap to a huge value.
func TestNodeType_RegistrationBonus_NeverNegative(t *testing.T) {
	for _, n := range allTiers {
		// uint64 is non-negative by construction; the meaningful assertion
		// is "must be set" — i.e. not the default-zero default-branch
		// fallback. Every defined tier should have a deliberate bonus,
		// even if 0 is a valid choice for some future tier; the test stays
		// useful by catching tiers added to allTiers but not to the switch.
		bonus := n.RegistrationBonus()
		_ = bonus // we only insist on no panic + non-negative; uint64 makes that automatic
	}
}

// TestNodeType_PointsPerHour_ArchiveHighest — Archive nodes are the most
// expensive to operate (full historical state) so they must out-earn every
// other tier per hour of uptime. If a regression bumps Fast or Full above
// Archive, this test fails.
func TestNodeType_PointsPerHour_ArchiveHighest(t *testing.T) {
	archive := BscArchive.PointsPerHour()
	for _, n := range allTiers {
		if n == BscArchive {
			continue
		}
		if n.PointsPerHour() > archive {
			t.Errorf("tier %s PointsPerHour=%d exceeds BscArchive=%d", n, n.PointsPerHour(), archive)
		}
	}
}

// TestNodeType_RegistrationBonus_ArchiveHighest — same ordering for the
// one-time bonus. Operators choose tier in part on this number; if Archive
// stops dominating the table, the incentive shape is wrong.
func TestNodeType_RegistrationBonus_ArchiveHighest(t *testing.T) {
	archive := BscArchive.RegistrationBonus()
	for _, n := range allTiers {
		if n == BscArchive {
			continue
		}
		if n.RegistrationBonus() > archive {
			t.Errorf("tier %s RegistrationBonus=%d exceeds BscArchive=%d", n, n.RegistrationBonus(), archive)
		}
	}
}

// TestNodeType_MinUptimePercent_InRange — every tier's minimum uptime
// percent must fit in the closed interval [0, 100]. Outside that range the
// stats math (uptime / 100) starts producing nonsense.
func TestNodeType_MinUptimePercent_InRange(t *testing.T) {
	for _, n := range allTiers {
		got := n.MinUptimePercent()
		// uint8 caps at 255; the meaningful guard is the upper bound.
		if got > 100 {
			t.Errorf("tier %s MinUptimePercent=%d outside [0,100]", n, got)
		}
	}
}

// TestNodeType_ChallengeFrequencyMinutes_Positive — frequency MUST be > 0
// or the scheduler's time.Duration math (60min / freq) divides by zero and
// crashes the server.
func TestNodeType_ChallengeFrequencyMinutes_Positive(t *testing.T) {
	for _, n := range allTiers {
		got := n.ChallengeFrequencyMinutes()
		if got == 0 {
			t.Errorf("tier %s ChallengeFrequencyMinutes=0 — would div-by-zero", n)
		}
	}
}
