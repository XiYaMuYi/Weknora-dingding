package handler

import (
	"github.com/Tencent/WeKnora/internal/productknowledge"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/gin-gonic/gin"
)

type ProductKnowledgeHandler struct{ handler *productknowledge.HTTPHandler }

func NewProductKnowledgeHandler(service *productknowledge.Service) *ProductKnowledgeHandler {
	return &ProductKnowledgeHandler{handler: productknowledge.NewHTTPHandler(service)}
}
func productKnowledgeScope(c *gin.Context) (productknowledge.Scope, bool) {
	tenant, ok := types.TenantIDFromContext(c.Request.Context())
	if !ok || tenant == 0 {
		c.AbortWithStatusJSON(401, gin.H{"success": false, "error": gin.H{"code": "UNAUTHORIZED"}})
		return productknowledge.Scope{}, false
	}
	return productknowledge.Scope{TenantID: tenant, KnowledgeBaseID: c.Param("id")}, true
}
func (h *ProductKnowledgeHandler) Get(c *gin.Context) {
	if scope, ok := productKnowledgeScope(c); ok {
		h.handler.Get(c, scope)
	}
}
func (h *ProductKnowledgeHandler) Put(c *gin.Context) {
	if scope, ok := productKnowledgeScope(c); ok {
		h.handler.Put(c, scope)
	}
}
func (h *ProductKnowledgeHandler) Query(c *gin.Context) {
	if scope, ok := productKnowledgeScope(c); ok {
		h.handler.Query(c, scope)
	}
}
func (h *ProductKnowledgeHandler) CreateEntity(c *gin.Context) {
	if scope, ok := productKnowledgeScope(c); ok {
		h.handler.CreateEntity(c, scope)
	}
}
func (h *ProductKnowledgeHandler) IngestCandidates(c *gin.Context) {
	if scope, ok := productKnowledgeScope(c); ok {
		h.handler.IngestCandidates(c, scope)
	}
}

