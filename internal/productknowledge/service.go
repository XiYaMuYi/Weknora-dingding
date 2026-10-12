package productknowledge

import (
	"context"
	"errors"
	"time"
)

type Graph interface {
	Project(context.Context, Snapshot, int64) error
	Paths(context.Context, Scope, int64, []string, int, int) ([][]string, error)
}

type Service struct {
	Store   *SQLStore
	Sources SourceReader
	graph   Graph
	wake    chan struct{}
}

func NewService(store *SQLStore, sources SourceReader, graph Graph) *Service {
	return &Service{Store: store, Sources: sources, graph: graph, wake: make(chan struct{}, 1)}
}

type Query struct {
	EntityIDs []string `json:"entity_ids,omitempty"`
	ChunkIDs  []string `json:"chunk_ids,omitempty"`
	MaxHops   int      `json:"max_hops,omitempty"`
	Limit     int      `json:"limit,omitempty"`
}

func (q Query) Validate() error {
	if q.MaxHops < 0 || q.MaxHops > 3 || q.Limit < 0 || q.Limit > 50 || len(q.EntityIDs) > 20 || len(q.ChunkIDs) > 30 {
		return ErrInvalidSnapshot
	}
	for _, ids := range [][]string{q.EntityIDs, q.ChunkIDs} {
		for _, id := range ids {
			if !validID(id) {
				return ErrInvalidSnapshot
			}
		}
	}
	return nil
}

type Source struct {
	KnowledgeID string `json:"knowledge_id"`
	ChunkID     string `json:"chunk_id"`
	Content     string `json:"content"`
	Title       string `json:"title"`
	Filename    string `json:"filename"`
}
type QueryResult struct {
	IndexVersion int64      `json:"index_version"`
	Entities     []Entity   `json:"entities"`
	Claims       []Claim    `json:"claims"`
	Sources      []Source   `json:"sources"`
	Paths        [][]string `json:"paths"`
	Degraded     bool       `json:"degraded"`
}

func emptyResult() QueryResult {
	return QueryResult{Entities: []Entity{}, Claims: []Claim{}, Sources: []Source{}, Paths: [][]string{}}
}

// Put is only for RBAC-authorized catalog editors. Async extraction must submit
// pending claims. A missing fingerprint is bound to current server-read data;
// a supplied stale fingerprint is rejected rather than silently re-approved.
func (s *Service) Put(ctx context.Context, snapshot Snapshot, expected int64) (int64, error) {
	if err := snapshot.Validate(snapshot.Scope); err != nil {
		return 0, err
	}
	old, version, err := s.Store.Get(ctx, snapshot.Scope)
	if err != nil && !errors.Is(err, ErrCatalogNotFound) {
		return 0, err
	}
	if version != expected {
		return 0, ErrVersionConflict
	}
	kinds := map[string]Kind{}
	for _, entity := range old.Entities {
		kinds[entity.ID] = entity.Kind
	}
	for _, entity := range snapshot.Entities {
		if kind, exists := kinds[entity.ID]; exists && kind != entity.Kind {
			return 0, ErrInvalidSnapshot
		}
	}
	ids := evidenceIDs(snapshot.Claims)
	records, err := s.Sources.ReadSources(ctx, snapshot.Scope, ids)
	if err != nil {
		return 0, err
	}
	byID := sourceMap(records)
	// Clone slices before server-side binding; never mutate callers' snapshots.
	claims := make([]Claim, len(snapshot.Claims))
	copy(claims, snapshot.Claims)
	snapshot.Claims = claims
	for i := range snapshot.Claims {
		claim := &snapshot.Claims[i]
		claim.Evidence = append([]Evidence(nil), claim.Evidence...)
		for j := range claim.Evidence {
			e := &claim.Evidence[j]
			r, exists := byID[e.ChunkID]
			if !exists || r.Scope != snapshot.Scope || r.KnowledgeID != e.KnowledgeID || !r.Enabled {
				return 0, ErrSourceUnavailable
			}
			if e.SourceFingerprint != "" && e.SourceFingerprint != r.Fingerprint() {
				return 0, ErrSourceUnavailable
			}
			e.SourceFingerprint = r.Fingerprint()
		}
	}
	version, err = s.Store.Put(ctx, snapshot, expected)
	if err == nil {
		select {
		case s.wake <- struct{}{}:
		default:
		}
	}
	return version, err
}

func evidenceIDs(claims []Claim) []string {
	seen := map[string]bool{}
	ids := []string{}
	for _, c := range claims {
		for _, e := range c.Evidence {
			if !seen[e.ChunkID] {
				seen[e.ChunkID] = true
				ids = append(ids, e.ChunkID)
			}
		}
	}
	return ids
}
func sourceMap(records []SourceRecord) map[string]SourceRecord {
	m := map[string]SourceRecord{}
	for _, r := range records {
		m[r.ChunkID] = r
	}
	return m
}

func (s *Service) Query(ctx context.Context, scope Scope, q Query) (QueryResult, error) {
	result := emptyResult()
	if err := q.Validate(); err != nil {
		return result, err
	}
	snapshot, version, err := s.Store.Get(ctx, scope)
	if errors.Is(err, ErrCatalogNotFound) {
		return result, nil
	}
	if err != nil {
		return result, err
	}
	result.IndexVersion = version
	if q.MaxHops == 0 {
		q.MaxHops = 2
	}
	if q.Limit == 0 {
		q.Limit = 20
	}
	chunks, seeds, selected := map[string]bool{}, map[string]bool{}, map[string]bool{}
	for _, id := range q.ChunkIDs {
		chunks[id] = true
	}
	for _, id := range q.EntityIDs {
		seeds[id] = true
	}
	for _, c := range snapshot.Claims {
		if c.Status == Confirmed {
			for _, e := range c.Evidence {
				if chunks[e.ChunkID] {
					seeds[c.SubjectID] = true
					seeds[c.ObjectID] = true
					selected[c.ID] = true
				}
			}
		}
	}
	if len(seeds) == 0 {
		return result, nil
	}
	seedIDs := []string{}
	for _, e := range snapshot.Entities {
		if seeds[e.ID] {
			seedIDs = append(seedIDs, e.ID)
		}
	}
	if len(seedIDs) > 30 {
		seedIDs = seedIDs[:30]
	}
	// Direct assertions remain available if the derived graph is rebuilding.
	for _, c := range snapshot.Claims {
		if c.Status == Confirmed && (seeds[c.SubjectID] || seeds[c.ObjectID]) {
			selected[c.ID] = true
		}
	}
	var paths [][]string
	if s.graph != nil {
		graphCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
		paths, err = s.graph.Paths(graphCtx, scope, version, seedIDs, q.MaxHops, q.Limit)
		cancel()
		result.Degraded = err != nil
		if err == nil {
			for _, path := range paths {
				for _, id := range path {
					selected[id] = true
				}
			}
		}
	} else {
		result.Degraded = true
	}
	candidates := []Claim{}
	for _, c := range snapshot.Claims {
		if selected[c.ID] && c.Status == Confirmed {
			candidates = append(candidates, c)
			if len(candidates) == q.Limit {
				break
			}
		}
	}
	records, err := s.Sources.ReadSources(ctx, scope, evidenceIDs(candidates))
	if err != nil {
		result.Degraded = true
		return result, nil
	}
	byID := sourceMap(records)
	entities, sources, validClaims := map[string]bool{}, map[string]bool{}, map[string]bool{}
	for _, c := range candidates {
		valid := true
		for _, e := range c.Evidence {
			r, exists := byID[e.ChunkID]
			if !exists || r.Scope != scope || !r.Enabled || r.KnowledgeID != e.KnowledgeID || r.Fingerprint() != e.SourceFingerprint {
				valid = false
				break
			}
		}
		if !valid {
			continue
		}
		result.Claims = append(result.Claims, c)
		validClaims[c.ID] = true
		entities[c.SubjectID] = true
		entities[c.ObjectID] = true
		for _, e := range c.Evidence {
			if !sources[e.ChunkID] {
				r := byID[e.ChunkID]
				result.Sources = append(result.Sources, Source{KnowledgeID: r.KnowledgeID, ChunkID: r.ChunkID, Content: r.Content, Title: r.Title, Filename: r.Filename})
				sources[e.ChunkID] = true
			}
		}
	}
	for _, e := range snapshot.Entities {
		if entities[e.ID] {
			result.Entities = append(result.Entities, e)
		}
	}
	for _, path := range paths {
		valid := len(path) > 0
		for _, id := range path {
			if !validClaims[id] {
				valid = false
			}
		}
		if valid {
			result.Paths = append(result.Paths, path)
		}
	}
	return result, nil
}

// Run reconciles the durable SQL outbox. It is started once by the container,
// stopped by ResourceCleaner, and never delays foreground catalog writes.
func (s *Service) Run(ctx context.Context) {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	for {
		if s.graph != nil {
			rows, err := s.Store.Pending(ctx, 20)
			if err == nil {
				for _, row := range rows {
					scope := Scope{TenantID: row.TenantID, KnowledgeBaseID: row.KnowledgeBaseID}
					snapshot, err := DecodeSnapshot([]byte(row.Payload), scope)
					if err != nil {
						continue
					}
					jobCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
					err = s.graph.Project(jobCtx, snapshot, row.Revision)
					if err == nil {
						_ = s.Store.MarkProjected(jobCtx, scope, row.Revision)
					}
					cancel()
				}
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		case <-s.wake:
		}
	}
}
