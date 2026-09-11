// Package service provides business logic implementations for WeKnora application
package service

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/Tencent/WeKnora/internal/logger"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
	"github.com/redis/go-redis/v9"
)

const (
	// mediaIDTTL is the expiration time for media_id mappings
	mediaIDTTL = 10 * time.Minute

	// mediaIDPrefix is the Redis key prefix for media_id mappings
	mediaIDPrefix = "knowledge_media:"

	// maxImagesPerChunk is the maximum number of images to return per chunk
	maxImagesPerChunk = 3

	// mediaIDLength is the length of the generated media_id
	mediaIDLength = 32
)

// allowedImageTypes defines the allowed image MIME types
var allowedImageTypes = map[string]bool{
	"image/jpeg": true,
	"image/png":  true,
	"image/webp": true,
	"image/gif":  true,
}

// knowledgeMediaService implements media resolution for knowledge base images
type knowledgeMediaService struct {
	chunkRepo  interfaces.ChunkRepository
	fileSvc    interfaces.FileService
	redis      *redis.Client
	hmacSecret []byte
}

// NewKnowledgeMediaService creates a new knowledge media service
func NewKnowledgeMediaService(
	chunkRepo interfaces.ChunkRepository,
	fileSvc interfaces.FileService,
	redisClient *redis.Client,
) interfaces.KnowledgeMediaService {
	// Use JWT_SECRET as HMAC secret for media_id signing
	hmacSecret := os.Getenv("JWT_SECRET")
	if hmacSecret == "" {
		hmacSecret = "default-knowledge-media-secret"
	}

	return &knowledgeMediaService{
		chunkRepo:  chunkRepo,
		fileSvc:    fileSvc,
		redis:      redisClient,
		hmacSecret: []byte(hmacSecret),
	}
}



// Resolve resolves knowledge_id + chunk_id pairs to media_ids
func (s *knowledgeMediaService) Resolve(ctx context.Context, tenantID uint64, items []interfaces.MediaResolveItem) ([]interfaces.ResolvedMedia, error) {
	result := make([]interfaces.ResolvedMedia, 0, len(items)*maxImagesPerChunk)

	for _, item := range items {
		// Get chunk with tenant validation
		chunk, err := s.chunkRepo.GetChunkByID(ctx, tenantID, item.ChunkID)
		if err != nil {
			logger.Warnf(ctx, "[KnowledgeMedia] Failed to get chunk %s: %v", item.ChunkID, err)
			continue
		}

		// Validate knowledge_id matches
		if chunk.KnowledgeID != item.KnowledgeID {
			logger.Warnf(ctx, "[KnowledgeMedia] Knowledge ID mismatch for chunk %s", item.ChunkID)
			continue
		}

		// Parse image_info
		if chunk.ImageInfo == "" {
			continue
		}

		var imageInfos []types.ImageInfo
		if err := json.Unmarshal([]byte(chunk.ImageInfo), &imageInfos); err != nil {
			logger.Warnf(ctx, "[KnowledgeMedia] Failed to parse image_info for chunk %s: %v", item.ChunkID, err)
			continue
		}

		// Process up to maxImagesPerChunk images
		count := 0
		for _, img := range imageInfos {
			if count >= maxImagesPerChunk {
				break
			}

			// Validate URL is oss:// path
			if !strings.HasPrefix(img.URL, "oss://") {
				continue
			}

			// Determine content type from file extension
			contentType := detectContentType(img.URL)
			if !allowedImageTypes[contentType] {
				continue
			}

			// Generate media_id
			mediaID, err := s.generateMediaID(ctx, tenantID, img.URL, contentType)
			if err != nil {
				logger.Errorf(ctx, "[KnowledgeMedia] Failed to generate media_id: %v", err)
				continue
			}

			// Build alt text
			alt := img.Caption
			if alt == "" {
				alt = "相关产品资料图片"
			}

			result = append(result, interfaces.ResolvedMedia{
				KnowledgeID: item.KnowledgeID,
				ChunkID:     item.ChunkID,
				MediaID:     mediaID,
				ContentType: contentType,
				Alt:         alt,
			})

			count++
		}
	}

	return result, nil
}

// Fetch retrieves image bytes by media_id
func (s *knowledgeMediaService) Fetch(ctx context.Context, tenantID uint64, mediaID string) ([]byte, string, error) {
	// Validate media_id format
	if len(mediaID) < 64 {
		return nil, "", fmt.Errorf("invalid media_id format")
	}

	// Extract components from media_id
	// Format: {random_hex}:{hmac_signature}:{tenant_id}:{content_type_hash}:{oss_path_hash}
	parts := strings.Split(mediaID, ":")
	if len(parts) != 5 {
		return nil, "", fmt.Errorf("invalid media_id structure")
	}

	// Verify HMAC signature
	signature := parts[1]
	payload := parts[0] + ":" + parts[2] + ":" + parts[3] + ":" + parts[4]
	if !s.verifyHMAC(payload, signature) {
		return nil, "", fmt.Errorf("invalid media_id signature")
	}

	// Check Redis for mapping
	redisKey := mediaIDPrefix + mediaID
	ossPath, err := s.redis.Get(ctx, redisKey).Result()
	if err == redis.Nil {
		return nil, "", fmt.Errorf("media_id expired or not found")
	}
	if err != nil {
		return nil, "", fmt.Errorf("failed to get media mapping: %w", err)
	}

	// Parse stored data (format: tenant_id:content_type:oss_path)
	storedParts := strings.SplitN(ossPath, ":", 3)
	if len(storedParts) != 3 {
		return nil, "", fmt.Errorf("invalid stored media data")
	}

	storedTenantID := storedParts[0]
	contentType := storedParts[1]
	ossPathValue := storedParts[2]

	// Validate tenant ID
	if fmt.Sprintf("%d", tenantID) != storedTenantID {
		return nil, "", fmt.Errorf("tenant mismatch")
	}

	// Validate content type
	if !allowedImageTypes[contentType] {
		return nil, "", fmt.Errorf("invalid content type")
	}

	// Read file from OSS
	reader, err := s.fileSvc.GetFile(ctx, ossPathValue)
	if err != nil {
		logger.Errorf(ctx, "[KnowledgeMedia] Failed to read file %s: %v", ossPathValue, err)
		return nil, "", fmt.Errorf("failed to read file: %w", err)
	}
	defer reader.Close()

	data, err := io.ReadAll(reader)
	if err != nil {
		return nil, "", fmt.Errorf("failed to read file data: %w", err)
	}

	return data, contentType, nil
}

// generateMediaID generates a secure, time-limited media_id
func (s *knowledgeMediaService) generateMediaID(ctx context.Context, tenantID uint64, ossPath, contentType string) (string, error) {
	// Generate random component
	randomBytes := make([]byte, mediaIDLength/2)
	if _, err := rand.Read(randomBytes); err != nil {
		return "", fmt.Errorf("failed to generate random bytes: %w", err)
	}
	randomHex := hex.EncodeToString(randomBytes)

	// Build payload
	tenantStr := fmt.Sprintf("%d", tenantID)
	contentTypeHash := hashString(contentType)
	ossPathHash := hashString(ossPath)
	payload := fmt.Sprintf("%s:%s:%s:%s", randomHex, tenantStr, contentTypeHash, ossPathHash)

	// Generate HMAC signature
	signature := s.generateHMAC(payload)

	// Build media_id
	mediaID := fmt.Sprintf("%s:%s:%s:%s:%s", randomHex, signature, tenantStr, contentTypeHash, ossPathHash)

	// Store in Redis with TTL
	redisKey := mediaIDPrefix + mediaID
	redisValue := fmt.Sprintf("%s:%s:%s", tenantStr, contentType, ossPath)
	if err := s.redis.Set(ctx, redisKey, redisValue, mediaIDTTL).Err(); err != nil {
		return "", fmt.Errorf("failed to store media mapping: %w", err)
	}

	return mediaID, nil
}

// generateHMAC generates HMAC-SHA256 signature
func (s *knowledgeMediaService) generateHMAC(payload string) string {
	mac := hmac.New(sha256.New, s.hmacSecret)
	mac.Write([]byte(payload))
	return hex.EncodeToString(mac.Sum(nil))
}

// verifyHMAC verifies HMAC-SHA256 signature
func (s *knowledgeMediaService) verifyHMAC(payload, signature string) bool {
	expected := s.generateHMAC(payload)
	return hmac.Equal([]byte(expected), []byte(signature))
}

// detectContentType detects content type from file path
func detectContentType(path string) string {
	ext := strings.ToLower(filepath.Ext(path))
	switch ext {
	case ".jpg", ".jpeg":
		return "image/jpeg"
	case ".png":
		return "image/png"
	case ".webp":
		return "image/webp"
	case ".gif":
		return "image/gif"
	default:
		return "application/octet-stream"
	}
}

// hashString generates SHA256 hash of a string
func hashString(s string) string {
	hash := sha256.Sum256([]byte(s))
	return hex.EncodeToString(hash[:])
}
