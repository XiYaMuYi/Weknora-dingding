# DingTalk Native Document Export Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Replace the invalid storage-download route for DingTalk native `.adoc` documents with the official asynchronous DOCX export flow, while preserving the existing storage-download and blocks implementations for later routing/fallback work.

**Architecture:** `DownloadWikiDocContent` will submit a DOCX export for the wiki node's `dentryUuid`, poll the task until `SUCCESS`, download the temporary URL without forwarding the DingTalk access token, and validate the bytes as DOCX. Native document sync remains fail-closed: export failure produces an error item and does not call blocks or advance the revision cursor. Existing `ResolveDentryIDByUUID`, `downloadStorageFile`, and `downloadWikiDocViaBlocks` stay intact and independently callable.

**Tech Stack:** Go, DingTalk OpenAPI `doc_2.0`, `net/http`, `httptest`, Go ZIP validation, existing WeKnora connector/logging infrastructure.

**Official API contract:** Alibaba Cloud's generated DingTalk Go SDK defines `POST /v2.0/doc/me/export/submit` with `dentryUuid`, `operatorId`, `targetFormat`, and `GET /v2.0/doc/me/export/task/query` with `operatorId`, `taskId`. The query result contains `status` and `downloadUrl`; observed official client behavior uses `PROCESSING`, `SUCCESS`, and `FAILED`, compared case-insensitively.

---

### Task 1: Model the export API and make polling testable

**Files:**
- Modify: `internal/datasource/connector/dingtalk/types.go`
- Modify: `internal/datasource/connector/dingtalk/client.go`
- Test: `internal/datasource/connector/dingtalk/connector_test.go`

- [ ] **Step 1: Write failing tests for submit request and immediate success**

Add an `httptest` server test that asserts:

```go
POST /v2.0/doc/me/export/submit
body == {
    "dentryUuid": "doc-key",
    "operatorId": "operator",
    "targetFormat": "docx",
}
```

Return `{"taskId":"task-1","downloadUrl":"<server>/export.docx"}` and assert `DownloadWikiDocContent` downloads the DOCX without querying the task endpoint.

- [ ] **Step 2: Run the focused test and verify failure**

Run:

```bash
go test ./internal/datasource/connector/dingtalk -run 'TestDownloadWikiDocContent_exportImmediateSuccess' -count=1
```

Expected: FAIL because the current code calls `queryDentryId` and `downloadInfos/query`.

- [ ] **Step 3: Add exact DTOs and injectable polling timing**

Add to `types.go`:

```go
type submitExportJobRequest struct {
    DentryUUID  string `json:"dentryUuid"`
    OperatorID  string `json:"operatorId"`
    TargetFormat string `json:"targetFormat"`
}

type submitExportJobResponse struct {
    TaskID      string `json:"taskId"`
    DownloadURL string `json:"downloadUrl"`
}

type queryExportTaskResponse struct {
    Status      string `json:"status"`
    DownloadURL string `json:"downloadUrl"`
}
```

Add to `Client`:

```go
exportPollInterval time.Duration
exportMaxPolls     int
```

Set production defaults in `NewClient` to a two-second interval and 150 attempts (about five minutes). Tests override the interval with `time.Nanosecond` and a small attempt count.

- [ ] **Step 4: Implement submit and immediate-result handling**

Add focused helpers in `client.go`:

```go
func (c *Client) submitDocumentExport(ctx context.Context, dentryUUID string) (*submitExportJobResponse, error)
func (c *Client) exportWikiDocAsDOCX(ctx context.Context, dentryUUID, name string) ([]byte, string, error)
```

`submitDocumentExport` must call:

```go
c.doAPI(ctx, http.MethodPost, "/v2.0/doc/me/export/submit", nil, submitExportJobRequest{
    DentryUUID: dentryUUID,
    OperatorID: c.cfg.OperatorUnionID,
    TargetFormat: "docx",
}, &out)
```

Require either a non-empty `downloadUrl` or a non-empty `taskId`. Route `DownloadWikiDocContent` to `exportWikiDocAsDOCX`; do not call `ResolveDentryIDByUUID`.

- [ ] **Step 5: Run focused tests**

Run:

```bash
go test ./internal/datasource/connector/dingtalk -run 'TestDownloadWikiDocContent_exportImmediateSuccess|TestValidateDOCX' -count=1
```

Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add internal/datasource/connector/dingtalk/client.go internal/datasource/connector/dingtalk/types.go internal/datasource/connector/dingtalk/connector_test.go
git commit -m "feat: submit DingTalk native document exports"
```

### Task 2: Poll export tasks with terminal-state and cancellation handling

**Files:**
- Modify: `internal/datasource/connector/dingtalk/client.go`
- Test: `internal/datasource/connector/dingtalk/connector_test.go`

- [ ] **Step 1: Write table-driven failing polling tests**

Cover these sequences with zero-duration test polling:

```text
PROCESSING -> SUCCESS with downloadUrl
processing -> success (case-insensitive)
FAILED -> error containing taskId and status
SUCCESS without downloadUrl -> explicit error
PROCESSING until max polls -> timeout error
cancelled context while waiting -> context cancellation error
```

Assert every query is:

```http
GET /v2.0/doc/me/export/task/query?operatorId=operator&taskId=task-1
```

- [ ] **Step 2: Run tests and verify failure**

Run:

```bash
go test ./internal/datasource/connector/dingtalk -run 'TestPollDocumentExport' -count=1
```

Expected: FAIL because polling is not implemented.

- [ ] **Step 3: Implement context-aware polling**

Add:

```go
func (c *Client) queryDocumentExport(ctx context.Context, taskID string) (*queryExportTaskResponse, error)
func (c *Client) pollDocumentExport(ctx context.Context, taskID string) (string, error)
```

Normalize status with `strings.ToUpper(strings.TrimSpace(status))`. Return the URL for `SUCCESS`, continue only for `PROCESSING`, and fail for every other status (including empty/unknown) so protocol changes are visible. Wait with `time.NewTimer` plus `select` on `ctx.Done()`; stop/drain the timer correctly.

- [ ] **Step 4: Add stage-specific logs**

Log failures with document name, `dentryUuid`, `taskId` when available, and one of:

```text
submit_export
poll_export
download_export
validate_docx
```

Do not log access tokens or the signed download URL query string.

- [ ] **Step 5: Run focused tests**

Run:

```bash
go test ./internal/datasource/connector/dingtalk -run 'TestPollDocumentExport|TestDownloadWikiDocContent' -count=1
```

Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add internal/datasource/connector/dingtalk/client.go internal/datasource/connector/dingtalk/connector_test.go
git commit -m "feat: poll DingTalk document export tasks"
```

### Task 3: Download signed export results safely and preserve old routes

**Files:**
- Modify: `internal/datasource/connector/dingtalk/client.go`
- Test: `internal/datasource/connector/dingtalk/connector_test.go`

- [ ] **Step 1: Write failing download-security tests**

Use separate API and download test servers. Assert the export download request:

```text
uses GET
does not contain x-acs-dingtalk-access-token
rejects a host outside the existing allowlist policy
returns a useful HTTP status/body error
rejects HTML/JSON/non-DOCX payloads
```

Also assert an export failure makes zero calls to:

```text
/v2.0/doc/dentries/{uuid}/queryDentryId
/v1.0/storage/.../downloadInfos/query
/v1.0/doc/suites/documents/{uuid}/blocks
```

- [ ] **Step 2: Run tests and verify failure**

Run:

```bash
go test ./internal/datasource/connector/dingtalk -run 'TestDownloadExportFile|TestDownloadWikiDocContent_.*DoesNotFallback' -count=1
```

Expected: FAIL until the dedicated signed-URL downloader exists.

- [ ] **Step 3: Implement a dedicated export downloader**

Add:

```go
func (c *Client) downloadExportFile(ctx context.Context, rawURL string) ([]byte, error)
```

Validate the URL using `isAllowedDownloadHost`, issue a plain `GET`, and do not add the DingTalk token or headers from storage download info. Read the body, report non-2xx status with a truncated response body, then call `validateDOCX` before returning from `exportWikiDocAsDOCX`.

Leave these existing functions unchanged and callable:

```go
ResolveDentryIDByUUID
downloadStorageFile
downloadWikiDocViaBlocks
downloadViaExportFallback
```

- [ ] **Step 4: Verify no fallback and cursor semantics**

Update the existing incremental failure test to make submit or polling fail. Assert:

```text
the fetched item contains the export error
the previous revision is not replaced with the failing document revision
blocks call count is zero
```

- [ ] **Step 5: Run connector tests**

Run:

```bash
go test ./internal/datasource/connector/dingtalk -count=1
```

Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add internal/datasource/connector/dingtalk/client.go internal/datasource/connector/dingtalk/connector_test.go
git commit -m "fix: download DingTalk export results safely"
```

### Task 4: Update parser version and perform regression verification

**Files:**
- Modify: `internal/datasource/connector/dingtalk/connector.go`
- Test: `internal/datasource/connector/dingtalk/connector_test.go`

- [ ] **Step 1: Write the parser-version regression expectation**

Update tests to expect a new parser version and verify an unchanged document from version `3` is reprocessed once through the export route.

- [ ] **Step 2: Bump the parser version**

Change:

```go
const dingtalkContentParserVersion = "4"
```

This forces one re-sync so documents previously produced by the invalid storage route are replaced by exported DOCX content.

- [ ] **Step 3: Run formatting and focused tests**

Run:

```bash
gofmt -w internal/datasource/connector/dingtalk/client.go internal/datasource/connector/dingtalk/types.go internal/datasource/connector/dingtalk/connector.go internal/datasource/connector/dingtalk/connector_test.go
go test ./internal/datasource/connector/dingtalk -count=1
```

Expected: PASS.

- [ ] **Step 4: Run broader connector regression tests**

Run:

```bash
go test ./internal/datasource/connector/... -count=1
```

Expected: DingTalk tests pass. If the pre-existing Yuque token-logging test still fails identically on unchanged baseline, record it separately and do not alter Yuque code.

- [ ] **Step 5: Inspect the final diff**

Run:

```bash
git diff --check
git status --short
git diff -- internal/datasource/connector/dingtalk
```

Expected: no whitespace errors; only intended DingTalk files plus the user's pre-existing `scripts/dev.sh` modification remain.

- [ ] **Step 6: Commit**

```bash
git add internal/datasource/connector/dingtalk/connector.go internal/datasource/connector/dingtalk/connector_test.go
git commit -m "chore: reprocess DingTalk documents with export parser"
```

