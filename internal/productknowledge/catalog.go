// Package productknowledge defines shared product-domain knowledge contracts.
// It owns neither user profiles nor recommendation decisions. Validation here
// is structural; services must independently verify actual source permissions.
package productknowledge

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"unicode/utf8"

	"github.com/google/uuid"
)

type Kind string

const (
	Product    Kind = "product"
	Ingredient Kind = "ingredient"
	Benefit    Kind = "benefit"
	SkinType   Kind = "skin_type"
	Scenario   Kind = "scenario"
	Precaution Kind = "precaution"
)

type Relation string

const (
	ContainsIngredient    Relation = "contains_ingredient"
	DeclaresBenefit       Relation = "declares_benefit"
	ExplicitlySuitableFor Relation = "explicitly_suitable_for"
	UsedIn                Relation = "used_in"
	HasPrecaution         Relation = "has_precaution"
	IngredientProperty    Relation = "ingredient_property"
)

type Status string

const (
	Pending   Status = "pending"
	Confirmed Status = "confirmed"
	Rejected  Status = "rejected"
)

var (
	ErrInvalidSnapshot  = errors.New("invalid knowledge snapshot")
	ErrScope            = errors.New("knowledge scope mismatch")
	ErrProductNotFound  = errors.New("product not found")
	ErrAmbiguousProduct = errors.New("ambiguous product reference")
)

type Scope struct {
	TenantID        uint64 `json:"tenant_id"`
	KnowledgeBaseID string `json:"knowledge_base_id"`
}

type Entity struct {
	ID      string   `json:"id"`
	Kind    Kind     `json:"kind"`
	Name    string   `json:"name"`
	Aliases []string `json:"aliases,omitempty"`
}

// NewEntity generates identity independently of names, source documents and goods IDs.
func NewEntity(kind Kind, name string) Entity {
	return Entity{ID: uuid.NewString(), Kind: kind, Name: strings.TrimSpace(name)}
}

type Evidence struct {
	KnowledgeID string `json:"knowledge_id"`
	ChunkID     string `json:"chunk_id"`
	Revision    int    `json:"revision"`
	// SourceFingerprint pins actual source content; Revision is a claim revision,
	// not a universal WeKnora document version. Runtime use requires this field.
	SourceFingerprint string `json:"source_fingerprint,omitempty"`
}

// Claims are explicit source-backed assertions, never automatically transitive.
type Claim struct {
	ID        string     `json:"id"`
	SubjectID string     `json:"subject_id"`
	ObjectID  string     `json:"object_id"`
	Relation  Relation   `json:"relation"`
	Status    Status     `json:"status"`
	Evidence  []Evidence `json:"evidence"`
}

type Snapshot struct {
	Scope    Scope    `json:"scope"`
	Entities []Entity `json:"entities"`
	Claims   []Claim  `json:"claims"`
}

// DecodeSnapshot is the external boundary; unknown fields and concatenated JSON
// are rejected instead of silently accepting agent/commerce data as knowledge.
func DecodeSnapshot(data []byte, scope Scope) (Snapshot, error) {
	if len(data) > 2*1024*1024 {
		return Snapshot{}, ErrInvalidSnapshot
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var snapshot Snapshot
	if decoder.Decode(&snapshot) != nil {
		return Snapshot{}, ErrInvalidSnapshot
	}
	var trailing any
	if decoder.Decode(&trailing) != io.EOF {
		return Snapshot{}, ErrInvalidSnapshot
	}
	if err := snapshot.Validate(scope); err != nil {
		return Snapshot{}, err
	}
	return snapshot, nil
}

func validID(value string) bool {
	id, err := uuid.Parse(value)
	return err == nil && id != uuid.Nil && value == id.String()
}

func validText(value string) bool {
	return strings.TrimSpace(value) != "" && utf8.ValidString(value) && utf8.RuneCountInString(value) <= 240
}

func validKind(kind Kind) bool {
	switch kind {
	case Product, Ingredient, Benefit, SkinType, Scenario, Precaution:
		return true
	}
	return false
}

func validRelation(relation Relation, subject, object Kind) bool {
	switch relation {
	case ContainsIngredient:
		return subject == Product && object == Ingredient
	case DeclaresBenefit:
		return subject == Product && object == Benefit
	case ExplicitlySuitableFor:
		return subject == Product && object == SkinType
	case UsedIn:
		return subject == Product && object == Scenario
	case HasPrecaution:
		return subject == Product && object == Precaution
	case IngredientProperty:
		return subject == Ingredient && object == Benefit
	}
	return false
}

func (s Snapshot) Validate(scope Scope) error {
	if scope.TenantID == 0 || !validID(scope.KnowledgeBaseID) || s.Scope != scope {
		return ErrScope
	}
	if len(s.Entities) > 1000 || len(s.Claims) > 10000 {
		return ErrInvalidSnapshot
	}
	entities := make(map[string]Kind, len(s.Entities))
	for _, entity := range s.Entities {
		if !validID(entity.ID) || !validKind(entity.Kind) || !validText(entity.Name) || len(entity.Aliases) > 20 {
			return ErrInvalidSnapshot
		}
		if _, duplicate := entities[entity.ID]; duplicate {
			return ErrInvalidSnapshot
		}
		aliases := make(map[string]bool)
		for _, alias := range entity.Aliases {
			key := strings.ToLower(strings.TrimSpace(alias))
			if !validText(alias) || aliases[key] {
				return ErrInvalidSnapshot
			}
			aliases[key] = true
		}
		entities[entity.ID] = entity.Kind
	}
	claims := make(map[string]bool, len(s.Claims))
	for _, claim := range s.Claims {
		if !validID(claim.ID) || claims[claim.ID] || !validRelation(claim.Relation, entities[claim.SubjectID], entities[claim.ObjectID]) {
			return ErrInvalidSnapshot
		}
		if claim.Status != Pending && claim.Status != Confirmed && claim.Status != Rejected {
			return ErrInvalidSnapshot
		}
		if len(claim.Evidence) == 0 || len(claim.Evidence) > 20 {
			return ErrInvalidSnapshot
		}
		for _, evidence := range claim.Evidence {
			if !validID(evidence.KnowledgeID) || !validID(evidence.ChunkID) || evidence.Revision < 1 {
				return ErrInvalidSnapshot
			}
		}
		claims[claim.ID] = true
	}
	return nil
}

// ResolveProduct resolves only explicit names/aliases; similarity is not identity.
func (s Snapshot) ResolveProduct(scope Scope, reference string) (Entity, error) {
	if err := s.Validate(scope); err != nil {
		return Entity{}, err
	}
	key := strings.ToLower(strings.TrimSpace(reference))
	if !validText(reference) {
		return Entity{}, ErrProductNotFound
	}
	var matched Entity
	for _, entity := range s.Entities {
		if entity.Kind != Product {
			continue
		}
		names := append([]string{entity.Name}, entity.Aliases...)
		for _, name := range names {
			if strings.ToLower(strings.TrimSpace(name)) != key {
				continue
			}
			if matched.ID != "" {
				return Entity{}, ErrAmbiguousProduct
			}
			matched = entity
			break
		}
	}
	if matched.ID == "" {
		return Entity{}, ErrProductNotFound
	}
	return matched, nil
}
