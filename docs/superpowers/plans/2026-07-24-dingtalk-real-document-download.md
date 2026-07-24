# DingTalk Real Document Download Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Route native DingTalk wiki documents through the backing dentry's binary download-info API, reject non-DOCX responses, preserve the blocks extractor as a dormant independent route, and retain actionable failure diagnostics.

**Architecture:** Split wiki document retrieval into an explicit binary route and an explicit blocks route. The binary route resolves the backing dentry, requests signed download information, downloads and validates DOCX bytes, and returns errors without falling back; the blocks route retains the current extraction behavior for future routing policies.

**Tech Stack:** Go, `net/http`, `archive/zip`, DingTalk OpenAPI, Go `httptest`, Logrus-style project logger.

---

## File Structure

- Modify `internal/datasource/connector/dingtalk/client.go`: add explicit binary and blocks routes, binary validation, and stage-specific logging/errors.
- Modify `internal/datasource/connector/dingtalk/connector_test.go`: add request-routing, DOCX validation, failure, and cursor regression tests.
- Modify `internal/datasource/connector/dingtalk/types.go`: only if a focused response DTO is needed for the download-info response already represented by `downloadInfoResponse`.

### Task 1: Prove Native Wiki Documents Use the Binary Download Route

**Files:**
- Modify: `internal/datasource/connector/dingtalk/connector_test.go`
- Modify: `internal/datasource/connector/dingtalk/client.go:434-457`

- [ ] **Step 1: Add a DOCX test helper**

Add a helper that creates a minimal valid ZIP/DOCX payload in memory:

```go
func testDOCXBytes(t *testing.T) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for name, content := range map[string]string{
		"[Content_Types].xml": `<Types xmlns="http://schemas.openxmlformats.org/package/2006/content-types"/>`,
		"word/document.xml":   `<w:document xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main"/>`,
	} {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := io.WriteString(w, content); err != nil {
			t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}
```

- [ ] **Step 2: Write the failing binary-route test**

Create `TestDownloadWikiDocContent_downloadsBackingDentryAsDOCX`. Its server must:

- return `spaceId` and `dentryId` from `/v2.0/doc/dentries/doc-key/queryDentryId`;
- return a signed URL from `POST /v1.0/storage/spaces/space-1/dentries/dentry-1/downloadInfos/query`;
- return `testDOCXBytes(t)` from that signed URL;
- fail the test if `/v1.0/doc/suites/documents/doc-key/blocks` or `/v1.0/doc/documents/dentry-1/content` is requested.

Assert:

```go
data, fileName, err := client.DownloadWikiDocContent(ctx, "doc-key", "测试文档.adoc", "adoc")
if err != nil {
	t.Fatal(err)
}
if fileName != "测试文档.docx" {
	t.Fatalf("fileName = %q", fileName)
}
if !bytes.Equal(data, wantDOCX) {
	t.Fatal("returned bytes differ from downloaded DOCX")
}
```

- [ ] **Step 3: Run the test and verify RED**

Run:

```bash
go test ./internal/datasource/connector/dingtalk -run TestDownloadWikiDocContent_downloadsBackingDentryAsDOCX -count=1
```

Expected: FAIL because current code requests `/v1.0/doc/documents/dentry-1/content` or falls through to blocks instead of requesting `downloadInfos/query`.

- [ ] **Step 4: Implement the minimal explicit binary route**

Refactor the production entry point into:

```go
func (c *Client) DownloadWikiDocContent(ctx context.Context, docKey, name, extension string) ([]byte, string, error) {
	return c.downloadWikiDocBinary(ctx, docKey, name)
}

func (c *Client) downloadWikiDocBinary(ctx context.Context, docKey, name string) ([]byte, string, error) {
	info, err := c.ResolveDentryIDByUUID(ctx, docKey)
	if err != nil {
		return nil, "", fmt.Errorf("解析钉钉文档下载标识失败：%w", err)
	}
	token, err := c.getAccessToken(ctx)
	if err != nil {
		return nil, "", fmt.Errorf("获取钉钉文档下载凭证失败：%w", err)
	}
	path := fmt.Sprintf("/v1.0/storage/spaces/%s/dentries/%s/downloadInfos/query",
		url.PathEscape(info.SpaceID), url.PathEscape(info.DentryID))
	data, err := c.downloadStorageFile(ctx, token, path, name, "docx")
	if err != nil {
		return nil, "", fmt.Errorf("下载钉钉完整文档失败：%w", err)
	}
	if err := validateDOCX(data); err != nil {
		return nil, "", fmt.Errorf("校验钉钉完整文档失败：%w", err)
	}
	return data, docxFileName(name), nil
}
```

Keep the old blocks request and extraction code by moving it without behavioral changes into:

```go
func (c *Client) downloadWikiDocViaBlocks(ctx context.Context, docKey, name string) ([]byte, string, error)
```

Do not call `downloadWikiDocViaBlocks` from `DownloadWikiDocContent`.

- [ ] **Step 5: Run the focused test and verify GREEN**

Run the command from Step 3.

Expected: PASS and the server observes exactly the resolve, download-info, and binary GET sequence.

- [ ] **Step 6: Commit**

```bash
git add internal/datasource/connector/dingtalk/client.go internal/datasource/connector/dingtalk/connector_test.go
git commit -m "feat: route DingTalk wiki docs through binary download"
```

### Task 2: Reject Incomplete or Disguised Downloads

**Files:**
- Modify: `internal/datasource/connector/dingtalk/connector_test.go`
- Modify: `internal/datasource/connector/dingtalk/client.go`

- [ ] **Step 1: Write failing table-driven validation tests**

Add `TestValidateDOCX_rejectsInvalidDownloads` with cases for:

```go
tests := []struct {
	name string
	data []byte
}{
	{"empty", nil},
	{"json error", []byte(`{"code":"Forbidden","message":"denied"}`)},
	{"html login page", []byte("<html><body>login</body></html>")},
	{"plain text", []byte("document text")},
	{"zip without word document", zipBytesContainingOnly(t, "other.txt", "x")},
}
```

Also add `TestValidateDOCX_acceptsDOCX` using `testDOCXBytes(t)`.

- [ ] **Step 2: Run tests and verify RED**

Run:

```bash
go test ./internal/datasource/connector/dingtalk -run 'TestValidateDOCX_' -count=1
```

Expected: build failure because `validateDOCX` does not exist yet, or assertion failure if only a superficial ZIP signature check exists.

- [ ] **Step 3: Implement structural DOCX validation**

Implement validation with `archive/zip`:

```go
func validateDOCX(data []byte) error {
	if len(data) == 0 {
		return fmt.Errorf("下载内容为空")
	}
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return fmt.Errorf("下载内容不是有效的 DOCX/ZIP：%w", err)
	}
	hasContentTypes := false
	hasDocument := false
	for _, f := range zr.File {
		switch f.Name {
		case "[Content_Types].xml":
			hasContentTypes = true
		case "word/document.xml":
			hasDocument = true
		}
	}
	if !hasContentTypes || !hasDocument {
		return fmt.Errorf("下载的 ZIP 缺少 DOCX 必需文件")
	}
	return nil
}
```

Add a focused `docxFileName` helper that strips `.adoc`, `.doc`, `.docx`, `.md`, or `.markdown` before appending `.docx`.

- [ ] **Step 4: Run validation and route tests**

Run:

```bash
go test ./internal/datasource/connector/dingtalk -run 'TestValidateDOCX_|TestDownloadWikiDocContent_downloadsBackingDentryAsDOCX' -count=1
```

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/datasource/connector/dingtalk/client.go internal/datasource/connector/dingtalk/connector_test.go
git commit -m "fix: validate downloaded DingTalk DOCX files"
```

### Task 3: Preserve Failure Stage, Logs, and No-Fallback Behavior

**Files:**
- Modify: `internal/datasource/connector/dingtalk/connector_test.go`
- Modify: `internal/datasource/connector/dingtalk/client.go`

- [ ] **Step 1: Write a failing resolve-error test**

Add `TestDownloadWikiDocContent_resolveFailureDoesNotFallbackToBlocks`. Return a DingTalk 403 body containing `code`, `message`, and `requestid` from the resolve endpoint. Count all blocks calls and assert:

```go
if blocksCalls != 0 {
	t.Fatalf("blocks calls = %d, want 0", blocksCalls)
}
for _, want := range []string{"解析钉钉文档下载标识失败", "Forbidden", "request"} {
	if !strings.Contains(err.Error(), want) {
		t.Fatalf("error %q missing %q", err, want)
	}
}
```

- [ ] **Step 2: Write a failing download-error test**

Add `TestDownloadWikiDocContent_downloadFailureDoesNotFallbackToBlocks`. Resolve successfully, return a 403 from `downloadInfos/query`, and assert zero blocks calls plus an error containing `下载钉钉完整文档失败` and the underlying DingTalk message.

- [ ] **Step 3: Run both tests and verify RED**

Run:

```bash
go test ./internal/datasource/connector/dingtalk -run 'TestDownloadWikiDocContent_(resolveFailure|downloadFailure)DoesNotFallbackToBlocks' -count=1
```

Expected: at least one assertion fails because the current API error does not preserve all diagnostic fields and stage logging is absent.

- [ ] **Step 4: Preserve DingTalk request diagnostics**

Extend `apiErrorBody` to decode both `requestid` and `requestId`, and ensure `doAPI` errors include:

```text
code=<code> message=<message> requestId=<request-id>（接口：<path>，HTTP <status>）
```

Add a helper that logs a warning at each failed binary stage with document name/key and the wrapped error. Use existing project logger APIs; never log the access token, signed URL query, authorization headers, app secret, or raw credentials.

The returned errors must remain stage-specific so `sync` stores them in `FetchedItem.Metadata["error"]` and the existing service copies them into `sync_logs.result.errors`.

- [ ] **Step 5: Run the focused tests and verify GREEN**

Run the command from Step 3.

Expected: PASS, with blocks call count remaining zero.

- [ ] **Step 6: Add and run a cursor regression test**

Add `TestFetchIncremental_downloadFailureDoesNotAdvanceRevision`. Simulate a changed native document whose download-info request fails, then assert:

```go
cursor := next.ConnectorCursor["doc_revisions"].(map[string]string)
if _, ok := cursor[externalID]; ok {
	t.Fatalf("failed document revision advanced")
}
```

Run:

```bash
go test ./internal/datasource/connector/dingtalk -run TestFetchIncremental_downloadFailureDoesNotAdvanceRevision -count=1
```

Expected: PASS with the existing cursor update placement after successful content retrieval.

- [ ] **Step 7: Commit**

```bash
git add internal/datasource/connector/dingtalk/client.go internal/datasource/connector/dingtalk/types.go internal/datasource/connector/dingtalk/connector_test.go
git commit -m "fix: retain DingTalk download failure diagnostics"
```

### Task 4: Prove the Blocks Route Remains Available

**Files:**
- Modify: `internal/datasource/connector/dingtalk/connector_test.go`

- [ ] **Step 1: Add a direct blocks-route regression test**

Add `TestDownloadWikiDocViaBlocks_remainsAvailable`. Stub only the blocks endpoint, invoke `client.downloadWikiDocViaBlocks`, and assert the existing paragraph/table Markdown output and `.md` filename.

- [ ] **Step 2: Run the test and verify GREEN**

Run:

```bash
go test ./internal/datasource/connector/dingtalk -run TestDownloadWikiDocViaBlocks_remainsAvailable -count=1
```

Expected: PASS. This is a preservation test: the route was extracted in Task 1 and is intentionally dormant, not deleted.

- [ ] **Step 3: Run the complete connector suite**

Run:

```bash
go test ./internal/datasource/connector/dingtalk -count=1
```

Expected: PASS.

- [ ] **Step 4: Run broader data-source tests**

Run:

```bash
go test ./internal/datasource/... -count=1
```

Expected: PASS.

- [ ] **Step 5: Check formatting and patch cleanliness**

Run:

```bash
gofmt -w internal/datasource/connector/dingtalk/client.go internal/datasource/connector/dingtalk/types.go internal/datasource/connector/dingtalk/connector_test.go
git diff --check
git status --short
```

Expected: no formatting or whitespace errors; the pre-existing `scripts/dev.sh` modification remains unstaged and untouched.

- [ ] **Step 6: Commit**

```bash
git add internal/datasource/connector/dingtalk/client.go internal/datasource/connector/dingtalk/types.go internal/datasource/connector/dingtalk/connector_test.go
git commit -m "test: preserve DingTalk blocks document route"
```

### Task 5: Final Verification

**Files:**
- Verify only; no planned production changes.

- [ ] **Step 1: Inspect the production call graph**

Confirm `DownloadWikiDocContent` calls `downloadWikiDocBinary`, and no call edge from the binary route reaches `downloadWikiDocViaBlocks` or the `/blocks` endpoint.

- [ ] **Step 2: Run final tests with fresh results**

Run:

```bash
go test ./internal/datasource/connector/dingtalk -count=1
go test ./internal/datasource/... -count=1
```

Expected: both commands exit 0.

- [ ] **Step 3: Review the final diff**

Run:

```bash
git diff HEAD~4 -- internal/datasource/connector/dingtalk
git status --short --branch
```

Expected: only scoped DingTalk implementation/tests plus the user's pre-existing unstaged `scripts/dev.sh` change.

- [ ] **Step 4: Report operational follow-up**

State that a live DingTalk sync is still required to confirm the tenant grants `Storage.File.Read` and that `downloadInfos/query` returns a real DOCX for native documents. If the live API rejects that operation, use the newly retained stage/error/request ID to determine the documented export API available to this tenant; do not silently reactivate blocks fallback.
