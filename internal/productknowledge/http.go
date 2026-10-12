package productknowledge

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"

	"github.com/gin-gonic/gin"
)

// HTTPHandler is called only after global Auth and the KB read/write guards.
// Scope is supplied by the trusted middleware adapter, never by request JSON.
type HTTPHandler struct{ service *Service }

func NewHTTPHandler(service *Service) *HTTPHandler { return &HTTPHandler{service: service} }

func strictBody(c *gin.Context, target any) error {
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 2*1024*1024)
	decoder := json.NewDecoder(c.Request.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return ErrInvalidSnapshot
	}
	var trailing any
	if decoder.Decode(&trailing) != io.EOF {
		return ErrInvalidSnapshot
	}
	return nil
}
func replyError(c *gin.Context, err error) {
	status, code := http.StatusInternalServerError, "KNOWLEDGE_UNAVAILABLE"
	switch {
	case errors.Is(err, ErrInvalidSnapshot), errors.Is(err, ErrScope):
		status, code = 400, "INVALID_REQUEST"
	case errors.Is(err, ErrVersionConflict):
		status, code = 409, "VERSION_CONFLICT"
	case errors.Is(err, ErrAmbiguousProduct):
		status, code = 409, "AMBIGUOUS_ENTITY"
	case errors.Is(err, ErrSourceUnavailable), errors.Is(err, ErrCatalogNotFound):
		status, code = 404, "EVIDENCE_UNAVAILABLE"
	}
	c.JSON(status, gin.H{"success": false, "error": gin.H{"code": code, "message": code}})
}
func (h *HTTPHandler) Get(c *gin.Context, scope Scope) {
	snapshot, version, err := h.service.Store.Get(c.Request.Context(), scope)
	if errors.Is(err, ErrCatalogNotFound) {
		snapshot = Snapshot{Scope: scope, Entities: []Entity{}, Claims: []Claim{}}
		err = nil
	}
	if err != nil {
		replyError(c, err)
		return
	}
	c.JSON(200, gin.H{"success": true, "data": gin.H{"version": version, "snapshot": snapshot}})
}
func (h *HTTPHandler) Put(c *gin.Context, scope Scope) {
	var request struct {
		ExpectedVersion int64    `json:"expected_version"`
		Snapshot        Snapshot `json:"snapshot"`
	}
	if err := strictBody(c, &request); err != nil {
		replyError(c, err)
		return
	}
	if err := request.Snapshot.Validate(scope); err != nil {
		replyError(c, err)
		return
	}
	version, err := h.service.Put(c.Request.Context(), request.Snapshot, request.ExpectedVersion)
	if err != nil {
		replyError(c, err)
		return
	}
	c.JSON(200, gin.H{"success": true, "data": gin.H{"version": version, "projection_status": "pending"}})
}
func (h *HTTPHandler) Query(c *gin.Context, scope Scope) {
	var query Query
	if err := strictBody(c, &query); err != nil {
		replyError(c, err)
		return
	}
	result, err := h.service.Query(c.Request.Context(), scope, query)
	if err != nil {
		replyError(c, err)
		return
	}
	c.JSON(200, gin.H{"success": true, "data": result})
}
func (h *HTTPHandler) CreateEntity(c *gin.Context, scope Scope) {
	var request struct {
		ExpectedVersion int64    `json:"expected_version"`
		Kind            Kind     `json:"kind"`
		Name            string   `json:"name"`
		Aliases         []string `json:"aliases"`
	}
	if err := strictBody(c, &request); err != nil {
		replyError(c, err)
		return
	}
	snapshot, version, err := h.service.Store.Get(c.Request.Context(), scope)
	if errors.Is(err, ErrCatalogNotFound) {
		snapshot = Snapshot{Scope: scope, Entities: []Entity{}, Claims: []Claim{}}
		err = nil
	}
	if err != nil {
		replyError(c, err)
		return
	}
	if version != request.ExpectedVersion {
		replyError(c, ErrVersionConflict)
		return
	}
	entity := NewEntity(request.Kind, request.Name)
	entity.Aliases = request.Aliases
	snapshot.Entities = append(snapshot.Entities, entity)
	version, err = h.service.Put(c.Request.Context(), snapshot, version)
	if err != nil {
		replyError(c, err)
		return
	}
	c.JSON(201, gin.H{"success": true, "data": gin.H{"version": version, "entity": entity}})
}

func (h *HTTPHandler) IngestCandidates(c *gin.Context, scope Scope) {
	var request CandidateBatch
	if err := strictBody(c, &request); err != nil {
		replyError(c, err)
		return
	}
	result, err := h.service.IngestCandidates(c.Request.Context(), scope, request)
	if err != nil {
		replyError(c, err)
		return
	}
	c.JSON(http.StatusAccepted, gin.H{"success": true, "data": result})
}
