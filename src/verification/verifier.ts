import { ethers } from 'ethers';
import {
  Challenge,
  ChallengeResponse,
  VerificationResult,
  NodeRegistration,
  HeartbeatRecord,
  LATENCY_THRESHOLDS,
} from '../types';
import { RpcClient, createRpcClient } from './rpc-client';
import { challengeGenerator } from './challenges';

export interface VerificationConfig {
  trustedRpcEndpoint: string;
}

export class NodeVerifier {
  private trustedRpc: RpcClient;
  private pendingChallenges: Map<string, { challenge: Challenge; expectedAnswer: string }> = new Map();

  constructor(config: VerificationConfig) {
    // We use our own trusted node to get the "correct" answers
    this.trustedRpc = createRpcClient(config.trustedRpcEndpoint);
  }

  // Create a challenge for a node to solve
  // We query our trusted node first so we know what the right answer is
  async createChallenge(node: NodeRegistration): Promise<Challenge> {
    const challenge = challengeGenerator.generateChallenge(node.id, node.nodeType);

    // Get the answer from our trusted node
    const expectedResponse = await this.trustedRpc.executeChallenge(challenge);

    if (!expectedResponse.success || !expectedResponse.data) {
      throw new Error(`Failed to get expected answer: ${expectedResponse.error}`);
    }

    // Store the challenge with its answer - we'll check against this later
    this.pendingChallenges.set(challenge.id, {
      challenge,
      expectedAnswer: expectedResponse.data,
    });

    // Send back the challenge WITHOUT the answer obviously
    return challenge;
  }

  // Check if a submitted answer is correct
  async verifyResponse(response: ChallengeResponse): Promise<VerificationResult> {
    const pending = this.pendingChallenges.get(response.challengeId);

    // Either we never issued this challenge or it already expired
    if (!pending) {
      return {
        challengeId: response.challengeId,
        nodeId: response.nodeId,
        passed: false,
        responseTimeMs: response.responseTimeMs,
        failureReason: 'Challenge not found or expired',
        timestamp: Date.now(),
      };
    }

    const { challenge, expectedAnswer } = pending;

    // Too slow - challenges expire after 1 minute
    if (Date.now() > challenge.expiresAt) {
      this.pendingChallenges.delete(response.challengeId);
      return {
        challengeId: response.challengeId,
        nodeId: response.nodeId,
        passed: false,
        responseTimeMs: response.responseTimeMs,
        failureReason: 'Challenge expired',
        timestamp: Date.now(),
      };
    }

    // Does their answer match ours?
    const answerMatches = this.compareAnswers(response.answer, expectedAnswer, challenge.type);

    if (!answerMatches) {
      this.pendingChallenges.delete(response.challengeId);
      return {
        challengeId: response.challengeId,
        nodeId: response.nodeId,
        passed: false,
        responseTimeMs: response.responseTimeMs,
        failureReason: 'Incorrect answer',
        timestamp: Date.now(),
      };
    }

    // Check if response time looks suspicious (might be using a public RPC)
    const latencyCheck = this.checkLatency(response.responseTimeMs);
    if (!latencyCheck.valid) {
      this.pendingChallenges.delete(response.challengeId);
      return {
        challengeId: response.challengeId,
        nodeId: response.nodeId,
        passed: false,
        responseTimeMs: response.responseTimeMs,
        failureReason: latencyCheck.reason,
        timestamp: Date.now(),
      };
    }

    // They passed!
    this.pendingChallenges.delete(response.challengeId);

    return {
      challengeId: response.challengeId,
      nodeId: response.nodeId,
      passed: true,
      responseTimeMs: response.responseTimeMs,
      timestamp: Date.now(),
    };
  }

  // Compare answers - different challenge types need different comparison logic
  private compareAnswers(submitted: string, expected: string, challengeType: string): boolean {
    const normalizedSubmitted = submitted.toLowerCase().trim();
    const normalizedExpected = expected.toLowerCase().trim();

    // Block hashes should match exactly
    if (challengeType === 'block-hash') {
      return normalizedSubmitted === normalizedExpected;
    }

    // Balances can have different formatting (leading zeros etc) so compare as numbers
    if (challengeType === 'state-balance') {
      try {
        return BigInt(normalizedSubmitted) === BigInt(normalizedExpected);
      } catch {
        return normalizedSubmitted === normalizedExpected;
      }
    }

    // JSON responses need to be parsed and compared properly
    if (challengeType === 'block-data' || challengeType === 'sync-status') {
      try {
        const submittedObj = JSON.parse(submitted);
        const expectedObj = JSON.parse(expected);
        return JSON.stringify(submittedObj) === JSON.stringify(expectedObj);
      } catch {
        return normalizedSubmitted === normalizedExpected;
      }
    }

    return normalizedSubmitted === normalizedExpected;
  }

  // Check if response time is suspiciously slow
  // Local nodes respond in <100ms, public RPCs take 300ms+
  private checkLatency(responseTimeMs: number): { valid: boolean; reason?: string } {
    if (responseTimeMs > LATENCY_THRESHOLDS.maxAllowed) {
      return { valid: false, reason: 'Response too slow - timeout' };
    }

    // Flag slow responses but don't fail them - could be network issues
    if (responseTimeMs > LATENCY_THRESHOLDS.suspiciousMin) {
      console.warn(`Suspicious latency: ${responseTimeMs}ms - might be proxying to public RPC`);
    }

    return { valid: true };
  }

  // Helper to verify wallet signatures
  async verifySignature(
    message: string,
    signature: string,
    expectedAddress: string
  ): Promise<boolean> {
    try {
      const recoveredAddress = ethers.verifyMessage(message, signature);
      return recoveredAddress.toLowerCase() === expectedAddress.toLowerCase();
    } catch {
      return false;
    }
  }

  // For nodes that expose their RPC, we can query them directly
  // This is more trusted than the local prover method
  async verifyExposedRpc(node: NodeRegistration): Promise<VerificationResult> {
    if (!node.rpcEndpoint) {
      return {
        challengeId: 'direct-' + Date.now(),
        nodeId: node.id,
        passed: false,
        responseTimeMs: 0,
        failureReason: 'No RPC endpoint configured',
        timestamp: Date.now(),
      };
    }

    const nodeRpc = createRpcClient(node.rpcEndpoint, node.authToken);

    // Generate a challenge
    const challenge = challengeGenerator.generateChallenge(node.id, node.nodeType);

    // Get the right answer from our trusted node
    const expectedResponse = await this.trustedRpc.executeChallenge(challenge);
    if (!expectedResponse.success || !expectedResponse.data) {
      return {
        challengeId: challenge.id,
        nodeId: node.id,
        passed: false,
        responseTimeMs: 0,
        failureReason: `Trusted node error: ${expectedResponse.error}`,
        timestamp: Date.now(),
      };
    }

    // Now ask their node the same question
    const userResponse = await nodeRpc.executeChallenge(challenge);

    if (!userResponse.success || !userResponse.data) {
      return {
        challengeId: challenge.id,
        nodeId: node.id,
        passed: false,
        responseTimeMs: userResponse.latencyMs,
        failureReason: userResponse.error || 'Failed to get response from node',
        timestamp: Date.now(),
      };
    }

    // Do the answers match?
    const answerMatches = this.compareAnswers(
      userResponse.data,
      expectedResponse.data,
      challenge.type
    );

    if (!answerMatches) {
      return {
        challengeId: challenge.id,
        nodeId: node.id,
        passed: false,
        responseTimeMs: userResponse.latencyMs,
        failureReason: 'Incorrect answer',
        timestamp: Date.now(),
      };
    }

    return {
      challengeId: challenge.id,
      nodeId: node.id,
      passed: true,
      responseTimeMs: userResponse.latencyMs,
      timestamp: Date.now(),
    };
  }

  // Quick check to see if a node is online and synced
  async checkHeartbeat(node: NodeRegistration): Promise<HeartbeatRecord | null> {
    if (!node.rpcEndpoint) {
      return null;
    }

    const nodeRpc = createRpcClient(node.rpcEndpoint, node.authToken);

    // Ask for block number, sync status, and peer count all at once
    const [blockResponse, syncResponse, peerResponse] = await Promise.all([
      nodeRpc.getBlockNumber(),
      nodeRpc.getSyncStatus(),
      nodeRpc.getPeerCount(),
    ]);

    if (!blockResponse.success) {
      return null;
    }

    return {
      nodeId: node.id,
      timestamp: Date.now(),
      blockNumber: blockResponse.data || 0,
      isSynced: syncResponse.success ? !syncResponse.data?.syncing : false,
      latencyMs: blockResponse.latencyMs,
      peersCount: peerResponse.success ? peerResponse.data || 0 : 0,
    };
  }

  // Remove old challenges that nobody answered
  cleanupExpiredChallenges(): number {
    const now = Date.now();
    let cleaned = 0;

    for (const [id, { challenge }] of this.pendingChallenges.entries()) {
      if (now > challenge.expiresAt) {
        this.pendingChallenges.delete(id);
        cleaned++;
      }
    }

    return cleaned;
  }
}

export function createVerifier(trustedRpcEndpoint: string): NodeVerifier {
  return new NodeVerifier({ trustedRpcEndpoint });
}
