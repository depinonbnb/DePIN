import { Router, Request, Response } from 'express';
import { ethers } from 'ethers';
import { nodeStore } from '../store/node-store';
import { createVerifier, NodeVerifier } from '../verification/verifier';
import { NodeType, VerificationMethod, ChallengeResponse, TIER_CONFIGS } from '../types';

export function createApiRoutes(verifier: NodeVerifier): Router {
  const router = Router();

  // ==================
  // NODE REGISTRATION
  // ==================

  // Users hit this endpoint to register their node with us
  router.post('/nodes/register', async (req: Request, res: Response) => {
    try {
      const { walletAddress, nodeType, verificationMethod, rpcEndpoint, authToken, signature, timestamp } = req.body;

      // Make sure they sent us everything we need
      if (!walletAddress || !nodeType || !verificationMethod || !signature || !timestamp) {
        return res.status(400).json({ error: 'Missing required fields' });
      }

      // Check if this is a node type we actually support
      if (!TIER_CONFIGS[nodeType as NodeType]) {
        return res.status(400).json({ error: 'Invalid node type' });
      }

      // Only two ways to verify: expose your RPC or run our prover script
      if (!['exposed-rpc', 'local-prover'].includes(verificationMethod)) {
        return res.status(400).json({ error: 'Invalid verification method' });
      }

      // If they want us to query their node directly, we need to know where it is
      if (verificationMethod === 'exposed-rpc' && !rpcEndpoint) {
        return res.status(400).json({ error: 'RPC endpoint required for exposed-rpc method' });
      }

      // They need to sign a message to prove they own this wallet
      const message = `Register node\nWallet: ${walletAddress}\nType: ${nodeType}\nTimestamp: ${timestamp}`;

      try {
        const recoveredAddress = ethers.verifyMessage(message, signature);
        if (recoveredAddress.toLowerCase() !== walletAddress.toLowerCase()) {
          return res.status(401).json({ error: 'Invalid signature' });
        }
      } catch {
        return res.status(401).json({ error: 'Invalid signature format' });
      }

      // Don't accept old requests - prevents replay attacks
      const now = Date.now();
      if (Math.abs(now - timestamp) > 5 * 60 * 1000) {
        return res.status(400).json({ error: 'Timestamp too old' });
      }

      // All good, save their node
      const node = nodeStore.registerNode(
        walletAddress,
        nodeType as NodeType,
        verificationMethod as VerificationMethod,
        rpcEndpoint,
        authToken
      );

      res.json({
        success: true,
        nodeId: node.id,
        message: 'Node registered successfully',
      });
    } catch (error) {
      console.error('Registration error:', error);
      res.status(500).json({ error: 'Internal server error' });
    }
  });

  // Get info about a specific node
  router.get('/nodes/:nodeId', (req: Request, res: Response) => {
    const node = nodeStore.getNode(req.params.nodeId);

    if (!node) {
      return res.status(404).json({ error: 'Node not found' });
    }

    // Never send the auth token back - that would be a security risk
    const { authToken, ...safeNode } = node;
    res.json(safeNode);
  });

  // Get all nodes belonging to a wallet
  router.get('/nodes/wallet/:walletAddress', (req: Request, res: Response) => {
    const nodes = nodeStore.getNodesByWallet(req.params.walletAddress);

    const safeNodes = nodes.map(({ authToken, ...node }) => node);
    res.json(safeNodes);
  });

  // Get stats for a node (uptime, pass rate, etc)
  router.get('/nodes/:nodeId/stats', (req: Request, res: Response) => {
    const stats = nodeStore.getNodeStats(req.params.nodeId);

    if (!stats) {
      return res.status(404).json({ error: 'Node not found' });
    }

    res.json(stats);
  });

  // ==================
  // CHALLENGES
  // ==================
  // These endpoints are for the local-prover method where users
  // run a script that fetches challenges and submits answers

  // User's prover script calls this to get a challenge to solve
  router.get('/challenges/request', async (req: Request, res: Response) => {
    try {
      const { nodeId } = req.query;

      if (!nodeId || typeof nodeId !== 'string') {
        return res.status(400).json({ error: 'nodeId required' });
      }

      const node = nodeStore.getNode(nodeId);
      if (!node) {
        return res.status(404).json({ error: 'Node not found' });
      }

      if (!node.isActive) {
        return res.status(400).json({ error: 'Node is not active' });
      }

      // Generate a random challenge - we already know the correct answer
      const challenge = await verifier.createChallenge(node);

      res.json({
        challenge: {
          id: challenge.id,
          type: challenge.type,
          params: challenge.params,
          expiresAt: challenge.expiresAt,
        },
        serverTime: Date.now(),
      });
    } catch (error) {
      console.error('Challenge request error:', error);
      res.status(500).json({ error: 'Failed to create challenge' });
    }
  });

  // User's prover script submits their answer here
  router.post('/challenges/submit', async (req: Request, res: Response) => {
    try {
      const response: ChallengeResponse = req.body;

      if (!response.challengeId || !response.nodeId || !response.answer || !response.signature) {
        return res.status(400).json({ error: 'Missing required fields' });
      }

      const node = nodeStore.getNode(response.nodeId);
      if (!node) {
        return res.status(404).json({ error: 'Node not found' });
      }

      // Make sure the response is actually signed by the node owner
      const message = `Challenge Response\nID: ${response.challengeId}\nAnswer: ${response.answer}\nTimestamp: ${response.timestamp}`;

      try {
        const recoveredAddress = ethers.verifyMessage(message, response.signature);
        if (recoveredAddress.toLowerCase() !== node.walletAddress.toLowerCase()) {
          return res.status(401).json({ error: 'Invalid signature' });
        }
      } catch {
        return res.status(401).json({ error: 'Invalid signature format' });
      }

      // Check if their answer is correct
      const result = await verifier.verifyResponse(response);

      // Save the result either way
      nodeStore.recordVerificationResult(result);

      res.json({
        passed: result.passed,
        failureReason: result.failureReason,
        responseTimeMs: result.responseTimeMs,
      });
    } catch (error) {
      console.error('Challenge submit error:', error);
      res.status(500).json({ error: 'Failed to verify challenge' });
    }
  });

  // ==================
  // DIRECT VERIFICATION
  // ==================
  // These endpoints are for exposed-rpc method where we query the user's node directly

  // Manually trigger a verification check on a node
  router.post('/verify/:nodeId', async (req: Request, res: Response) => {
    try {
      const node = nodeStore.getNode(req.params.nodeId);

      if (!node) {
        return res.status(404).json({ error: 'Node not found' });
      }

      if (node.verificationMethod !== 'exposed-rpc') {
        return res.status(400).json({ error: 'Node is not using exposed-rpc method' });
      }

      const result = await verifier.verifyExposedRpc(node);
      nodeStore.recordVerificationResult(result);

      res.json({
        passed: result.passed,
        failureReason: result.failureReason,
        responseTimeMs: result.responseTimeMs,
      });
    } catch (error) {
      console.error('Verification error:', error);
      res.status(500).json({ error: 'Verification failed' });
    }
  });

  // Quick check to see if a node is online and synced
  router.get('/verify/:nodeId/heartbeat', async (req: Request, res: Response) => {
    try {
      const node = nodeStore.getNode(req.params.nodeId);

      if (!node) {
        return res.status(404).json({ error: 'Node not found' });
      }

      if (node.verificationMethod !== 'exposed-rpc') {
        return res.status(400).json({ error: 'Node is not using exposed-rpc method' });
      }

      const heartbeat = await verifier.checkHeartbeat(node);

      if (!heartbeat) {
        return res.status(503).json({ error: 'Node unreachable' });
      }

      nodeStore.recordHeartbeat(heartbeat);

      res.json(heartbeat);
    } catch (error) {
      console.error('Heartbeat error:', error);
      res.status(500).json({ error: 'Heartbeat check failed' });
    }
  });

  // ==================
  // PUBLIC DATA
  // ==================

  // Top 100 nodes ranked by uptime
  router.get('/leaderboard', (req: Request, res: Response) => {
    const nodes = nodeStore.getAllActiveNodes();

    const leaderboard = nodes
      .map((node) => {
        const stats = nodeStore.getNodeStats(node.id);
        return {
          nodeId: node.id,
          walletAddress: node.walletAddress,
          nodeType: node.nodeType,
          uptimePercent: stats?.uptimePercent || 0,
          challengePassRate: stats?.challengePassRate || 0,
          currentStreak: stats?.currentStreak || 0,
          registeredAt: node.registeredAt,
        };
      })
      .sort((a, b) => b.uptimePercent - a.uptimePercent)
      .slice(0, 100);

    res.json(leaderboard);
  });

  // Overall network stats
  router.get('/stats', (req: Request, res: Response) => {
    const nodes = nodeStore.getAllActiveNodes();

    const byType: Record<string, number> = {};
    const byMethod: Record<string, number> = {};

    for (const node of nodes) {
      byType[node.nodeType] = (byType[node.nodeType] || 0) + 1;
      byMethod[node.verificationMethod] = (byMethod[node.verificationMethod] || 0) + 1;
    }

    res.json({
      totalNodes: nodes.length,
      byType,
      byMethod,
    });
  });

  return router;
}
