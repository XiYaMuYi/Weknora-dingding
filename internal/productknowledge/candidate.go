package productknowledge

import (
	"context"
	"errors"
	"regexp"
	"sort"
	"strings"

	"github.com/google/uuid"
)

var candidateRefPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)

// CandidateBatch is the narrow asynchronous-extraction boundary. It deliberately
// has no status field: every extracted assertion enters review as pending.
type CandidateBatch struct {
	ExpectedVersion int64             `json:"expected_version"`
	Entities        []CandidateEntity `json:"entities"`
	Claims          []CandidateClaim  `json:"claims"`
}

type CandidateEntity struct {
	Ref     string   `json:"ref"`
	Kind    Kind     `json:"kind"`
	Name    string   `json:"name"`
	Aliases []string `json:"aliases,omitempty"`
}

type CandidateClaim struct {
	SubjectRef string     `json:"subject_ref"`
	ObjectRef  string     `json:"object_ref"`
	Relation   Relation   `json:"relation"`
	Evidence   []Evidence `json:"evidence"`
}

type CandidateResult struct {
	Version  int64    `json:"version"`
	Entities []Entity `json:"entities"`
	ClaimIDs []string `json:"claim_ids"`
}

func (b CandidateBatch) Validate() error {
	if b.ExpectedVersion < 0 || len(b.Entities) == 0 || len(b.Entities) > 200 || len(b.Claims) > 1000 {
		return ErrInvalidSnapshot
	}
	refs := make(map[string]Kind, len(b.Entities))
	for _, entity := range b.Entities {
		if !candidateRefPattern.MatchString(entity.Ref) || !validKind(entity.Kind) || !validText(entity.Name) || len(entity.Aliases) > 20 {
			return ErrInvalidSnapshot
		}
		if _, duplicate := refs[entity.Ref]; duplicate {
			return ErrInvalidSnapshot
		}
		for _, alias := range entity.Aliases {
			if !validText(alias) {
				return ErrInvalidSnapshot
			}
		}
		refs[entity.Ref] = entity.Kind
	}
	for _, claim := range b.Claims {
		if !validRelation(claim.Relation, refs[claim.SubjectRef], refs[claim.ObjectRef]) || len(claim.Evidence) == 0 || len(claim.Evidence) > 20 {
			return ErrInvalidSnapshot
		}
		for _, evidence := range claim.Evidence {
			if !validID(evidence.KnowledgeID) || !validID(evidence.ChunkID) || evidence.Revision < 1 || evidence.SourceFingerprint != "" {
				return ErrInvalidSnapshot
			}
		}
	}
	return nil
}

func normalizedEntityKey(kind Kind, name string) string {
	return string(kind) + "\x00" + strings.ToLower(strings.TrimSpace(name))
}

func evidenceKey(values []Evidence) string {
	parts := make([]string, 0, len(values))
	for _, value := range values {
		parts = append(parts, value.KnowledgeID+"/"+value.ChunkID)
	}
	sort.Strings(parts)
	return strings.Join(parts, "|")
}

func claimKey(subjectID, objectID string, relation Relation, evidence []Evidence) string {
	return subjectID + "\x00" + string(relation) + "\x00" + objectID + "\x00" + evidenceKey(evidence)
}

// IngestCandidates merges model/extractor output by exact typed identity and
// source. It never confirms facts; the existing owner/admin catalog PUT is the
// explicit review boundary.
func (s *Service) IngestCandidates(ctx context.Context, scope Scope, batch CandidateBatch) (CandidateResult, error) {
	result := CandidateResult{Entities: []Entity{}, ClaimIDs: []string{}}
	if err := batch.Validate(); err != nil {
		return result, err
	}
	snapshot, version, err := s.Store.Get(ctx, scope)
	if errors.Is(err, ErrCatalogNotFound) {
		snapshot, version, err = Snapshot{Scope: scope, Entities: []Entity{}, Claims: []Claim{}}, 0, nil
	}
	if err != nil {
		return result, err
	}
	if version != batch.ExpectedVersion {
		return result, ErrVersionConflict
	}
	byKey := map[string]Entity{}
	ambiguous := map[string]bool{}
	for _, entity := range snapshot.Entities {
		key := normalizedEntityKey(entity.Kind, entity.Name)
		if _, exists := byKey[key]; exists {
			ambiguous[key] = true
		}
		byKey[key] = entity
	}
	byRef := make(map[string]Entity, len(batch.Entities))
	for _, candidate := range batch.Entities {
		key := normalizedEntityKey(candidate.Kind, candidate.Name)
		if ambiguous[key] {
			return result, ErrAmbiguousProduct
		}
		entity, exists := byKey[key]
		if !exists {
			entity = NewEntity(candidate.Kind, candidate.Name)
			entity.Aliases = append([]string(nil), candidate.Aliases...)
			snapshot.Entities = append(snapshot.Entities, entity)
			byKey[key] = entity
		}
		byRef[candidate.Ref] = entity
		result.Entities = append(result.Entities, entity)
	}
	existingClaims := map[string]string{}
	for _, claim := range snapshot.Claims {
		existingClaims[claimKey(claim.SubjectID, claim.ObjectID, claim.Relation, claim.Evidence)] = claim.ID
	}
	for _, candidate := range batch.Claims {
		subject, object := byRef[candidate.SubjectRef], byRef[candidate.ObjectRef]
		key := claimKey(subject.ID, object.ID, candidate.Relation, candidate.Evidence)
		claimID, exists := existingClaims[key]
		if !exists {
			claimID = uuid.NewString()
			snapshot.Claims = append(snapshot.Claims, Claim{
				ID: claimID, SubjectID: subject.ID, ObjectID: object.ID,
				Relation: candidate.Relation, Status: Pending,
				Evidence: append([]Evidence(nil), candidate.Evidence...),
			})
			existingClaims[key] = claimID
		}
		result.ClaimIDs = append(result.ClaimIDs, claimID)
	}
	result.Version, err = s.Put(ctx, snapshot, version)
	return result, err
}
