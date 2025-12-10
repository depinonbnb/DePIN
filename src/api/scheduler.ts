import { nodeStore } from '../store/node-store';
import { NodeVerifier } from '../verification/verifier';
import { TIER_CONFIGS } from '../types';

/**
 * Scheduler for automated verification tasks
 * - Periodically verifies exposed-rpc nodes
 * - Checks heartbeats
 * - Cleans up expired challenges
 */

export class VerificationScheduler {
  private verifier: NodeVerifier;
  private isRunning: boolean = false;
  private heartbeatInterval?: NodeJS.Timeout;
  private verificationInterval?: NodeJS.Timeout;
  private cleanupInterval?: NodeJS.Timeout;

  constructor(verifier: NodeVerifier) {
    this.verifier = verifier;
  }

  start(): void {
    if (this.isRunning) {
      console.log('Scheduler already running');
      return;
    }

    this.isRunning = true;
    console.log('Starting verification scheduler...');

    // Heartbeat checks every 5 minutes
    this.heartbeatInterval = setInterval(() => {
      this.runHeartbeatChecks();
    }, 5 * 60 * 1000);

    // Verification checks every 30 minutes
    this.verificationInterval = setInterval(() => {
      this.runVerificationChecks();
    }, 30 * 60 * 1000);

    // Cleanup expired challenges every minute
    this.cleanupInterval = setInterval(() => {
      const cleaned = this.verifier.cleanupExpiredChallenges();
      if (cleaned > 0) {
        console.log(`Cleaned up ${cleaned} expired challenges`);
      }
    }, 60 * 1000);

    // Run initial checks
    setTimeout(() => {
      this.runHeartbeatChecks();
    }, 10000);

    console.log('Scheduler started');
  }

  stop(): void {
    this.isRunning = false;

    if (this.heartbeatInterval) {
      clearInterval(this.heartbeatInterval);
    }
    if (this.verificationInterval) {
      clearInterval(this.verificationInterval);
    }
    if (this.cleanupInterval) {
      clearInterval(this.cleanupInterval);
    }

    console.log('Scheduler stopped');
  }

  private async runHeartbeatChecks(): Promise<void> {
    const nodes = nodeStore.getAllActiveNodes().filter(
      (n) => n.verificationMethod === 'exposed-rpc'
    );

    console.log(`Running heartbeat checks for ${nodes.length} exposed-rpc nodes...`);

    for (const node of nodes) {
      try {
        const heartbeat = await this.verifier.checkHeartbeat(node);

        if (heartbeat) {
          nodeStore.recordHeartbeat(heartbeat);
          console.log(`  [${node.id.slice(0, 8)}] Block #${heartbeat.blockNumber}, Synced: ${heartbeat.isSynced}`);
        } else {
          console.log(`  [${node.id.slice(0, 8)}] UNREACHABLE`);
        }
      } catch (error) {
        console.error(`  [${node.id.slice(0, 8)}] Error:`, error);
      }
    }
  }

  private async runVerificationChecks(): Promise<void> {
    const nodes = nodeStore.getAllActiveNodes().filter(
      (n) => n.verificationMethod === 'exposed-rpc'
    );

    console.log(`Running verification challenges for ${nodes.length} exposed-rpc nodes...`);

    for (const node of nodes) {
      try {
        // Check if node is due for verification based on its tier
        const config = TIER_CONFIGS[node.nodeType];
        const intervalMs = config.challengeFrequencyMinutes * 60 * 1000;
        const timeSinceLastVerification = Date.now() - node.lastVerifiedAt;

        if (timeSinceLastVerification < intervalMs) {
          continue; // Not due yet
        }

        const result = await this.verifier.verifyExposedRpc(node);
        nodeStore.recordVerificationResult(result);

        if (result.passed) {
          console.log(`  [${node.id.slice(0, 8)}] PASSED (${result.responseTimeMs}ms)`);
        } else {
          console.log(`  [${node.id.slice(0, 8)}] FAILED: ${result.failureReason}`);
        }
      } catch (error) {
        console.error(`  [${node.id.slice(0, 8)}] Error:`, error);
      }
    }
  }

  // Manual trigger for testing
  async triggerHeartbeatChecks(): Promise<void> {
    await this.runHeartbeatChecks();
  }

  async triggerVerificationChecks(): Promise<void> {
    await this.runVerificationChecks();
  }
}

export function createScheduler(verifier: NodeVerifier): VerificationScheduler {
  return new VerificationScheduler(verifier);
}
