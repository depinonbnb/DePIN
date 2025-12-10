import express from 'express';
import cors from 'cors';
import dotenv from 'dotenv';
import { createApiRoutes } from './api/routes';
import { createVerifier } from './verification/verifier';
import { createScheduler } from './api/scheduler';

dotenv.config();

const PORT = process.env.PORT || 3000;
const TRUSTED_RPC = process.env.TRUSTED_RPC || 'https://bsc-dataseed1.binance.org';

async function main(): Promise<void> {
  console.log('='.repeat(60));
  console.log('DePIN BNB Verification Server');
  console.log('='.repeat(60));

  // Create verifier with trusted RPC
  console.log(`Using trusted RPC: ${TRUSTED_RPC}`);
  const verifier = createVerifier(TRUSTED_RPC);

  // Create Express app
  const app = express();

  app.use(cors());
  app.use(express.json());

  // Health check
  app.get('/health', (req, res) => {
    res.json({ status: 'ok', timestamp: Date.now() });
  });

  // API routes
  app.use('/api', createApiRoutes(verifier));

  // Start server
  app.listen(PORT, () => {
    console.log(`Server running on port ${PORT}`);
    console.log(`API available at http://localhost:${PORT}/api`);
  });

  // Start scheduler for automated verification
  const scheduler = createScheduler(verifier);
  scheduler.start();

  // Graceful shutdown
  process.on('SIGINT', () => {
    console.log('\nShutting down...');
    scheduler.stop();
    process.exit(0);
  });

  process.on('SIGTERM', () => {
    console.log('\nShutting down...');
    scheduler.stop();
    process.exit(0);
  });

  console.log('='.repeat(60));
  console.log('Server ready!');
  console.log('');
  console.log('Endpoints:');
  console.log('  POST /api/nodes/register     - Register a new node');
  console.log('  GET  /api/nodes/:id          - Get node details');
  console.log('  GET  /api/nodes/:id/stats    - Get node statistics');
  console.log('  GET  /api/challenges/request - Request a challenge (local-prover)');
  console.log('  POST /api/challenges/submit  - Submit challenge response');
  console.log('  POST /api/verify/:id         - Verify exposed-rpc node');
  console.log('  GET  /api/leaderboard        - Get top nodes');
  console.log('  GET  /api/stats              - Get network stats');
  console.log('='.repeat(60));
}

main().catch((error) => {
  console.error('Fatal error:', error);
  process.exit(1);
});
