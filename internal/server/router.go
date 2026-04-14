package server

import (
	"net/http"

	"github.com/gin-gonic/gin"
)

// SetupRouter configures all the routes and their handlers.
func SetupRouter(r *gin.Engine) {
	api := r.Group("/api/v1")
	{
		api.GET("/ping", handlePing)
		api.GET("/hello", handleHello)
	}
}

func handlePing(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{
		"message": "pong",
	})
}

func handleHello(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{
		"message": "hello from dango",
	})
}
