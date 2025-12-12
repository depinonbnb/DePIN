package main

import (
	"fmt"
	"log"
	"os"
	"time"

	"github.com/depinonbnb/depin/internal/api"
	"github.com/depinonbnb/depin/internal/store"
	"github.com/depinonbnb/depin/internal/verification"
	"github.com/joho/godotenv"
)

func main() {
	// Load .env file if it exists
	godotenv.Load()

	port := os.Getenv("PORT")
	if port == "" {
		port = "3000"
	}

	trustedRPC := os.Getenv("TRUSTED_RPC")
	if trustedRPC == "" {
		trustedRPC = "https://bsc-dataseed1.binance.org"
	}

	adminAPIKey := os.Getenv("ADMIN_API_KEY")

	fmt.Println("============================================================")
	fmt.Println("DePIN BNB Verification Server")
	fmt.Println("============================================================")
	fmt.Printf("Trusted RPC: %s\n", trustedRPC)
	fmt.Printf("Port: %s\n", port)
	if adminAPIKey != "" {
		fmt.Println("Admin API Key: [configured]")
	} else {
		fmt.Println("Admin API Key: [NOT SET - admin endpoints unprotected!]")
	}
	fmt.Println("============================================================")

	// Initialize components
	nodeStore := store.NewStore()
	verifier := verification.NewVerifier(trustedRPC)

	// Start cleanup goroutine
	go func() {
		ticker := time.NewTicker(1 * time.Minute)
		for range ticker.C {
			cleaned := verifier.CleanupExpiredChallenges()
			if cleaned > 0 {
				log.Printf("cleaned up %d expired challenges", cleaned)
			}
		}
	}()

	// Setup router
	router := api.SetupRouter(nodeStore, verifier, adminAPIKey)

	fmt.Println("")
	fmt.Println("Endpoints:")
	fmt.Println("  POST /api/nodes/register     - Register a new node")
	fmt.Println("  GET  /api/nodes/:id          - Get node details")
	fmt.Println("  GET  /api/nodes/:id/stats    - Get node statistics")
	fmt.Println("  GET  /api/challenges/request - Request a challenge")
	fmt.Println("  POST /api/challenges/submit  - Submit challenge response")
	fmt.Println("  POST /api/verify/:id         - Verify exposed-rpc node")
	fmt.Println("  GET  /api/leaderboard        - Get top nodes")
	fmt.Println("  GET  /api/stats              - Get network stats")
	fmt.Println("============================================================")
	fmt.Println("Server ready!")
	fmt.Println("")

	// Start server
	if err := router.Run(":" + port); err != nil {
		log.Fatalf("failed to start server: %v", err)
	}
}
