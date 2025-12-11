package main

// ===========================================
// DePIN BNB Local Prover
// ===========================================
// This is the open-source script that users run to prove they're running a node.
// You can read every line of this code - nothing hidden.
//
// How to run:
//   go run cmd/prover/main.go --private-key YOUR_KEY
//
// Or build and run:
//   go build -o prover cmd/prover/main.go
//   ./prover --private-key YOUR_KEY

import (
	"bytes"
	"crypto/ecdsa"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/depinonbnb/depin/internal/rpc"
	"github.com/depinonbnb/depin/internal/types"
	"github.com/ethereum/go-ethereum/crypto"
)

type Config struct {
	PrivateKey  string
	NodeRPC     string
	APIEndpoint string
	NodeType    types.NodeType
	IntervalMs  int
}

type Prover struct {
	config     Config
	privateKey *ecdsa.PrivateKey
	address    string
	nodeRPC    *rpc.Client
	nodeID     string
	running    bool
}

type ChallengeResponse struct {
	Challenge struct {
		ID            string                `json:"id"`
		ChallengeType types.ChallengeType   `json:"challenge_type"`
		Params        types.ChallengeParams `json:"params"`
		ExpiresAt     int64                 `json:"expires_at"`
	} `json:"challenge"`
	ServerTime int64 `json:"server_time"`
}

type SubmitResponse struct {
	Passed        bool   `json:"passed"`
	FailureReason string `json:"failure_reason"`
}

func NewProver(config Config) (*Prover, error) {
	// Parse private key
	privateKey, err := crypto.HexToECDSA(strings.TrimPrefix(config.PrivateKey, "0x"))
	if err != nil {
		return nil, fmt.Errorf("invalid private key: %v", err)
	}

	address := crypto.PubkeyToAddress(privateKey.PublicKey).Hex()

	return &Prover{
		config:     config,
		privateKey: privateKey,
		address:    address,
		nodeRPC:    rpc.NewClient(config.NodeRPC, ""),
	}, nil
}

func (p *Prover) Start() error {
	fmt.Println("============================================================")
	fmt.Println("DePIN BNB Local Prover")
	fmt.Println("============================================================")
	fmt.Printf("Wallet: %s\n", p.address)
	fmt.Printf("Node RPC: %s\n", p.config.NodeRPC)
	fmt.Printf("API: %s\n", p.config.APIEndpoint)
	fmt.Printf("Node Type: %s\n", p.config.NodeType)
	fmt.Println("============================================================")

	// Check if we can connect to the local node
	blockNum, _, err := p.nodeRPC.GetBlockNumber()
	if err != nil {
		return fmt.Errorf("cannot connect to local node: %v", err)
	}

	synced, _, _ := p.nodeRPC.GetSyncStatus()
	fmt.Printf("Local node connected - Block #%d\n", blockNum)
	fmt.Printf("Synced: %v\n", synced)

	if !synced {
		return fmt.Errorf("node is not fully synced - please wait for sync to complete")
	}

	// Register with the API
	if err := p.register(); err != nil {
		return fmt.Errorf("registration failed: %v", err)
	}

	// Start the proof loop
	p.running = true
	fmt.Println("\nStarting proof loop...\n")

	for p.running {
		if err := p.submitProof(); err != nil {
			log.Printf("proof submission error: %v", err)
		}
		time.Sleep(time.Duration(p.config.IntervalMs) * time.Millisecond)
	}

	return nil
}

func (p *Prover) Stop() {
	fmt.Println("\nStopping prover...")
	p.running = false
}

func (p *Prover) signMessage(message string) (string, error) {
	// Hash with Ethereum prefix
	prefixedMessage := fmt.Sprintf("\x19Ethereum Signed Message:\n%d%s", len(message), message)
	hash := crypto.Keccak256Hash([]byte(prefixedMessage))

	// Sign
	sig, err := crypto.Sign(hash.Bytes(), p.privateKey)
	if err != nil {
		return "", err
	}

	// Ethereum uses v = 27 or 28
	sig[64] += 27

	return "0x" + hex.EncodeToString(sig), nil
}

func (p *Prover) register() error {
	fmt.Println("Registering node with API...")

	timestamp := time.Now().UnixMilli()
	message := fmt.Sprintf("Register node\nWallet: %s\nType: %s\nTimestamp: %d", p.address, p.config.NodeType, timestamp)
	signature, err := p.signMessage(message)
	if err != nil {
		return err
	}

	body := map[string]interface{}{
		"wallet_address":      p.address,
		"node_type":           p.config.NodeType,
		"verification_method": "local-prover",
		"signature":           signature,
		"timestamp":           timestamp,
	}

	jsonBody, _ := json.Marshal(body)
	resp, err := http.Post(p.config.APIEndpoint+"/nodes/register", "application/json", bytes.NewReader(jsonBody))
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	respBody, _ := io.ReadAll(resp.Body)

	if resp.StatusCode != 200 {
		return fmt.Errorf("registration failed: %s", string(respBody))
	}

	var result struct {
		NodeID string `json:"node_id"`
	}
	json.Unmarshal(respBody, &result)
	p.nodeID = result.NodeID

	fmt.Printf("Registered successfully - Node ID: %s\n", p.nodeID)
	return nil
}

func (p *Prover) submitProof() error {
	startTime := time.Now()

	// Step 1: Get a challenge from the server
	fmt.Printf("[%s] Requesting challenge...\n", time.Now().Format(time.RFC3339))

	resp, err := http.Get(fmt.Sprintf("%s/challenges/request?nodeId=%s", p.config.APIEndpoint, p.nodeID))
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("failed to get challenge: %s", string(body))
	}

	var challengeResp ChallengeResponse
	json.NewDecoder(resp.Body).Decode(&challengeResp)

	blockNum := "N/A"
	if challengeResp.Challenge.Params.BlockNumber != nil {
		blockNum = fmt.Sprintf("%d", *challengeResp.Challenge.Params.BlockNumber)
	}
	fmt.Printf("  Challenge: %s (Block #%s)\n", challengeResp.Challenge.ChallengeType, blockNum)

	// Step 2: Ask our local node for the answer
	queryStart := time.Now()
	challenge := &types.Challenge{
		ID:            challengeResp.Challenge.ID,
		ChallengeType: challengeResp.Challenge.ChallengeType,
		Params:        challengeResp.Challenge.Params,
	}
	nodeResponse := p.nodeRPC.ExecuteChallenge(challenge)
	queryTime := time.Since(queryStart).Milliseconds()

	if !nodeResponse.Success {
		fmt.Printf("  FAILED: %s\n", nodeResponse.Error)
		return nil
	}

	fmt.Printf("  Query time: %dms\n", queryTime)

	// Step 3: Sign the response
	timestamp := time.Now().UnixMilli()
	message := fmt.Sprintf("Challenge Response\nID: %s\nAnswer: %s\nTimestamp: %d", challenge.ID, nodeResponse.Data, timestamp)
	signature, err := p.signMessage(message)
	if err != nil {
		return err
	}

	// Step 4: Send the answer back
	submitBody := map[string]interface{}{
		"challenge_id":     challenge.ID,
		"node_id":          p.nodeID,
		"answer":           nodeResponse.Data,
		"signature":        signature,
		"response_time_ms": queryTime,
		"timestamp":        timestamp,
	}

	jsonBody, _ := json.Marshal(submitBody)
	submitResp, err := http.Post(p.config.APIEndpoint+"/challenges/submit", "application/json", bytes.NewReader(jsonBody))
	if err != nil {
		return err
	}
	defer submitResp.Body.Close()

	var result SubmitResponse
	json.NewDecoder(submitResp.Body).Decode(&result)

	totalTime := time.Since(startTime).Milliseconds()

	if result.Passed {
		fmt.Printf("  PASSED (Total: %dms)\n", totalTime)
	} else {
		fmt.Printf("  FAILED: %s\n", result.FailureReason)
	}

	return nil
}

func main() {
	privateKey := flag.String("private-key", "", "Your wallet private key")
	nodeRPC := flag.String("node-rpc", "http://localhost:8545", "Your node RPC endpoint")
	apiEndpoint := flag.String("api", "http://localhost:3000/api", "DePIN API endpoint")
	nodeType := flag.String("node-type", "bsc-full", "Node type: bsc-full, bsc-fast, opbnb-full, etc.")
	intervalMs := flag.Int("interval", 300000, "Proof interval in milliseconds (default: 5 min)")

	flag.Parse()

	// Also check environment variables
	if *privateKey == "" {
		*privateKey = os.Getenv("PROVER_PRIVATE_KEY")
	}
	if os.Getenv("NODE_RPC") != "" {
		*nodeRPC = os.Getenv("NODE_RPC")
	}
	if os.Getenv("DEPIN_API") != "" {
		*apiEndpoint = os.Getenv("DEPIN_API")
	}
	if os.Getenv("NODE_TYPE") != "" {
		*nodeType = os.Getenv("NODE_TYPE")
	}

	if *privateKey == "" {
		fmt.Println("ERROR: Private key required")
		fmt.Println("")
		fmt.Println("Usage: prover --private-key YOUR_KEY [options]")
		fmt.Println("")
		fmt.Println("Options:")
		fmt.Println("  --private-key   Your wallet private key (or set PROVER_PRIVATE_KEY env)")
		fmt.Println("  --node-rpc      Your node RPC endpoint (default: http://localhost:8545)")
		fmt.Println("  --api           DePIN API endpoint (default: http://localhost:3000/api)")
		fmt.Println("  --node-type     Node type: bsc-full, bsc-fast, opbnb-full, etc.")
		fmt.Println("  --interval      Proof interval in ms (default: 300000 = 5 min)")
		os.Exit(1)
	}

	prover, err := NewProver(Config{
		PrivateKey:  *privateKey,
		NodeRPC:     *nodeRPC,
		APIEndpoint: *apiEndpoint,
		NodeType:    types.NodeType(*nodeType),
		IntervalMs:  *intervalMs,
	})
	if err != nil {
		log.Fatalf("failed to create prover: %v", err)
	}

	// Handle shutdown
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

	go func() {
		<-sigCh
		prover.Stop()
	}()

	if err := prover.Start(); err != nil {
		log.Fatalf("prover error: %v", err)
	}
}
