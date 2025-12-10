import {
  NodeRegistration,
  VerificationResult,
  HeartbeatRecord,
  NodeStats,
  NodeType,
  VerificationMethod,
} from '../types';
import { v4 as uuidv4 } from 'uuid';

/**
 * In-memory store for nodes and verification data.
 * In production, replace with a proper database (PostgreSQL, MongoDB, etc.)
 */

class NodeStore {
  private nodes: Map<string, NodeRegistration> = new Map();
  private nodesByWallet: Map<string, string[]> = new Map(); // wallet -> nodeIds
  private verificationHistory: Map<string, VerificationResult[]> = new Map(); // nodeId -> results
  private heartbeats: Map<string, HeartbeatRecord[]> = new Map(); // nodeId -> heartbeats

  // Register a new node
  registerNode(
    walletAddress: string,
    nodeType: NodeType,
    verificationMethod: VerificationMethod,
    rpcEndpoint?: string,
    authToken?: string
  ): NodeRegistration {
    const nodeId = uuidv4();
    const now = Date.now();

    const node: NodeRegistration = {
      id: nodeId,
      walletAddress: walletAddress.toLowerCase(),
      nodeType,
      verificationMethod,
      rpcEndpoint,
      authToken,
      registeredAt: now,
      lastVerifiedAt: 0,
      totalUptimeSeconds: 0,
      totalChallengesPassed: 0,
      totalChallengesFailed: 0,
      rewardTier: this.getRewardTier(nodeType),
      isActive: true,
    };

    this.nodes.set(nodeId, node);

    // Track by wallet
    const walletNodes = this.nodesByWallet.get(walletAddress.toLowerCase()) || [];
    walletNodes.push(nodeId);
    this.nodesByWallet.set(walletAddress.toLowerCase(), walletNodes);

    return node;
  }

  private getRewardTier(nodeType: NodeType): number {
    const tiers: Record<NodeType, number> = {
      'bsc-archive': 4,
      'bsc-full': 3,
      'bsc-fast': 2,
      'opbnb-full': 2,
      'opbnb-fast': 1,
    };
    return tiers[nodeType] || 1;
  }

  getNode(nodeId: string): NodeRegistration | undefined {
    return this.nodes.get(nodeId);
  }

  getNodesByWallet(walletAddress: string): NodeRegistration[] {
    const nodeIds = this.nodesByWallet.get(walletAddress.toLowerCase()) || [];
    return nodeIds
      .map((id) => this.nodes.get(id))
      .filter((n): n is NodeRegistration => n !== undefined);
  }

  getAllActiveNodes(): NodeRegistration[] {
    return Array.from(this.nodes.values()).filter((n) => n.isActive);
  }

  updateNode(nodeId: string, updates: Partial<NodeRegistration>): NodeRegistration | undefined {
    const node = this.nodes.get(nodeId);
    if (!node) return undefined;

    const updated = { ...node, ...updates };
    this.nodes.set(nodeId, updated);
    return updated;
  }

  deactivateNode(nodeId: string): boolean {
    const node = this.nodes.get(nodeId);
    if (!node) return false;

    node.isActive = false;
    this.nodes.set(nodeId, node);
    return true;
  }

  // Verification results
  recordVerificationResult(result: VerificationResult): void {
    const history = this.verificationHistory.get(result.nodeId) || [];
    history.push(result);

    // Keep last 1000 results per node
    if (history.length > 1000) {
      history.shift();
    }

    this.verificationHistory.set(result.nodeId, history);

    // Update node stats
    const node = this.nodes.get(result.nodeId);
    if (node) {
      if (result.passed) {
        node.totalChallengesPassed++;
      } else {
        node.totalChallengesFailed++;
      }
      node.lastVerifiedAt = result.timestamp;
      this.nodes.set(result.nodeId, node);
    }
  }

  getVerificationHistory(nodeId: string, limit: number = 100): VerificationResult[] {
    const history = this.verificationHistory.get(nodeId) || [];
    return history.slice(-limit);
  }

  // Heartbeats
  recordHeartbeat(heartbeat: HeartbeatRecord): void {
    const history = this.heartbeats.get(heartbeat.nodeId) || [];
    history.push(heartbeat);

    // Keep last 24 hours of heartbeats (assuming 5 min intervals = 288 records)
    if (history.length > 300) {
      history.shift();
    }

    this.heartbeats.set(heartbeat.nodeId, history);
  }

  getHeartbeats(nodeId: string, since?: number): HeartbeatRecord[] {
    const history = this.heartbeats.get(nodeId) || [];
    if (since) {
      return history.filter((h) => h.timestamp >= since);
    }
    return history;
  }

  // Statistics
  getNodeStats(nodeId: string): NodeStats | undefined {
    const node = this.nodes.get(nodeId);
    if (!node) return undefined;

    const verifications = this.verificationHistory.get(nodeId) || [];
    const heartbeats = this.heartbeats.get(nodeId) || [];

    // Calculate uptime from heartbeats (last 24 hours)
    const last24h = Date.now() - 24 * 60 * 60 * 1000;
    const recentHeartbeats = heartbeats.filter((h) => h.timestamp >= last24h);
    const expectedHeartbeats = (24 * 60) / 5; // Every 5 minutes
    const uptimePercent =
      recentHeartbeats.length > 0
        ? Math.min(100, (recentHeartbeats.length / expectedHeartbeats) * 100)
        : 0;

    // Challenge pass rate
    const recentVerifications = verifications.filter((v) => v.timestamp >= last24h);
    const passRate =
      recentVerifications.length > 0
        ? (recentVerifications.filter((v) => v.passed).length / recentVerifications.length) * 100
        : 0;

    // Average latency
    const avgLatency =
      recentVerifications.length > 0
        ? recentVerifications.reduce((sum, v) => sum + v.responseTimeMs, 0) /
          recentVerifications.length
        : 0;

    // Calculate streak
    let streak = 0;
    for (let i = verifications.length - 1; i >= 0; i--) {
      if (verifications[i].passed) {
        streak++;
      } else {
        break;
      }
    }

    return {
      nodeId,
      uptimePercent,
      challengePassRate: passRate,
      averageLatencyMs: avgLatency,
      totalRewardsEarned: '0', // TODO: Implement reward tracking
      currentStreak: streak,
    };
  }

  // Get nodes due for verification
  getNodesDueForVerification(intervalMs: number): NodeRegistration[] {
    const now = Date.now();
    return Array.from(this.nodes.values()).filter((node) => {
      if (!node.isActive) return false;
      return now - node.lastVerifiedAt >= intervalMs;
    });
  }

  // Export for backup/persistence
  exportData(): {
    nodes: NodeRegistration[];
    verifications: Record<string, VerificationResult[]>;
    heartbeats: Record<string, HeartbeatRecord[]>;
  } {
    return {
      nodes: Array.from(this.nodes.values()),
      verifications: Object.fromEntries(this.verificationHistory),
      heartbeats: Object.fromEntries(this.heartbeats),
    };
  }

  // Import from backup
  importData(data: {
    nodes: NodeRegistration[];
    verifications: Record<string, VerificationResult[]>;
    heartbeats: Record<string, HeartbeatRecord[]>;
  }): void {
    for (const node of data.nodes) {
      this.nodes.set(node.id, node);
      const walletNodes = this.nodesByWallet.get(node.walletAddress.toLowerCase()) || [];
      if (!walletNodes.includes(node.id)) {
        walletNodes.push(node.id);
        this.nodesByWallet.set(node.walletAddress.toLowerCase(), walletNodes);
      }
    }

    for (const [nodeId, results] of Object.entries(data.verifications)) {
      this.verificationHistory.set(nodeId, results);
    }

    for (const [nodeId, beats] of Object.entries(data.heartbeats)) {
      this.heartbeats.set(nodeId, beats);
    }
  }
}

// Singleton instance
export const nodeStore = new NodeStore();
