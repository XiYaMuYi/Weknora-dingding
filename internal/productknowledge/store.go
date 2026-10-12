package productknowledge

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

var (
	ErrVersionConflict = errors.New("knowledge catalog version conflict")
	ErrCatalogNotFound = errors.New("knowledge catalog not found")
)

// CatalogRecord is the authoritative scoped catalog. Neo4j is only a derived,
// versioned index. Payload never contains user profiles or commerce data.
type CatalogRecord struct {
	TenantID          uint64 `gorm:"primaryKey;autoIncrement:false"`
	KnowledgeBaseID   string `gorm:"primaryKey;type:varchar(36)"`
	Revision          int64
	ProjectedRevision int64
	Payload           string `gorm:"type:text"`
	UpdatedAt         time.Time
}

func (CatalogRecord) TableName() string { return "product_knowledge_catalogs" }

type SQLStore struct{ db *gorm.DB }

func NewSQLStore(db *gorm.DB) *SQLStore { return &SQLStore{db: db} }

func (s *SQLStore) Get(ctx context.Context, scope Scope) (Snapshot, int64, error) {
	var row CatalogRecord
	err := s.db.WithContext(ctx).Where("tenant_id = ? AND knowledge_base_id = ?", scope.TenantID, scope.KnowledgeBaseID).First(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return Snapshot{}, 0, ErrCatalogNotFound
	}
	if err != nil {
		return Snapshot{}, 0, err
	}
	snapshot, err := DecodeSnapshot([]byte(row.Payload), scope)
	return snapshot, row.Revision, err
}

func (s *SQLStore) Put(ctx context.Context, snapshot Snapshot, expected int64) (int64, error) {
	if expected < 0 {
		return 0, ErrVersionConflict
	}
	if err := snapshot.Validate(snapshot.Scope); err != nil {
		return 0, err
	}
	data, err := json.Marshal(snapshot)
	if err != nil || len(data) > 2*1024*1024 {
		return 0, ErrInvalidSnapshot
	}
	var result *gorm.DB
	if expected == 0 {
		row := CatalogRecord{TenantID: snapshot.Scope.TenantID, KnowledgeBaseID: snapshot.Scope.KnowledgeBaseID, Revision: 1, Payload: string(data), UpdatedAt: time.Now().UTC()}
		result = s.db.WithContext(ctx).Clauses(clause.OnConflict{DoNothing: true}).Create(&row)
	} else {
		result = s.db.WithContext(ctx).Model(&CatalogRecord{}).
			Where("tenant_id = ? AND knowledge_base_id = ? AND revision = ?", snapshot.Scope.TenantID, snapshot.Scope.KnowledgeBaseID, expected).
			Updates(map[string]any{"revision": expected + 1, "payload": string(data), "updated_at": time.Now().UTC()})
	}
	if result.Error != nil {
		return 0, result.Error
	}
	if result.RowsAffected != 1 {
		return 0, ErrVersionConflict
	}
	return expected + 1, nil
}

func (s *SQLStore) MarkProjected(ctx context.Context, scope Scope, revision int64) error {
	result := s.db.WithContext(ctx).Model(&CatalogRecord{}).
		Where("tenant_id = ? AND knowledge_base_id = ? AND revision = ?", scope.TenantID, scope.KnowledgeBaseID, revision).
		Update("projected_revision", revision)
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 {
		return ErrVersionConflict
	}
	return nil
}

// Pending is a durable outbox: a crash between a catalog commit and indexing
// cannot lose work. The reconciler retries unprojected revisions after restart.
func (s *SQLStore) Pending(ctx context.Context, limit int) ([]CatalogRecord, error) {
	var rows []CatalogRecord
	if limit < 1 || limit > 100 {
		limit = 20
	}
	err := s.db.WithContext(ctx).Where("projected_revision < revision").Order("updated_at ASC").Limit(limit).Find(&rows).Error
	return rows, err
}
