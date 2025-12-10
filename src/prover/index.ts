#!/usr/bin/env node

// ===========================================
// DePIN BNB Local Prover
// ===========================================
// This is the open-source script that users run to prove they're running a node.
// You can read every line of this code - nothing hidden, nothing sketchy.
//
// How to run:
//   npx depin-bnb-prover --private-key YOUR_KEY
//
// Or directly:
//   ts-node src/prover/index.ts --private-key YOUR_KEY

import { ethers } from 'ethers';
import { RpcClient, createRpcClient } from '../verification/rpc-client';
import { Challenge, ChallengeResponse, NodeType } from '../types';

interface ProverConfig {
  walletPrivateKey: string;
  nodeRpcEndpoint: string;
  apiEndpoint: string;
  nodeType: NodeType;
  intervalMs: number;
}

interface ApiChallenge {
  challenge: Challenge;
  serverTime: number;
}

class LocalProver {
  private config: ProverConfig;
  private wallet: ethers.Wallet;
  private nodeRpc: RpcClient;
  private isRunning: boolean = false;
  private nodeId: string | null = null;

  constructor(config: ProverConfig) {
    this.config = config;
    this.wallet = new ethers.Wallet(config.walletPrivateKey);
    this.nodeRpc = createRpcClient(config.nodeRpcEndpoint);
  }

  async start(): Promise<void> {
    console.log('='.repeat(60));
    console.log('DePIN BNB Local Prover');
    console.log('='.repeat(60));
    console.log(`Wallet: ${this.wallet.address}`);
    console.log(`Node RPC: ${this.config.nodeRpcEndpoint}`);
    console.log(`API: ${this.config.apiEndpoint}`);
    console.log(`Node Type: ${this.config.nodeType}`);
    console.log('='.repeat(60));

    // First make sure we can actually talk to the local node
    const nodeCheck = await this.checkLocalNode();
    if (!nodeCheck.success) {
      console.error('ERROR: Cannot connect to local node');
      console.error(`Make sure your BNB node is running at ${this.config.nodeRpcEndpoint}`);
      process.exit(1);
    }

    console.log(`Local node connected - Block #${nodeCheck.blockNumber}`);
    console.log(`Synced: ${nodeCheck.synced ? 'YES' : 'NO'}`);

    // Don't run if node isn't synced - you won't pass challenges anyway
    if (!nodeCheck.synced) {
      console.error('ERROR: Node is not fully synced. Please wait for sync to complete.');
      process.exit(1);
    }

    // Register with the API
    await this.registerNode();

    // Start submitting proofs
    this.isRunning = true;
    await this.proofLoop();
  }

  // Check if we can reach the local node and if it's synced
  private async checkLocalNode(): Promise<{ success: boolean; blockNumber?: number; synced?: boolean }> {
    try {
      const [blockResponse, syncResponse] = await Promise.all([
        this.nodeRpc.getBlockNumber(),
        this.nodeRpc.getSyncStatus(),
      ]);

      if (!blockResponse.success) {
        return { success: false };
      }

      const synced = syncResponse.success && !syncResponse.data?.syncing;

      return {
        success: true,
        blockNumber: blockResponse.data,
        synced,
      };
    } catch (error) {
      return { success: false };
    }
  }

  // Tell the API about our node
  private async registerNode(): Promise<void> {
    console.log('Registering node with API...');

    const timestamp = Date.now();
    const message = `Register node\nWallet: ${this.wallet.address}\nType: ${this.config.nodeType}\nTimestamp: ${timestamp}`;
    const signature = await this.wallet.signMessage(message);

    try {
      const response = await fetch(`${this.config.apiEndpoint}/nodes/register`, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({
          walletAddress: this.wallet.address,
          nodeType: this.config.nodeType,
          verificationMethod: 'local-prover',
          signature,
          timestamp,
        }),
      });

      if (!response.ok) {
        const error = await response.text();
        throw new Error(`Registration failed: ${error}`);
      }

      const data = await response.json();
      this.nodeId = data.nodeId;
      console.log(`Registered successfully - Node ID: ${this.nodeId}`);
    } catch (error) {
      console.error('Registration error:', error);
      throw error;
    }
  }

  // Main loop - keep submitting proofs at regular intervals
  private async proofLoop(): Promise<void> {
    console.log('\nStarting proof loop...\n');

    while (this.isRunning) {
      try {
        await this.submitProof();
      } catch (error) {
        console.error('Proof submission error:', error);
      }

      // Wait before next proof
      await this.sleep(this.config.intervalMs);
    }
  }

  // The actual proof submission flow
  private async submitProof(): Promise<void> {
    const startTime = Date.now();

    // Step 1: Get a challenge from the server
    console.log(`[${new Date().toISOString()}] Requesting challenge...`);

    const challengeResponse = await fetch(
      `${this.config.apiEndpoint}/challenges/request?nodeId=${this.nodeId}`,
      { method: 'GET' }
    );

    if (!challengeResponse.ok) {
      throw new Error(`Failed to get challenge: ${await challengeResponse.text()}`);
    }

    const { challenge }: ApiChallenge = await challengeResponse.json();
    console.log(`  Challenge: ${challenge.type} (Block #${challenge.params.blockNumber || 'N/A'})`);

    // Step 2: Ask our local node for the answer
    const queryStart = Date.now();
    const nodeResponse = await this.nodeRpc.executeChallenge(challenge);
    const queryTime = Date.now() - queryStart;

    if (!nodeResponse.success || !nodeResponse.data) {
      console.error(`  FAILED: ${nodeResponse.error}`);
      return;
    }

    console.log(`  Query time: ${queryTime}ms`);

    // Step 3: Sign the response so the server knows it's really us
    const responseMessage = `Challenge Response\nID: ${challenge.id}\nAnswer: ${nodeResponse.data}\nTimestamp: ${Date.now()}`;
    const signature = await this.wallet.signMessage(responseMessage);

    // Step 4: Send the answer back
    const proof: ChallengeResponse = {
      challengeId: challenge.id,
      nodeId: this.nodeId!,
      answer: nodeResponse.data,
      signature,
      responseTimeMs: queryTime,
      timestamp: Date.now(),
    };

    const submitResponse = await fetch(`${this.config.apiEndpoint}/challenges/submit`, {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify(proof),
    });

    if (!submitResponse.ok) {
      const error = await submitResponse.text();
      console.error(`  Submit failed: ${error}`);
      return;
    }

    const result = await submitResponse.json();
    const totalTime = Date.now() - startTime;

    if (result.passed) {
      console.log(`  PASSED (Total: ${totalTime}ms)`);
    } else {
      console.log(`  FAILED: ${result.failureReason}`);
    }
  }

  stop(): void {
    console.log('\nStopping prover...');
    this.isRunning = false;
  }

  private sleep(ms: number): Promise<void> {
    return new Promise((resolve) => setTimeout(resolve, ms));
  }
}

// Handle command line args and start the prover
async function main(): Promise<void> {
  const args = process.argv.slice(2);

  const getArg = (name: string): string | undefined => {
    const index = args.indexOf(`--${name}`);
    if (index !== -1 && args[index + 1]) {
      return args[index + 1];
    }
    return undefined;
  };

  // Get config from args or environment variables
  const privateKey = getArg('private-key') || process.env.PROVER_PRIVATE_KEY;
  const nodeRpc = getArg('node-rpc') || process.env.NODE_RPC || 'http://localhost:8545';
  const apiEndpoint = getArg('api') || process.env.DEPIN_API || 'http://localhost:3000/api';
  const nodeType = (getArg('node-type') || process.env.NODE_TYPE || 'bsc-full') as NodeType;
  const intervalMs = parseInt(getArg('interval') || process.env.INTERVAL || '300000', 10);

  if (!privateKey) {
    console.error('ERROR: Private key required');
    console.error('');
    console.error('Usage: npx depin-bnb-prover --private-key YOUR_KEY [options]');
    console.error('');
    console.error('Options:');
    console.error('  --private-key   Your wallet private key (or set PROVER_PRIVATE_KEY env var)');
    console.error('  --node-rpc      Your node RPC endpoint (default: http://localhost:8545)');
    console.error('  --api           DePIN API endpoint (default: http://localhost:3000/api)');
    console.error('  --node-type     Node type: bsc-full, bsc-fast, opbnb-full, etc.');
    console.error('  --interval      How often to submit proofs in ms (default: 300000 = 5 min)');
    process.exit(1);
  }

  const prover = new LocalProver({
    walletPrivateKey: privateKey,
    nodeRpcEndpoint: nodeRpc,
    apiEndpoint: apiEndpoint,
    nodeType: nodeType,
    intervalMs: intervalMs,
  });

  // Clean shutdown on ctrl+c
  process.on('SIGINT', () => {
    prover.stop();
    process.exit(0);
  });

  process.on('SIGTERM', () => {
    prover.stop();
    process.exit(0);
  });

  await prover.start();
}

main().catch((error) => {
  console.error('Fatal error:', error);
  process.exit(1);
});

export { LocalProver, ProverConfig };
