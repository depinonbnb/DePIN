package verification

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"math/big"
	"strings"
	"sync"
	"time"

	"github.com/depinonbnb/depin/internal/challenge"
	"github.com/depinonbnb/depin/internal/rpc"
	"github.com/depinonbnb/depin/internal/types"
)

type pendingChallenge struct {
	Challenge      *types.Challenge
	ExpectedAnswer string
}

type Verifier struct {
	// trustedRPC is the quorum client added in Phase 3 per ADR-0009. It still
	// satisfies the same per-call surface as the old single *rpc.Client, but
	// returns RpcResponse{Error: rpc.QuorumNoMajority} when no majority is
	// reached. Callers below treat that as ABORT (skip + re-queue), NOT as
	// cheating.
	trustedRPC        *rpc.QuorumClient
	generator         *challenge.Generator
	pendingChallenges map[string]*pendingChallenge
	mu                sync.RWMutex
}

// NewVerifier constructs a Verifier whose trusted source is the quorum of
// the given RPC endpoints. Pass a single-element slice for ADR-0005
// behaviour; the QuorumClient logs a deprecation warning in that case.
func NewVerifier(trustedRPCEndpoints []string) *Verifier {
	return &Verifier{
		trustedRPC:        rpc.NewQuorum(trustedRPCEndpoints),
		generator:         challenge.NewGenerator(),
		pendingChallenges: make(map[string]*pendingChallenge),
	}
}

// TrustedRPC exposes the underlying QuorumClient so /ready can reach it for
// HealthCheck. Returning the live client (not a copy) keeps the readiness
// probe honest — anything the verifier sees, /ready sees too.
func (v *Verifier) TrustedRPC() *rpc.QuorumClient {
	return v.trustedRPC
}

// Create a challenge for a node
// We query our trusted node first so we know the right answer.
// Quorum-RPC abort path: if the trusted quorum can't agree (response.Error
// == rpc.QuorumNoMajority) we surface it as a normal error so the caller
// can decide what to do — per ADR-0009 the caller (scheduler / handler)
// must NOT punish the node, just skip and re-queue.
func (v *Verifier) CreateChallenge(node *types.NodeRegistration) (*types.Challenge, error) {
	ch := v.generator.GenerateChallenge(node.ID, node.NodeType)

	// Get the answer from our trusted node
	response := v.trustedRPC.ExecuteChallenge(ch)
	if !response.Success {
		if response.Error == rpc.QuorumNoMajority {
			slog.Warn("verification: trusted RPC quorum disagreement; aborting challenge create",
				"node_id", node.ID,
				"challenge_type", string(ch.ChallengeType),
			)
			return nil, fmt.Errorf("trusted RPC quorum disagreement (treated as abort, not cheating)")
		}
		return nil, fmt.Errorf("failed to get expected answer: %s", response.Error)
	}

	// Store the challenge with its answer
	v.mu.Lock()
	v.pendingChallenges[ch.ID] = &pendingChallenge{
		Challenge:      ch,
		ExpectedAnswer: response.Data,
	}
	v.mu.Unlock()

	return ch, nil
}

// Check if a submitted answer is correct
func (v *Verifier) VerifyResponse(response *types.ChallengeResponse) *types.VerificationResult {
	v.mu.RLock()
	pending, exists := v.pendingChallenges[response.ChallengeID]
	v.mu.RUnlock()

	now := time.Now().UnixMilli()

	// Challenge not found or already used
	if !exists {
		return &types.VerificationResult{
			ChallengeID:    response.ChallengeID,
			NodeID:         response.NodeID,
			Passed:         false,
			ResponseTimeMs: response.ResponseTimeMs,
			FailureReason:  "challenge not found or expired",
			Timestamp:      now,
		}
	}

	// Too slow - challenges expire after 1 minute
	if now > pending.Challenge.ExpiresAt {
		v.deleteChallenge(response.ChallengeID)
		return &types.VerificationResult{
			ChallengeID:    response.ChallengeID,
			NodeID:         response.NodeID,
			Passed:         false,
			ResponseTimeMs: response.ResponseTimeMs,
			FailureReason:  "challenge expired",
			Timestamp:      now,
		}
	}

	// Does their answer match ours?
	if !v.compareAnswers(response.Answer, pending.ExpectedAnswer, pending.Challenge.ChallengeType) {
		v.deleteChallenge(response.ChallengeID)
		return &types.VerificationResult{
			ChallengeID:    response.ChallengeID,
			NodeID:         response.NodeID,
			Passed:         false,
			ResponseTimeMs: response.ResponseTimeMs,
			FailureReason:  "incorrect answer",
			Timestamp:      now,
		}
	}

	// Check if response time looks suspicious
	if response.ResponseTimeMs > types.LatencyMaxAllowed {
		v.deleteChallenge(response.ChallengeID)
		return &types.VerificationResult{
			ChallengeID:    response.ChallengeID,
			NodeID:         response.NodeID,
			Passed:         false,
			ResponseTimeMs: response.ResponseTimeMs,
			FailureReason:  "response too slow",
			Suspicious:     true,
			SuspiciousNote: "Response took too long - possible proxy or offline node",
			Timestamp:      now,
		}
	}

	// They passed!
	v.deleteChallenge(response.ChallengeID)

	// Flag slow responses but still pass them (suspicious but not failed)
	suspicious := response.ResponseTimeMs > types.LatencySuspiciousMin
	suspiciousNote := ""
	if suspicious {
		suspiciousNote = fmt.Sprintf("High latency %dms - might be proxying to public RPC", response.ResponseTimeMs)
		slog.Warn("suspicious latency",
			"node_id", response.NodeID,
			"response_time_ms", response.ResponseTimeMs,
		)
	}

	return &types.VerificationResult{
		ChallengeID:    response.ChallengeID,
		NodeID:         response.NodeID,
		Passed:         true,
		ResponseTimeMs: response.ResponseTimeMs,
		Suspicious:     suspicious,
		SuspiciousNote: suspiciousNote,
		Timestamp:      now,
	}
}

func (v *Verifier) deleteChallenge(id string) {
	v.mu.Lock()
	delete(v.pendingChallenges, id)
	v.mu.Unlock()
}

// Compare answers - different challenge types need different comparison
func (v *Verifier) compareAnswers(submitted, expected string, challengeType types.ChallengeType) bool {
	submitted = strings.ToLower(strings.TrimSpace(submitted))
	expected = strings.ToLower(strings.TrimSpace(expected))

	switch challengeType {
	case types.BlockHash:
		// Block hashes should match exactly
		return submitted == expected

	case types.StateBalance:
		// Balances can have different formatting so compare as numbers
		subBig, ok1 := new(big.Int).SetString(strings.TrimPrefix(submitted, "0x"), 16)
		expBig, ok2 := new(big.Int).SetString(strings.TrimPrefix(expected, "0x"), 16)
		if ok1 && ok2 {
			return subBig.Cmp(expBig) == 0
		}
		return submitted == expected

	case types.BlockData, types.SyncStatus:
		// JSON responses need to be parsed and compared
		var subObj, expObj map[string]interface{}
		if json.Unmarshal([]byte(submitted), &subObj) == nil &&
			json.Unmarshal([]byte(expected), &expObj) == nil {
			subJSON, _ := json.Marshal(subObj)
			expJSON, _ := json.Marshal(expObj)
			return string(subJSON) == string(expJSON)
		}
		return submitted == expected

	default:
		return submitted == expected
	}
}

// For nodes that expose their RPC, we query them directly
func (v *Verifier) VerifyExposedRPC(node *types.NodeRegistration) *types.VerificationResult {
	now := time.Now().UnixMilli()

	if node.RPCEndpoint == "" {
		return &types.VerificationResult{
			ChallengeID:   fmt.Sprintf("direct-%d", now),
			NodeID:        node.ID,
			Passed:        false,
			FailureReason: "no RPC endpoint configured",
			Timestamp:     now,
		}
	}

	nodeRPC := rpc.NewClient(node.RPCEndpoint, node.AuthToken)

	// Generate a challenge
	ch := v.generator.GenerateChallenge(node.ID, node.NodeType)

	// Get the right answer from our trusted node
	expectedResponse := v.trustedRPC.ExecuteChallenge(ch)
	if !expectedResponse.Success {
		// ADR-0009 abort path: a "no quorum" answer means the trusted side
		// disagreed with itself. We MUST NOT bump the operator's warning
		// count — Suspicious=false guarantees the in-memory + sqlite stores
		// won't escalate. Caller still records the result so it shows up in
		// history; downstream stats can ignore it.
		if expectedResponse.Error == rpc.QuorumNoMajority {
			slog.Warn("verification: trusted RPC quorum disagreement; aborting verify",
				"node_id", node.ID,
				"challenge_type", string(ch.ChallengeType),
			)
			return &types.VerificationResult{
				ChallengeID:   ch.ID,
				NodeID:        node.ID,
				Passed:        false,
				FailureReason: "trusted RPC quorum disagreement (treated as abort, not cheating)",
				Suspicious:    false,
				Timestamp:     now,
			}
		}
		return &types.VerificationResult{
			ChallengeID:   ch.ID,
			NodeID:        node.ID,
			Passed:        false,
			FailureReason: fmt.Sprintf("trusted node error: %s", expectedResponse.Error),
			Timestamp:     now,
		}
	}

	// Now ask their node the same question
	userResponse := nodeRPC.ExecuteChallenge(ch)
	if !userResponse.Success {
		return &types.VerificationResult{
			ChallengeID:    ch.ID,
			NodeID:         node.ID,
			Passed:         false,
			ResponseTimeMs: userResponse.LatencyMs,
			FailureReason:  userResponse.Error,
			Timestamp:      now,
		}
	}

	// Do the answers match?
	if !v.compareAnswers(userResponse.Data, expectedResponse.Data, ch.ChallengeType) {
		return &types.VerificationResult{
			ChallengeID:    ch.ID,
			NodeID:         node.ID,
			Passed:         false,
			ResponseTimeMs: userResponse.LatencyMs,
			FailureReason:  "incorrect answer",
			Timestamp:      now,
		}
	}

	return &types.VerificationResult{
		ChallengeID:    ch.ID,
		NodeID:         node.ID,
		Passed:         true,
		ResponseTimeMs: userResponse.LatencyMs,
		Timestamp:      now,
	}
}

// Quick check to see if a node is online and synced
func (v *Verifier) CheckHeartbeat(node *types.NodeRegistration) *types.HeartbeatRecord {
	if node.RPCEndpoint == "" {
		return nil
	}

	nodeRPC := rpc.NewClient(node.RPCEndpoint, node.AuthToken)

	blockNum, latency, err := nodeRPC.GetBlockNumber()
	if err != nil {
		return nil
	}

	synced, _, _ := nodeRPC.GetSyncStatus()
	peerCount, _, _ := nodeRPC.GetPeerCount()

	return &types.HeartbeatRecord{
		NodeID:      node.ID,
		Timestamp:   time.Now().UnixMilli(),
		BlockNumber: blockNum,
		IsSynced:    synced,
		LatencyMs:   latency,
		PeersCount:  peerCount,
	}
}

// Remove old challenges that nobody answered
func (v *Verifier) CleanupExpiredChallenges() int {
	now := time.Now().UnixMilli()
	cleaned := 0

	v.mu.Lock()
	defer v.mu.Unlock()

	for id, pending := range v.pendingChallenges {
		if now > pending.Challenge.ExpiresAt {
			delete(v.pendingChallenges, id)
			cleaned++
		}
	}

	return cleaned
}
