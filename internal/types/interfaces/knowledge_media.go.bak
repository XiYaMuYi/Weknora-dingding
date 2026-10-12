package interfaces

import "context"

// KnowledgeMediaService provides media resolution for knowledge base images
type KnowledgeMediaService interface {
	// MediaResolveItem represents a single item to resolve
	// MediaResolveItem is defined in service layer

	// Resolve resolves knowledge_id + chunk_id pairs to media_ids
	// Returns a list of resolved media items with short-lived media_ids
	Resolve(ctx context.Context, tenantID uint64, items []MediaResolveItem) ([]ResolvedMedia, error)

	// ResolveForAccess resolves chunks belonging to resourceTenantID, while
	// binding the short-lived media handles to accessTenantID. Callers must
	// verify the requester's KB permission before invoking this method.
	ResolveForAccess(ctx context.Context, resourceTenantID, accessTenantID uint64, items []MediaResolveItem) ([]ResolvedMedia, error)

	// Fetch retrieves image bytes by media_id
	// Returns image data, content type, and error
	Fetch(ctx context.Context, tenantID uint64, mediaID string) ([]byte, string, error)
}

// MediaResolveItem represents a single item to resolve
type MediaResolveItem struct {
	KnowledgeID string `json:"knowledge_id"`
	ChunkID     string `json:"chunk_id"`
}

// ResolvedMedia represents a resolved media item
type ResolvedMedia struct {
	KnowledgeID string `json:"knowledge_id"`
	ChunkID     string `json:"chunk_id"`
	MediaID     string `json:"media_id"`
	ContentType string `json:"content_type"`
	Alt         string `json:"alt"`
}
