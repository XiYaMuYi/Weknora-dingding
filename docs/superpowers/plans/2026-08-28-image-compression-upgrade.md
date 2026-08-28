# Image Compression Upgrade Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Compress new and existing knowledge-base images to at most 1 MiB while using temporary originals for first-pass RAG.

**Architecture:** A pure-Go compression component produces a typed result and permanent/retryable failures. Upload and batch services call it before storing a new object; document processing receives a temporary original path and owns cleanup. Historical migration uses the existing Asynq/Redis progress patterns and performs create-switch-delete replacement.

**Tech Stack:** Go 1.26, `github.com/gen2brain/webp`, `github.com/disintegration/imaging`, `golang.org/x/image`, Gin, GORM, Asynq, Redis, Vue 3/TypeScript.

**Spec:** `docs/superpowers/specs/2026-08-28-image-compression-upgrade-design.md`

## Global Constraints

- Permanent raster output must be at most 1 MiB.
- Maximum decoded pixel count is 40 megapixels.
- Originals used for first-pass RAG are actively deleted and have a three-day OSS lifecycle fallback.
- Only completed knowledge-base images are eligible for historical migration.
- Existing unrelated worktree changes must not be modified or committed.

---

### Task 1: Adaptive compression core

**Files:**
- Create: `internal/imagecompression/compressor.go`
- Create: `internal/imagecompression/compressor_test.go`
- Modify: `go.mod`, `go.sum`

**Interfaces:**
- Produces: `Compress(data []byte, fileName string, cfg Config) (Result, error)` and typed permanent/retryable errors.

- [ ] Write table-driven failing tests for JPEG, transparent PNG, GIF/WebP, BMP/TIFF, small files, 40MP rejection, corrupt input, and 1 MiB output.
- [ ] Run `go test ./internal/imagecompression -count=1` and confirm RED.
- [ ] Implement format sniffing, decode limits, EXIF orientation, adaptive resize/encode, and result metadata.
- [ ] Run focused tests and confirm GREEN.

### Task 2: Upload storage and temporary-original lifecycle

**Files:**
- Modify: `internal/application/service/knowledge_create.go`
- Modify: `internal/types/task.go`
- Modify: `internal/application/service/knowledge_process.go`
- Modify: `internal/application/service/file/oss.go`
- Test: adjacent service/file tests.

**Interfaces:**
- Consumes: `imagecompression.Compress`.
- Produces: `DocumentProcessPayload.DeleteSourceAfterProcess` and temporary-object cleanup behavior.

- [ ] Write failing service tests proving permanent and processing paths differ, original metadata drives RAG, cleanup waits across retryable attempts, and final cleanup occurs.
- [ ] Integrate compression and rollback-safe dual storage in `CreateKnowledgeFromFile`.
- [ ] Add OSS `tmp/rag-original/` prefix fallback for lifecycle rules.
- [ ] Add final-attempt/success cleanup in `ProcessDocument`.
- [ ] Run focused application/file tests.

### Task 3: Chat attachment compression

**Files:**
- Modify: `internal/handler/session/attachment_processor.go`
- Test: `internal/handler/session/attachment_processor_test.go`

**Interfaces:**
- Consumes: `imagecompression.Compress`.

- [ ] Write a failing test proving stored image bytes, name, MIME extension, and reported size use the compressed result.
- [ ] Compress only image attachments before `SaveBytes`; preserve non-image behavior.
- [ ] Run session handler tests.

### Task 4: Compression metadata migration

**Files:**
- Modify: `internal/types/knowledge.go`
- Create: `migrations/*_add_knowledge_compression_info.up.sql`
- Create: matching down migration.
- Test: repository schema tests affected by explicit SQLite DDL.

**Interfaces:**
- Produces: `CompressionInfo` JSON metadata persisted on `Knowledge`.

- [ ] Add failing persistence/round-trip tests.
- [ ] Add model field and PostgreSQL migration.
- [ ] Update explicit SQLite test schemas.
- [ ] Run repository tests.

### Task 5: Historical migration worker and progress

**Files:**
- Create: `internal/application/service/knowledge_image_compression.go`
- Modify: `internal/types/task.go`
- Modify: `internal/types/interfaces/knowledge.go`
- Modify: `internal/router/task.go`, `internal/router/sync_task.go`
- Test: `internal/application/service/knowledge_image_compression_test.go`

**Interfaces:**
- Produces: dry-run summary, enqueue API, Asynq processor, Redis progress model.

- [ ] Write failing tests for candidate filtering, dry-run, create-switch-delete, rollback, idempotency, and retry classification.
- [ ] Implement candidate query and low-priority round processor.
- [ ] Register task handler and progress storage.
- [ ] Run focused service/router tests.

### Task 6: HTTP API and frontend controls

**Files:**
- Modify: `internal/handler/knowledge.go`
- Modify: route registration near knowledge routes.
- Modify: `frontend/src/api/knowledge-base/index.ts`
- Modify: the existing knowledge-base settings component.
- Test: handler tests and frontend type/build tests.

**Interfaces:**
- Produces: dry-run/start/progress endpoints and user-visible progress/results.

- [ ] Add failing handler/API contract tests.
- [ ] Implement authorization-checked endpoints.
- [ ] Add dry-run confirmation and asynchronous progress UI.
- [ ] Run focused backend and frontend checks.

### Task 7: Full verification and safety review

- [ ] Run focused tests for every changed backend package.
- [ ] Run `go test ./internal/... -count=1`.
- [ ] Run the repository frontend typecheck/build commands discovered from `package.json`.
- [ ] Inspect `git diff --check`, `git diff --stat`, and the complete diff for unrelated changes/secrets.
- [ ] Verify every success criterion from the design document with fresh evidence.
