export type NodeType = 'bsc-full' | 'bsc-fast' | 'bsc-archive' | 'opbnb-full' | 'opbnb-fast';

export type VerificationMethod = 'exposed-rpc' | 'local-prover';

export type ChallengeType = 'block-hash' | 'block-data' | 'state-balance' | 'tx-receipt' | 'sync-status';

export interface NodeRegistration {
  id: string;
  walletAddress: string;
  nodeType: NodeType;
  verificationMethod: VerificationMethod;
  rpcEndpoint?: string; // For exposed-rpc method
  authToken?: string; // For exposed-rpc authentication
  registeredAt: number;
  lastVerifiedAt: number;
  totalUptimeSeconds: number;
  totalChallengesPassed: number;
  totalChallengesFailed: number;
  rewardTier: number;
  isActive: boolean;
}

export interface Challenge {
  id: string;
  nodeId: string;
  type: ChallengeType;
  createdAt: number;
  expiresAt: number;
  params: ChallengeParams;
  expectedAnswer?: string; // Server knows this, not sent to client
}

export interface ChallengeParams {
  blockNumber?: number;
  address?: string;
  txHash?: string;
}

export interface ChallengeResponse {
  challengeId: string;
  nodeId: string;
  answer: string;
  signature: string; // Wallet signature proving ownership
  responseTimeMs: number;
  timestamp: number;
}

export interface VerificationResult {
  challengeId: string;
  nodeId: string;
  passed: boolean;
  responseTimeMs: number;
  failureReason?: string;
  timestamp: number;
}

export interface HeartbeatRecord {
  nodeId: string;
  timestamp: number;
  blockNumber: number;
  isSynced: boolean;
  latencyMs: number;
  peersCount: number;
}

export interface NodeStats {
  nodeId: string;
  uptimePercent: number;
  challengePassRate: number;
  averageLatencyMs: number;
  totalRewardsEarned: string;
  currentStreak: number; // Consecutive successful verifications
}

export interface RewardCalculation {
  nodeId: string;
  periodStart: number;
  periodEnd: number;
  uptimePercent: number;
  challengePassRate: number;
  baseReward: string;
  tierMultiplier: number;
  uptimeBonus: number;
  finalReward: string;
}

// RPC response types
export interface BlockData {
  hash: string;
  number: string;
  timestamp: string;
  parentHash: string;
  stateRoot: string;
  transactionsRoot: string;
  receiptsRoot: string;
  miner: string;
  gasUsed: string;
  gasLimit: string;
}

export interface SyncStatus {
  syncing: boolean;
  currentBlock?: number;
  highestBlock?: number;
}

// Tier configuration
export interface TierConfig {
  nodeType: NodeType;
  baseRewardPerDay: string;
  minUptimePercent: number;
  challengeFrequencyMinutes: number;
}

export const TIER_CONFIGS: Record<NodeType, TierConfig> = {
  'bsc-archive': {
    nodeType: 'bsc-archive',
    baseRewardPerDay: '150',
    minUptimePercent: 95,
    challengeFrequencyMinutes: 30,
  },
  'bsc-full': {
    nodeType: 'bsc-full',
    baseRewardPerDay: '100',
    minUptimePercent: 95,
    challengeFrequencyMinutes: 30,
  },
  'bsc-fast': {
    nodeType: 'bsc-fast',
    baseRewardPerDay: '70',
    minUptimePercent: 90,
    challengeFrequencyMinutes: 60,
  },
  'opbnb-full': {
    nodeType: 'opbnb-full',
    baseRewardPerDay: '60',
    minUptimePercent: 90,
    challengeFrequencyMinutes: 60,
  },
  'opbnb-fast': {
    nodeType: 'opbnb-fast',
    baseRewardPerDay: '40',
    minUptimePercent: 85,
    challengeFrequencyMinutes: 60,
  },
};

// Latency thresholds for anti-cheat
export const LATENCY_THRESHOLDS = {
  localNode: 100, // Max ms for local node response
  suspiciousMin: 150, // Responses slower than this are suspicious
  publicRpcTypical: 300, // Typical public RPC latency
  maxAllowed: 5000, // Max response time before timeout
};

// Verification method reward multipliers
export const VERIFICATION_MULTIPLIERS: Record<VerificationMethod, number> = {
  'exposed-rpc': 1.0, // 100% rewards - most trusted
  'local-prover': 0.85, // 85% rewards - some trust required
};
