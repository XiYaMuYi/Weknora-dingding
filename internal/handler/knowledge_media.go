package handler

import (
	"net/http"

	"github.com/Tencent/WeKnora/internal/logger"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
	"github.com/gin-gonic/gin"
)

// KnowledgeMediaHandler handles knowledge media resolution APIs
type KnowledgeMediaHandler struct {
	service interfaces.KnowledgeMediaService
}

// NewKnowledgeMediaHandler creates a new knowledge media handler
func NewKnowledgeMediaHandler(service interfaces.KnowledgeMediaService) *KnowledgeMediaHandler {
	return &KnowledgeMediaHandler{
		service: service,
	}
}

// ResolveRequest represents the request body for POST /knowledge-media/resolve
type ResolveRequest struct {
	Items []interfaces.MediaResolveItem `json:"items" binding:"required"`
}

// ResolveResponse represents the response body for POST /knowledge-media/resolve
type ResolveResponse struct {
	Success bool `json:"success"`
	Data    struct {
		Items []interfaces.ResolvedMedia `json:"items"`
	} `json:"data"`
}

// Resolve handles POST /api/v1/knowledge-media/resolve
// @Summary Resolve knowledge media to short-lived media_ids
// @Description Resolves knowledge_id + chunk_id pairs to short-lived media_ids for image access
// @Tags Knowledge Media
// @Accept json
// @Produce json
// @Param request body ResolveRequest true "Resolve request"
// @Success 200 {object} ResolveResponse
// @Failure 400 {object} map[string]interface{}
// @Failure 401 {object} map[string]interface{}
// @Failure 403 {object} map[string]interface{}
// @Router /knowledge-media/resolve [post]
func (h *KnowledgeMediaHandler) Resolve(c *gin.Context) {
	ctx := c.Request.Context()

	// Get tenant ID from context (set by API key middleware)
	tenantID, exists := c.Get("tenant_id")
	if !exists {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "tenant not found"})
		return
	}

	tenantIDUint, ok := tenantID.(uint64)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "invalid tenant_id"})
		return
	}

	// Parse request
	var req ResolveRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request: " + err.Error()})
		return
	}

	// Validate request
	if len(req.Items) == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "items cannot be empty"})
		return
	}

	if len(req.Items) > 100 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "too many items, max 100"})
		return
	}

	// Resolve media
	resolved, err := h.service.Resolve(ctx, tenantIDUint, req.Items)
	if err != nil {
		logger.Errorf(ctx, "[KnowledgeMedia] Failed to resolve media: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to resolve media"})
		return
	}

	// Return response
	resp := ResolveResponse{
		Success: true,
	}
	resp.Data.Items = resolved

	c.JSON(http.StatusOK, resp)
}

// Fetch handles GET /api/v1/knowledge-media/:media_id
// @Summary Fetch image by media_id
// @Description Retrieves image bytes by media_id
// @Tags Knowledge Media
// @Produce image/jpeg,image/png,image/webp,image/gif
// @Param media_id path string true "Media ID"
// @Success 200 {file} binary
// @Failure 400 {object} map[string]interface{}
// @Failure 401 {object} map[string]interface{}
// @Failure 403 {object} map[string]interface{}
// @Failure 404 {object} map[string]interface{}
// @Router /knowledge-media/{media_id} [get]
func (h *KnowledgeMediaHandler) Fetch(c *gin.Context) {
	ctx := c.Request.Context()

	// Get tenant ID from context
	tenantID, exists := c.Get("tenant_id")
	if !exists {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "tenant not found"})
		return
	}

	tenantIDUint, ok := tenantID.(uint64)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "invalid tenant_id"})
		return
	}

	// Get media_id from path
	mediaID := c.Param("media_id")
	if mediaID == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "media_id is required"})
		return
	}

	// Fetch image
	data, contentType, err := h.service.Fetch(ctx, tenantIDUint, mediaID)
	if err != nil {
		logger.Warnf(ctx, "[KnowledgeMedia] Failed to fetch media %s: %v", mediaID, err)
		// Return 404 for all errors to avoid leaking information
		c.JSON(http.StatusNotFound, gin.H{"error": "media not found or expired"})
		return
	}

	// Set headers
	c.Header("Content-Type", contentType)
	c.Header("X-Content-Type-Options", "nosniff")
	c.Header("Cache-Control", "private, max-age=300")

	// Return image data
	c.Data(http.StatusOK, contentType, data)
}
