package productknowledge

import (
	"context"

	"gorm.io/gorm"
)

// SQLSourceReader reuses the existing WeKnora DB and source tables. It introduces
// no storage credentials and never trusts IDs alone as permission evidence.
type SQLSourceReader struct{ db *gorm.DB }

func NewSQLSourceReader(db *gorm.DB) *SQLSourceReader { return &SQLSourceReader{db: db} }

func (r *SQLSourceReader) ReadSources(ctx context.Context, scope Scope, ids []string) ([]SourceRecord, error) {
	if scope.TenantID == 0 || !validID(scope.KnowledgeBaseID) {
		return nil, ErrScope
	}
	if len(ids) > 5000 {
		return nil, ErrInvalidSnapshot
	}
	if len(ids) == 0 {
		return []SourceRecord{}, nil
	}
	if r == nil || r.db == nil {
		return nil, ErrSourceUnavailable
	}
	for _, id := range ids {
		if !validID(id) {
			return nil, ErrInvalidSnapshot
		}
	}
	records := make([]SourceRecord, 0, len(ids))
	// Small batches work within both SQLite and PostgreSQL bind limits and avoid
	// a per-evidence N+1 query when several products share a source chunk.
	for start := 0; start < len(ids); start += 200 {
		end := min(start+200, len(ids))
		var batch []struct {
			KnowledgeID, ChunkID, FileHash, Content, ImageInfo string
			Title, Filename                                    string
		}
		err := r.db.WithContext(ctx).Table("chunks AS c").
			Select("c.id AS chunk_id, c.knowledge_id, c.content, c.image_info, k.file_hash, k.title, k.file_name AS filename").
			Joins("JOIN knowledges AS k ON k.id = c.knowledge_id AND k.tenant_id = c.tenant_id AND k.knowledge_base_id = c.knowledge_base_id").
			Where("c.tenant_id = ? AND c.knowledge_base_id = ? AND c.id IN ?", scope.TenantID, scope.KnowledgeBaseID, ids[start:end]).
			Where("c.deleted_at IS NULL AND k.deleted_at IS NULL AND c.is_enabled = ? AND k.enable_status = ?", true, "enabled").
			Where("k.parse_status IN ?", []string{"completed", "finalizing"}).
			Scan(&batch).Error
		if err != nil {
			return nil, err
		}
		for _, row := range batch {
			records = append(records, SourceRecord{Scope: scope, Enabled: true,
				KnowledgeID: row.KnowledgeID, ChunkID: row.ChunkID, FileHash: row.FileHash,
				Content: row.Content, ImageInfo: row.ImageInfo})
			records[len(records)-1].Title, records[len(records)-1].Filename = row.Title, row.Filename
		}
	}
	return records, nil
}
