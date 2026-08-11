package routes

import (
	"github.com/Wei-Shaw/sub2api/internal/media"
	"github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/gin-gonic/gin"
)

func RegisterMediaRoutes(r *gin.Engine, handler *media.Handler, apiKeyAuth middleware.APIKeyAuthMiddleware) {
	group := r.Group("/v1/media")
	group.Use(gin.HandlerFunc(apiKeyAuth))
	group.GET("/models", handler.Models)
	group.POST("/quotes", handler.Quote)
	group.POST("/orders", handler.CreateOrder)
	group.GET("/orders/:order_id", handler.GetOrder)
	group.GET("/orders/:order_id/artifacts/:artifact_id", handler.AuthorizeArtifact)
}
