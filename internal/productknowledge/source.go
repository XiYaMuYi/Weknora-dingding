package productknowledge

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
)

var ErrSourceUnavailable = errors.New("knowledge evidence unavailable or changed")

// SourceRecord is internal only: it must never be serialized into an API reply.
type SourceRecord struct {
	Scope                                              Scope
	KnowledgeID, ChunkID, FileHash, Content, ImageInfo string
	Title, Filename                                    string
	Enabled                                            bool
}

// Fingerprint is versioned and scoped, using an unambiguous encoded tuple.
// UpdatedAt is intentionally excluded: unrelated summary updates do not
// invalidate unchanged source facts. Changed text, images or file do.
func (r SourceRecord) Fingerprint() string {
	data, _ := json.Marshal([]any{"source-v1", r.Scope.TenantID, r.Scope.KnowledgeBaseID, r.KnowledgeID, r.ChunkID, r.FileHash, r.Content, r.ImageInfo})
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// SourceReader must enforce document and chunk scope, soft deletion, and
// availability in its backing store. Validation also checks scope defensively.
type SourceReader interface {
	ReadSources(context.Context, Scope, []string) ([]SourceRecord, error)
}

// ValidateSources is separate from structural decoding. Neither an arbitrary
// revision nor a valid UUID is proof that a document is available to this KB.
func (s Snapshot) ValidateSources(ctx context.Context, scope Scope, reader SourceReader) error {
	if err := s.Validate(scope); err != nil {
		return err
	}
	ids := make([]string, 0)
	seen := make(map[string]bool)
	for _, claim := range s.Claims {
		for _, e := range claim.Evidence {
			if !seen[e.ChunkID] {
				ids = append(ids, e.ChunkID)
				seen[e.ChunkID] = true
			}
		}
	}
	if len(ids) == 0 {
		return nil
	}
	if reader == nil {
		return ErrSourceUnavailable
	}
	records, err := reader.ReadSources(ctx, scope, ids)
	if err != nil {
		return err
	}
	byID := make(map[string]SourceRecord, len(records))
	for _, record := range records {
		if record.Scope != scope || !record.Enabled {
			return ErrSourceUnavailable
		}
		if _, duplicate := byID[record.ChunkID]; duplicate {
			return ErrSourceUnavailable
		}
		byID[record.ChunkID] = record
	}
	for _, claim := range s.Claims {
		for _, e := range claim.Evidence {
			r, exists := byID[e.ChunkID]
			if !exists || r.KnowledgeID != e.KnowledgeID || e.SourceFingerprint != r.Fingerprint() {
				return ErrSourceUnavailable
			}
		}
	}
	return nil
}
