import { ethers } from 'ethers';
import { BlockData, SyncStatus, Challenge, ChallengeType } from '../types';

export interface RpcClientConfig {
  endpoint: string;
  authToken?: string;
  timeout?: number;
}

export interface RpcResponse<T> {
  success: boolean;
  data?: T;
  error?: string;
  latencyMs: number;
}

export class RpcClient {
  private endpoint: string;
  private authToken?: string;
  private timeout: number;

  constructor(config: RpcClientConfig) {
    this.endpoint = config.endpoint;
    this.authToken = config.authToken;
    this.timeout = config.timeout || 5000;
  }

  private async makeRpcCall<T>(method: string, params: unknown[] = []): Promise<RpcResponse<T>> {
    const startTime = Date.now();

    try {
      const headers: Record<string, string> = {
        'Content-Type': 'application/json',
      };

      if (this.authToken) {
        headers['Authorization'] = `Bearer ${this.authToken}`;
      }

      const controller = new AbortController();
      const timeoutId = setTimeout(() => controller.abort(), this.timeout);

      const response = await fetch(this.endpoint, {
        method: 'POST',
        headers,
        body: JSON.stringify({
          jsonrpc: '2.0',
          id: Date.now(),
          method,
          params,
        }),
        signal: controller.signal,
      });

      clearTimeout(timeoutId);

      const latencyMs = Date.now() - startTime;

      if (!response.ok) {
        return {
          success: false,
          error: `HTTP ${response.status}: ${response.statusText}`,
          latencyMs,
        };
      }

      const json = await response.json();

      if (json.error) {
        return {
          success: false,
          error: json.error.message || 'RPC error',
          latencyMs,
        };
      }

      return {
        success: true,
        data: json.result as T,
        latencyMs,
      };
    } catch (error) {
      const latencyMs = Date.now() - startTime;

      if (error instanceof Error) {
        if (error.name === 'AbortError') {
          return {
            success: false,
            error: 'Request timeout',
            latencyMs,
          };
        }
        return {
          success: false,
          error: error.message,
          latencyMs,
        };
      }

      return {
        success: false,
        error: 'Unknown error',
        latencyMs,
      };
    }
  }

  async getBlockNumber(): Promise<RpcResponse<number>> {
    const response = await this.makeRpcCall<string>('eth_blockNumber');

    if (response.success && response.data) {
      return {
        ...response,
        data: parseInt(response.data, 16),
      };
    }

    return response as RpcResponse<number>;
  }

  async getSyncStatus(): Promise<RpcResponse<SyncStatus>> {
    const response = await this.makeRpcCall<boolean | { currentBlock: string; highestBlock: string }>(
      'eth_syncing'
    );

    if (response.success) {
      if (response.data === false) {
        return {
          ...response,
          data: { syncing: false },
        };
      }

      if (typeof response.data === 'object' && response.data !== null) {
        return {
          ...response,
          data: {
            syncing: true,
            currentBlock: parseInt(response.data.currentBlock, 16),
            highestBlock: parseInt(response.data.highestBlock, 16),
          },
        };
      }
    }

    return response as RpcResponse<SyncStatus>;
  }

  async getBlockByNumber(blockNumber: number): Promise<RpcResponse<BlockData>> {
    const blockHex = '0x' + blockNumber.toString(16);
    const response = await this.makeRpcCall<BlockData>('eth_getBlockByNumber', [blockHex, false]);
    return response;
  }

  async getBlockHash(blockNumber: number): Promise<RpcResponse<string>> {
    const blockResponse = await this.getBlockByNumber(blockNumber);

    if (blockResponse.success && blockResponse.data) {
      return {
        success: true,
        data: blockResponse.data.hash,
        latencyMs: blockResponse.latencyMs,
      };
    }

    return {
      success: false,
      error: blockResponse.error || 'Failed to get block',
      latencyMs: blockResponse.latencyMs,
    };
  }

  async getBalance(address: string, blockNumber?: number): Promise<RpcResponse<string>> {
    const blockTag = blockNumber ? '0x' + blockNumber.toString(16) : 'latest';
    const response = await this.makeRpcCall<string>('eth_getBalance', [address, blockTag]);

    if (response.success && response.data) {
      return {
        ...response,
        data: response.data, // Return raw hex balance
      };
    }

    return response;
  }

  async getPeerCount(): Promise<RpcResponse<number>> {
    const response = await this.makeRpcCall<string>('net_peerCount');

    if (response.success && response.data) {
      return {
        ...response,
        data: parseInt(response.data, 16),
      };
    }

    return response as RpcResponse<number>;
  }

  async getNodeInfo(): Promise<RpcResponse<{ name: string; enode: string }>> {
    // Try admin_nodeInfo first (requires admin API)
    const response = await this.makeRpcCall<{ name: string; enode: string }>('admin_nodeInfo');
    return response;
  }

  async executeChallenge(challenge: Challenge): Promise<RpcResponse<string>> {
    switch (challenge.type) {
      case 'block-hash':
        if (challenge.params.blockNumber === undefined) {
          return { success: false, error: 'Missing block number', latencyMs: 0 };
        }
        return this.getBlockHash(challenge.params.blockNumber);

      case 'block-data':
        if (challenge.params.blockNumber === undefined) {
          return { success: false, error: 'Missing block number', latencyMs: 0 };
        }
        const blockResponse = await this.getBlockByNumber(challenge.params.blockNumber);
        if (blockResponse.success && blockResponse.data) {
          // Return stringified block data as answer
          return {
            success: true,
            data: JSON.stringify({
              hash: blockResponse.data.hash,
              parentHash: blockResponse.data.parentHash,
              stateRoot: blockResponse.data.stateRoot,
            }),
            latencyMs: blockResponse.latencyMs,
          };
        }
        return { success: false, error: blockResponse.error, latencyMs: blockResponse.latencyMs };

      case 'state-balance':
        if (!challenge.params.address || challenge.params.blockNumber === undefined) {
          return { success: false, error: 'Missing address or block number', latencyMs: 0 };
        }
        return this.getBalance(challenge.params.address, challenge.params.blockNumber);

      case 'sync-status':
        const syncResponse = await this.getSyncStatus();
        if (syncResponse.success && syncResponse.data) {
          return {
            success: true,
            data: JSON.stringify(syncResponse.data),
            latencyMs: syncResponse.latencyMs,
          };
        }
        return { success: false, error: syncResponse.error, latencyMs: syncResponse.latencyMs };

      default:
        return { success: false, error: `Unknown challenge type: ${challenge.type}`, latencyMs: 0 };
    }
  }
}

// Factory function for creating RPC clients
export function createRpcClient(endpoint: string, authToken?: string): RpcClient {
  return new RpcClient({ endpoint, authToken });
}
