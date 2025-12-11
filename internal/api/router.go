package api

import (
	"github.com/depinonbnb/depin/internal/store"
	"github.com/depinonbnb/depin/internal/verification"
	"github.com/gin-gonic/gin"
)

func SetupRouter(store *store.Store, verifier *verification.Verifier) *gin.Engine {
	router := gin.Default()

	// Enable CORS
	router.Use(func(c *gin.Context) {
		c.Header("Access-Control-Allow-Origin", "*")
		c.Header("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
		c.Header("Access-Control-Allow-Headers", "Content-Type, Authorization")
		if c.Request.Method == "OPTIONS" {
			c.AbortWithStatus(204)
			return
		}
		c.Next()
	})

	handlers := NewHandlers(store, verifier)

	// Health check
	router.GET("/health", func(c *gin.Context) {
		c.JSON(200, gin.H{"status": "ok"})
	})

	api := router.Group("/api")
	{
		// Node registration
		api.POST("/nodes/register", handlers.RegisterNode)
		api.GET("/nodes/:nodeId", handlers.GetNode)
		api.GET("/nodes/wallet/:walletAddress", handlers.GetNodesByWallet)
		api.GET("/nodes/:nodeId/stats", handlers.GetNodeStats)

		// Challenges (for local-prover)
		api.GET("/challenges/request", handlers.RequestChallenge)
		api.POST("/challenges/submit", handlers.SubmitChallenge)

		// Direct verification (for exposed-rpc)
		api.POST("/verify/:nodeId", handlers.VerifyNode)
		api.GET("/verify/:nodeId/heartbeat", handlers.CheckHeartbeat)

		// Public data
		api.GET("/leaderboard", handlers.GetLeaderboard)
		api.GET("/stats", handlers.GetNetworkStats)
	}

	return router
}
