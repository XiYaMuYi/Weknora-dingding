package productknowledge

import (
	"context"
	"errors"
	"fmt"
	"strconv"

	"github.com/neo4j/neo4j-go-driver/v6/neo4j"
)

var ErrGraphUnavailable = errors.New("knowledge graph unavailable")

type Neo4jGraph struct{ driver neo4j.Driver }

func NewNeo4jGraph(driver neo4j.Driver) *Neo4jGraph { return &Neo4jGraph{driver: driver} }
func graphScope(scope Scope) string {
	return strconv.FormatUint(scope.TenantID, 10) + ":" + scope.KnowledgeBaseID
}

// Project uses separate labels and relationship types. Existing ENTITY and
// Wiki indexes are neither interpreted differently nor deleted.
func (g *Neo4jGraph) Project(ctx context.Context, snapshot Snapshot, version int64) error {
	if g == nil || g.driver == nil {
		return ErrGraphUnavailable
	}
	if version < 1 {
		return ErrVersionConflict
	}
	if err := snapshot.Validate(snapshot.Scope); err != nil {
		return err
	}
	session := g.driver.NewSession(ctx, neo4j.SessionConfig{AccessMode: neo4j.AccessModeWrite})
	defer session.Close(ctx)
	entities := []map[string]any{}
	claims := []map[string]any{}
	for _, e := range snapshot.Entities {
		entities = append(entities, map[string]any{"id": e.ID, "kind": string(e.Kind), "name": e.Name, "aliases": e.Aliases})
	}
	for _, c := range snapshot.Claims {
		if c.Status == Confirmed {
			claims = append(claims, map[string]any{"id": c.ID, "subject": c.SubjectID, "object": c.ObjectID, "relation": string(c.Relation)})
		}
	}
	_, err := session.ExecuteWrite(ctx, func(tx neo4j.ManagedTransaction) (any, error) {
		params := map[string]any{"scope": graphScope(snapshot.Scope), "version": version, "entities": entities, "claims": claims}
		// Acquiring a write lock before reading revision prevents an older
		// worker from replacing a newer projection during concurrent retries.
		r, err := tx.Run(ctx, `MERGE (c:PKCatalog {scope:$scope}) SET c.lock = coalesce(c.lock,0)+1 RETURN coalesce(c.version,0) AS version`, params)
		if err != nil {
			return nil, err
		}
		record, err := r.Single(ctx)
		if err != nil {
			return nil, err
		}
		current, _ := record.Get("version")
		if v, ok := current.(int64); ok && v > version {
			return nil, nil
		}
		for _, query := range []string{
			`MATCH (e:PKEntity {scope:$scope}) DETACH DELETE e`,
			`UNWIND $entities AS row CREATE (e:PKEntity {scope:$scope,version:$version,id:row.id,kind:row.kind,name:row.name,aliases:row.aliases})`,
			`UNWIND $claims AS row MATCH (a:PKEntity {scope:$scope,id:row.subject}), (b:PKEntity {scope:$scope,id:row.object}) CREATE (a)-[:PK_FACT {scope:$scope,version:$version,id:row.id,relation:row.relation,status:'confirmed'}]->(b)`,
			`MATCH (c:PKCatalog {scope:$scope}) SET c.version=$version`,
		} {
			result, err := tx.Run(ctx, query, params)
			if err != nil {
				return nil, err
			}
			if _, err := result.Consume(ctx); err != nil {
				return nil, err
			}
		}
		return nil, nil
	})
	return err
}

func (g *Neo4jGraph) Paths(ctx context.Context, scope Scope, version int64, seeds []string, hops, limit int) ([][]string, error) {
	if g == nil || g.driver == nil {
		return nil, ErrGraphUnavailable
	}
	if err := (Query{EntityIDs: seeds, MaxHops: hops, Limit: limit}).Validate(); err != nil {
		return nil, err
	}
	if hops < 1 || version < 1 {
		return nil, ErrInvalidSnapshot
	}
	relations := []string{string(ContainsIngredient), string(DeclaresBenefit), string(ExplicitlySuitableFor), string(UsedIn), string(HasPrecaution), string(IngredientProperty)}
	// Only a validated integer is interpolated; Cypher and labels cannot come
	// from callers. Querying paths returns assertions, not inferred new facts.
	query := fmt.Sprintf(`MATCH (c:PKCatalog {scope:$scope,version:$version})
MATCH (s:PKEntity {scope:$scope,version:$version}) WHERE s.id IN $seeds
MATCH p=(s)-[:PK_FACT*1..%d]-(t:PKEntity)
WHERE all(n IN nodes(p) WHERE n.scope=$scope AND n.version=$version)
AND all(r IN relationships(p) WHERE r.scope=$scope AND r.version=$version AND r.status='confirmed' AND r.relation IN $relations)
RETURN DISTINCT [r IN relationships(p) | r.id] AS claims LIMIT $limit`, hops)
	session := g.driver.NewSession(ctx, neo4j.SessionConfig{AccessMode: neo4j.AccessModeRead})
	defer session.Close(ctx)
	value, err := session.ExecuteRead(ctx, func(tx neo4j.ManagedTransaction) (any, error) {
		r, err := tx.Run(ctx, query, map[string]any{"scope": graphScope(scope), "version": version, "seeds": seeds, "relations": relations, "limit": limit})
		if err != nil {
			return nil, err
		}
		paths := [][]string{}
		for r.Next(ctx) {
			value, _ := r.Record().Get("claims")
			values, ok := value.([]any)
			if !ok {
				return nil, ErrGraphUnavailable
			}
			path := []string{}
			for _, value := range values {
				id, ok := value.(string)
				if !ok {
					return nil, ErrGraphUnavailable
				}
				path = append(path, id)
			}
			paths = append(paths, path)
		}
		return paths, r.Err()
	})
	if err != nil {
		return nil, err
	}
	return value.([][]string), nil
}
