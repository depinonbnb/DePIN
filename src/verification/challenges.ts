import { v4 as uuidv4 } from 'uuid';
import { Challenge, ChallengeType, NodeType, ChallengeParams } from '../types';

// Popular token contracts on BSC - we use these for balance queries
// because they always have activity and non-zero balances
const KNOWN_ADDRESSES = [
  '0xbb4CdB9CBd36B01bD1cBaEBF2De08d9173bc095c', // WBNB
  '0x55d398326f99059fF775485246999027B3197955', // USDT
  '0x8AC76a51cc950d9822D68b83fE1Ad97B32Cd580d', // USDC
  '0xe9e7CEA3DedcA5984780Bafc599bD69ADd087D56', // BUSD
  '0x2170Ed0880ac9A755fd29B2688956BD959F933F8', // ETH
  '0x0E09FaBB73Bd3Ade0a17ECC321fD13a19e81cE82', // CAKE
  '0x7130d2A12B9BCbFAe4f2634d864A1Ee1Ce3Ead9c', // BTCB
];

// Block ranges we can safely query - these are confirmed and won't change
const BSC_BLOCK_RANGES = {
  min: 1000000,
  safeMax: 45000000,
  recentWindow: 100,
};

const OPBNB_BLOCK_RANGES = {
  min: 1000,
  safeMax: 30000000,
  recentWindow: 100,
};

export class ChallengeGenerator {
  private getBlockRanges(nodeType: NodeType) {
    if (nodeType.startsWith('opbnb')) {
      return OPBNB_BLOCK_RANGES;
    }
    return BSC_BLOCK_RANGES;
  }

  // Generate a random challenge for a node
  generateChallenge(nodeId: string, nodeType: NodeType): Challenge {
    const challengeTypes = this.getAvailableChallengeTypes(nodeType);
    const type = challengeTypes[Math.floor(Math.random() * challengeTypes.length)];

    const now = Date.now();
    const expiresIn = 60000; // 1 minute to answer

    return {
      id: uuidv4(),
      nodeId,
      type,
      createdAt: now,
      expiresAt: now + expiresIn,
      params: this.generateParams(type, nodeType),
    };
  }

  // Different node types can handle different challenges
  // Archive nodes can do everything, fast nodes are more limited
  private getAvailableChallengeTypes(nodeType: NodeType): ChallengeType[] {
    if (nodeType === 'bsc-archive') {
      // Archive nodes keep all historical state so we can ask about any block
      return ['block-hash', 'block-data', 'state-balance', 'tx-receipt', 'sync-status'];
    }

    if (nodeType === 'bsc-full' || nodeType === 'opbnb-full') {
      // Full nodes have block data but limited historical state
      return ['block-hash', 'block-data', 'sync-status'];
    }

    // Fast nodes only keep recent stuff
    return ['block-hash', 'sync-status'];
  }

  // Generate random parameters for each challenge type
  private generateParams(type: ChallengeType, nodeType: NodeType): ChallengeParams {
    const ranges = this.getBlockRanges(nodeType);

    switch (type) {
      case 'block-hash':
      case 'block-data':
        // Pick any random block in the safe range
        return {
          blockNumber: this.randomBlockNumber(ranges.min, ranges.safeMax),
        };

      case 'state-balance':
        // Archive nodes can query old blocks, others need recent ones
        const isArchive = nodeType === 'bsc-archive';
        const minBlock = isArchive ? ranges.min : ranges.safeMax - 10000;
        return {
          blockNumber: this.randomBlockNumber(minBlock, ranges.safeMax),
          address: KNOWN_ADDRESSES[Math.floor(Math.random() * KNOWN_ADDRESSES.length)],
        };

      case 'sync-status':
        // Just checking if they're synced, no params needed
        return {};

      case 'tx-receipt':
        // Query a recent tx
        return {
          blockNumber: this.randomBlockNumber(ranges.safeMax - 1000, ranges.safeMax),
        };

      default:
        return {};
    }
  }

  private randomBlockNumber(min: number, max: number): number {
    return Math.floor(Math.random() * (max - min + 1)) + min;
  }

  // Generate multiple challenges at once if needed
  generateBatchChallenges(nodeId: string, nodeType: NodeType, count: number): Challenge[] {
    const challenges: Challenge[] = [];
    for (let i = 0; i < count; i++) {
      challenges.push(this.generateChallenge(nodeId, nodeType));
    }
    return challenges;
  }
}

export const challengeGenerator = new ChallengeGenerator();
