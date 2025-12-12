package verification

import (
	"testing"
	"time"

	"github.com/depinonbnb/depin/internal/types"
)

func TestNewVerifier(t *testing.T) {
	v := NewVerifier("https://bsc-dataseed1.binance.org")

	if v == nil {
		t.Fatal("NewVerifier returned nil")
	}

	if v.trustedRPC == nil {
		t.Error("trustedRPC should be initialized")
	}

	if v.pendingChallenges == nil {
		t.Error("pendingChallenges map not initialized")
	}
}

func TestVerifyResponseExpired(t *testing.T) {
	v := NewVerifier("https://bsc-dataseed1.binance.org")

	// Response for non-existent challenge
	response := &types.ChallengeResponse{
		ChallengeID:    "nonexistent",
		NodeID:         "test-node",
		Answer:         "answer",
		ResponseTimeMs: 50,
		Timestamp:      time.Now().UnixMilli(),
	}

	result := v.VerifyResponse(response)

	if result.Passed {
		t.Error("should fail for expired/non-existent challenge")
	}

	if result.FailureReason != "challenge not found or expired" {
		t.Errorf("unexpected failure reason: %s", result.FailureReason)
	}
}

func TestVerifyResponseTooSlow(t *testing.T) {
	v := NewVerifier("https://bsc-dataseed1.binance.org")

	// Manually create a pending challenge
	v.mu.Lock()
	v.pendingChallenges["test-challenge"] = &pendingChallenge{
		Challenge: &types.Challenge{
			ID:        "test-challenge",
			NodeID:    "test-node",
			ExpiresAt: time.Now().UnixMilli() + 60000,
		},
		ExpectedAnswer: "test-answer",
	}
	v.mu.Unlock()

	response := &types.ChallengeResponse{
		ChallengeID:    "test-challenge",
		NodeID:         "test-node",
		Answer:         "test-answer",
		ResponseTimeMs: 6000, // Over 5000ms limit
		Timestamp:      time.Now().UnixMilli(),
	}

	result := v.VerifyResponse(response)

	if result.Passed {
		t.Error("should fail for too slow response")
	}

	if result.FailureReason != "response too slow" {
		t.Errorf("unexpected failure reason: %s", result.FailureReason)
	}

	if !result.Suspicious {
		t.Error("slow response should be marked suspicious")
	}
}

func TestVerifyResponseWrongAnswer(t *testing.T) {
	v := NewVerifier("https://bsc-dataseed1.binance.org")

	// Manually create a pending challenge
	v.mu.Lock()
	v.pendingChallenges["test-challenge"] = &pendingChallenge{
		Challenge: &types.Challenge{
			ID:        "test-challenge",
			NodeID:    "test-node",
			ExpiresAt: time.Now().UnixMilli() + 60000,
		},
		ExpectedAnswer: "correct-answer",
	}
	v.mu.Unlock()

	response := &types.ChallengeResponse{
		ChallengeID:    "test-challenge",
		NodeID:         "test-node",
		Answer:         "wrong-answer",
		ResponseTimeMs: 50,
		Timestamp:      time.Now().UnixMilli(),
	}

	result := v.VerifyResponse(response)

	if result.Passed {
		t.Error("should fail for wrong answer")
	}

	if result.FailureReason != "incorrect answer" {
		t.Errorf("unexpected failure reason: %s", result.FailureReason)
	}
}

func TestVerifyResponseSuccess(t *testing.T) {
	v := NewVerifier("https://bsc-dataseed1.binance.org")

	// Manually create a pending challenge
	v.mu.Lock()
	v.pendingChallenges["test-challenge"] = &pendingChallenge{
		Challenge: &types.Challenge{
			ID:        "test-challenge",
			NodeID:    "test-node",
			ExpiresAt: time.Now().UnixMilli() + 60000,
		},
		ExpectedAnswer: "correct-answer",
	}
	v.mu.Unlock()

	response := &types.ChallengeResponse{
		ChallengeID:    "test-challenge",
		NodeID:         "test-node",
		Answer:         "correct-answer",
		ResponseTimeMs: 50,
		Timestamp:      time.Now().UnixMilli(),
	}

	result := v.VerifyResponse(response)

	if !result.Passed {
		t.Errorf("should pass with correct answer, got failure: %s", result.FailureReason)
	}

	if result.Suspicious {
		t.Error("fast response should not be suspicious")
	}
}

func TestVerifyResponseSuspiciousLatency(t *testing.T) {
	v := NewVerifier("https://bsc-dataseed1.binance.org")

	// Manually create a pending challenge
	v.mu.Lock()
	v.pendingChallenges["test-challenge"] = &pendingChallenge{
		Challenge: &types.Challenge{
			ID:        "test-challenge",
			NodeID:    "test-node",
			ExpiresAt: time.Now().UnixMilli() + 60000,
		},
		ExpectedAnswer: "correct-answer",
	}
	v.mu.Unlock()

	response := &types.ChallengeResponse{
		ChallengeID:    "test-challenge",
		NodeID:         "test-node",
		Answer:         "correct-answer",
		ResponseTimeMs: 200, // Over 150ms suspicious threshold but under 5000ms
		Timestamp:      time.Now().UnixMilli(),
	}

	result := v.VerifyResponse(response)

	if !result.Passed {
		t.Error("should still pass with suspicious latency")
	}

	if !result.Suspicious {
		t.Error("high latency response should be marked suspicious")
	}

	if result.SuspiciousNote == "" {
		t.Error("suspicious note should be set")
	}
}

func TestCleanupExpiredChallenges(t *testing.T) {
	v := NewVerifier("https://bsc-dataseed1.binance.org")

	// Create expired and valid challenges manually
	v.mu.Lock()
	v.pendingChallenges["expired-1"] = &pendingChallenge{
		Challenge: &types.Challenge{
			ID:        "expired-1",
			ExpiresAt: time.Now().UnixMilli() - 1000, // Expired
		},
	}
	v.pendingChallenges["valid-1"] = &pendingChallenge{
		Challenge: &types.Challenge{
			ID:        "valid-1",
			ExpiresAt: time.Now().UnixMilli() + 60000, // Still valid
		},
	}
	v.mu.Unlock()

	cleaned := v.CleanupExpiredChallenges()

	if cleaned != 1 {
		t.Errorf("expected 1 cleaned challenge, got %d", cleaned)
	}

	if len(v.pendingChallenges) != 1 {
		t.Errorf("expected 1 remaining challenge, got %d", len(v.pendingChallenges))
	}

	if _, exists := v.pendingChallenges["valid-1"]; !exists {
		t.Error("valid challenge should still exist")
	}
}

func TestCleanupMultipleExpired(t *testing.T) {
	v := NewVerifier("https://bsc-dataseed1.binance.org")

	// Create multiple expired challenges
	v.mu.Lock()
	for i := 0; i < 5; i++ {
		v.pendingChallenges[string(rune('a'+i))] = &pendingChallenge{
			Challenge: &types.Challenge{
				ID:        string(rune('a' + i)),
				ExpiresAt: time.Now().UnixMilli() - 1000,
			},
		}
	}
	v.mu.Unlock()

	cleaned := v.CleanupExpiredChallenges()

	if cleaned != 5 {
		t.Errorf("expected 5 cleaned challenges, got %d", cleaned)
	}

	if len(v.pendingChallenges) != 0 {
		t.Errorf("expected 0 remaining challenges, got %d", len(v.pendingChallenges))
	}
}

func TestLatencyThresholds(t *testing.T) {
	// Test that our latency thresholds make sense
	if types.LatencyLocalNode >= types.LatencySuspiciousMin {
		t.Error("local node latency should be below suspicious threshold")
	}

	if types.LatencySuspiciousMin >= types.LatencyMaxAllowed {
		t.Error("suspicious threshold should be below max allowed")
	}
}

func TestVerifyResponseChallengeDeleted(t *testing.T) {
	v := NewVerifier("https://bsc-dataseed1.binance.org")

	// Manually create a pending challenge
	v.mu.Lock()
	v.pendingChallenges["test-challenge"] = &pendingChallenge{
		Challenge: &types.Challenge{
			ID:        "test-challenge",
			NodeID:    "test-node",
			ExpiresAt: time.Now().UnixMilli() + 60000,
		},
		ExpectedAnswer: "correct-answer",
	}
	v.mu.Unlock()

	response := &types.ChallengeResponse{
		ChallengeID:    "test-challenge",
		NodeID:         "test-node",
		Answer:         "correct-answer",
		ResponseTimeMs: 50,
		Timestamp:      time.Now().UnixMilli(),
	}

	// First response should pass
	result := v.VerifyResponse(response)
	if !result.Passed {
		t.Error("first response should pass")
	}

	// Second response should fail (challenge deleted)
	result2 := v.VerifyResponse(response)
	if result2.Passed {
		t.Error("second response should fail - challenge should be deleted after use")
	}
}
