package rpc

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/depinonbnb/depin/internal/types"
)

type Client struct {
	endpoint  string
	authToken string
	client    *http.Client
}

type RpcResponse struct {
	Success   bool
	Data      string
	Error     string
	LatencyMs uint64
}

type jsonRpcRequest struct {
	Jsonrpc string        `json:"jsonrpc"`
	ID      int           `json:"id"`
	Method  string        `json:"method"`
	Params  []interface{} `json:"params"`
}

type jsonRpcResponse struct {
	Result json.RawMessage `json:"result"`
	Error  *struct {
		Message string `json:"message"`
	} `json:"error"`
}

type BlockData struct {
	Hash             string `json:"hash"`
	Number           string `json:"number"`
	Timestamp        string `json:"timestamp"`
	ParentHash       string `json:"parentHash"`
	StateRoot        string `json:"stateRoot"`
	TransactionsRoot string `json:"transactionsRoot"`
	ReceiptsRoot     string `json:"receiptsRoot"`
	Miner            string `json:"miner"`
	GasUsed          string `json:"gasUsed"`
	GasLimit         string `json:"gasLimit"`
}

func NewClient(endpoint string, authToken string) *Client {
	return &Client{
		endpoint:  endpoint,
		authToken: authToken,
		client: &http.Client{
			Timeout: 5 * time.Second,
		},
	}
}

// Make a JSON-RPC call to the node
func (c *Client) call(method string, params []interface{}) (json.RawMessage, uint64, error) {
	start := time.Now()

	reqBody := jsonRpcRequest{
		Jsonrpc: "2.0",
		ID:      1,
		Method:  method,
		Params:  params,
	}

	body, err := json.Marshal(reqBody)
	if err != nil {
		return nil, uint64(time.Since(start).Milliseconds()), err
	}

	req, err := http.NewRequest("POST", c.endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, uint64(time.Since(start).Milliseconds()), err
	}

	req.Header.Set("Content-Type", "application/json")
	if c.authToken != "" {
		req.Header.Set("Authorization", "Bearer "+c.authToken)
	}

	resp, err := c.client.Do(req)
	if err != nil {
		return nil, uint64(time.Since(start).Milliseconds()), err
	}
	defer resp.Body.Close()

	latencyMs := uint64(time.Since(start).Milliseconds())

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, latencyMs, err
	}

	var rpcResp jsonRpcResponse
	if err := json.Unmarshal(respBody, &rpcResp); err != nil {
		return nil, latencyMs, err
	}

	if rpcResp.Error != nil {
		return nil, latencyMs, fmt.Errorf(rpcResp.Error.Message)
	}

	return rpcResp.Result, latencyMs, nil
}

// Get current block number
func (c *Client) GetBlockNumber() (uint64, uint64, error) {
	result, latency, err := c.call("eth_blockNumber", []interface{}{})
	if err != nil {
		return 0, latency, err
	}

	var hexStr string
	if err := json.Unmarshal(result, &hexStr); err != nil {
		return 0, latency, err
	}

	blockNum, err := strconv.ParseUint(strings.TrimPrefix(hexStr, "0x"), 16, 64)
	if err != nil {
		return 0, latency, err
	}

	return blockNum, latency, nil
}

// Check if node is synced
func (c *Client) GetSyncStatus() (bool, uint64, error) {
	result, latency, err := c.call("eth_syncing", []interface{}{})
	if err != nil {
		return false, latency, err
	}

	// If result is "false", node is synced
	var syncing bool
	if err := json.Unmarshal(result, &syncing); err == nil {
		return !syncing, latency, nil
	}

	// If it's an object, node is still syncing
	return false, latency, nil
}

// Get block by number
func (c *Client) GetBlockByNumber(blockNumber uint64) (*BlockData, uint64, error) {
	blockHex := fmt.Sprintf("0x%x", blockNumber)
	result, latency, err := c.call("eth_getBlockByNumber", []interface{}{blockHex, false})
	if err != nil {
		return nil, latency, err
	}

	var block BlockData
	if err := json.Unmarshal(result, &block); err != nil {
		return nil, latency, err
	}

	return &block, latency, nil
}

// Get block hash
func (c *Client) GetBlockHash(blockNumber uint64) (string, uint64, error) {
	block, latency, err := c.GetBlockByNumber(blockNumber)
	if err != nil {
		return "", latency, err
	}
	return block.Hash, latency, nil
}

// Get balance at specific block
func (c *Client) GetBalance(address string, blockNumber *uint64) (string, uint64, error) {
	var blockTag interface{}
	if blockNumber != nil {
		blockTag = fmt.Sprintf("0x%x", *blockNumber)
	} else {
		blockTag = "latest"
	}

	result, latency, err := c.call("eth_getBalance", []interface{}{address, blockTag})
	if err != nil {
		return "", latency, err
	}

	var balance string
	if err := json.Unmarshal(result, &balance); err != nil {
		return "", latency, err
	}

	return balance, latency, nil
}

// Get peer count
func (c *Client) GetPeerCount() (uint64, uint64, error) {
	result, latency, err := c.call("net_peerCount", []interface{}{})
	if err != nil {
		return 0, latency, err
	}

	var hexStr string
	if err := json.Unmarshal(result, &hexStr); err != nil {
		return 0, latency, err
	}

	count, err := strconv.ParseUint(strings.TrimPrefix(hexStr, "0x"), 16, 64)
	if err != nil {
		return 0, latency, err
	}

	return count, latency, nil
}

// Execute a challenge and return the answer
func (c *Client) ExecuteChallenge(challenge *types.Challenge) RpcResponse {
	switch challenge.ChallengeType {
	case types.BlockHash:
		blockNum := uint64(0)
		if challenge.Params.BlockNumber != nil {
			blockNum = *challenge.Params.BlockNumber
		}
		hash, latency, err := c.GetBlockHash(blockNum)
		if err != nil {
			return RpcResponse{Success: false, Error: err.Error(), LatencyMs: latency}
		}
		return RpcResponse{Success: true, Data: hash, LatencyMs: latency}

	case types.BlockData:
		blockNum := uint64(0)
		if challenge.Params.BlockNumber != nil {
			blockNum = *challenge.Params.BlockNumber
		}
		block, latency, err := c.GetBlockByNumber(blockNum)
		if err != nil {
			return RpcResponse{Success: false, Error: err.Error(), LatencyMs: latency}
		}
		// Return just the important fields
		data := map[string]string{
			"hash":       block.Hash,
			"parentHash": block.ParentHash,
			"stateRoot":  block.StateRoot,
		}
		jsonData, _ := json.Marshal(data)
		return RpcResponse{Success: true, Data: string(jsonData), LatencyMs: latency}

	case types.StateBalance:
		balance, latency, err := c.GetBalance(challenge.Params.Address, challenge.Params.BlockNumber)
		if err != nil {
			return RpcResponse{Success: false, Error: err.Error(), LatencyMs: latency}
		}
		return RpcResponse{Success: true, Data: balance, LatencyMs: latency}

	case types.SyncStatus:
		synced, latency, err := c.GetSyncStatus()
		if err != nil {
			return RpcResponse{Success: false, Error: err.Error(), LatencyMs: latency}
		}
		data := map[string]bool{"synced": synced}
		jsonData, _ := json.Marshal(data)
		return RpcResponse{Success: true, Data: string(jsonData), LatencyMs: latency}

	default:
		return RpcResponse{Success: false, Error: "unknown challenge type", LatencyMs: 0}
	}
}
