package challenge

import (
	"math/rand"
	"time"

	"github.com/depinonbnb/depin/internal/types"
	"github.com/google/uuid"
)

// Popular token contracts on BSC for balance queries
var knownAddresses = []string{
	"0xbb4CdB9CBd36B01bD1cBaEBF2De08d9173bc095c", // WBNB
	"0x55d398326f99059fF775485246999027B3197955", // USDT
	"0x8AC76a51cc950d9822D68b83fE1Ad97B32Cd580d", // USDC
	"0xe9e7CEA3DedcA5984780Bafc599bD69ADd087D56", // BUSD
	"0x2170Ed0880ac9A755fd29B2688956BD959F933F8", // ETH
	"0x0E09FaBB73Bd3Ade0a17ECC321fD13a19e81cE82", // CAKE
	"0x7130d2A12B9BCbFAe4f2634d864A1Ee1Ce3Ead9c", // BTCB
}

// Block ranges we can safely query
type blockRange struct {
	min          uint64
	safeMax      uint64
	recentWindow uint64
}

var bscBlockRanges = blockRange{
	min:          1000000,
	safeMax:      45000000,
	recentWindow: 100,
}

var opbnbBlockRanges = blockRange{
	min:          1000,
	safeMax:      30000000,
	recentWindow: 100,
}

type Generator struct {
	rng *rand.Rand
}

func NewGenerator() *Generator {
	return &Generator{
		rng: rand.New(rand.NewSource(time.Now().UnixNano())),
	}
}

func (g *Generator) getBlockRanges(nodeType types.NodeType) blockRange {
	switch nodeType {
	case types.OpbnbFull, types.OpbnbFast:
		return opbnbBlockRanges
	default:
		return bscBlockRanges
	}
}

// Different node types can handle different challenges
func (g *Generator) getAvailableChallengeTypes(nodeType types.NodeType) []types.ChallengeType {
	switch nodeType {
	case types.BscArchive:
		// Archive nodes keep all historical state
		return []types.ChallengeType{
			types.BlockHash,
			types.BlockData,
			types.StateBalance,
			types.SyncStatus,
		}
	case types.BscFull, types.OpbnbFull:
		// Full nodes have block data but limited historical state
		return []types.ChallengeType{
			types.BlockHash,
			types.BlockData,
			types.SyncStatus,
		}
	default:
		// Fast nodes only keep recent stuff
		return []types.ChallengeType{
			types.BlockHash,
			types.SyncStatus,
		}
	}
}

func (g *Generator) randomBlockNumber(min, max uint64) uint64 {
	return min + uint64(g.rng.Int63n(int64(max-min+1)))
}

// Generate a random challenge for a node
func (g *Generator) GenerateChallenge(nodeID string, nodeType types.NodeType) *types.Challenge {
	challengeTypes := g.getAvailableChallengeTypes(nodeType)
	challengeType := challengeTypes[g.rng.Intn(len(challengeTypes))]

	now := time.Now().UnixMilli()
	expiresIn := int64(60000) // 1 minute to answer

	challenge := &types.Challenge{
		ID:            uuid.New().String(),
		NodeID:        nodeID,
		ChallengeType: challengeType,
		CreatedAt:     now,
		ExpiresAt:     now + expiresIn,
		Params:        g.generateParams(challengeType, nodeType),
	}

	return challenge
}

func (g *Generator) generateParams(challengeType types.ChallengeType, nodeType types.NodeType) types.ChallengeParams {
	ranges := g.getBlockRanges(nodeType)

	switch challengeType {
	case types.BlockHash, types.BlockData:
		blockNum := g.randomBlockNumber(ranges.min, ranges.safeMax)
		return types.ChallengeParams{
			BlockNumber: &blockNum,
		}

	case types.StateBalance:
		// Archive nodes can query old blocks, others need recent ones
		var minBlock uint64
		if nodeType == types.BscArchive {
			minBlock = ranges.min
		} else {
			minBlock = ranges.safeMax - 10000
		}
		blockNum := g.randomBlockNumber(minBlock, ranges.safeMax)
		address := knownAddresses[g.rng.Intn(len(knownAddresses))]
		return types.ChallengeParams{
			BlockNumber: &blockNum,
			Address:     address,
		}

	case types.SyncStatus:
		return types.ChallengeParams{}

	default:
		return types.ChallengeParams{}
	}
}

// Generate multiple challenges at once
func (g *Generator) GenerateBatch(nodeID string, nodeType types.NodeType, count int) []*types.Challenge {
	challenges := make([]*types.Challenge, count)
	for i := 0; i < count; i++ {
		challenges[i] = g.GenerateChallenge(nodeID, nodeType)
	}
	return challenges
}
