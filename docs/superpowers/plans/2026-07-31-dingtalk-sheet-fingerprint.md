# DingTalk Native Sheet Fingerprint Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Detect content changes for DingTalk `native_sheet` nodes whose metadata has no usable modification time or size.

**Architecture:** Keep normal revision short-circuiting for native documents and uploaded files. For native sheets with an unreliable metadata revision, fetch the bounded workbook ranges on every incremental probe, hash the canonical exported content, compare a dedicated cursor fingerprint, and only emit/ingest when the fingerprint changes. Empty sheets receive a fingerprint and remain a normal skip.

**Tech Stack:** Go, SHA-256, existing DingTalk workbook range API, sync cursor JSON, httptest.

---

### Task 1: Add fingerprint state and failing regression tests

**Files:**
- Modify: `internal/datasource/connector/dingtalk/connector.go`
- Modify: `internal/datasource/connector/dingtalk/connector_test.go`

- [ ] Add cursor field `SheetFingerprints map[string]string` and tests proving an unreliable native sheet is probed even when its old metadata revision is unchanged.
- [ ] Add tests for changed content (one fetched item), unchanged content (no fetched item), and empty-to-nonempty content (second run fetched).
- [ ] Run the focused tests and observe failure because the current code skips on `NodeID:0`.

### Task 2: Implement canonical sheet fingerprinting

**Files:**
- Modify: `internal/datasource/connector/dingtalk/connector.go`
- Modify: `internal/datasource/connector/dingtalk/client.go`

- [ ] Add `sheetRevisionUnreliable(ref)` requiring native sheet plus empty updated time and zero size.
- [ ] Make the incremental pre-check bypass only unreliable native sheets.
- [ ] Hash canonical returned content with SHA-256; store it in `SheetFingerprints`.
- [ ] On unchanged fingerprint, emit no content item; on changed fingerprint, preserve the existing ingest path.
- [ ] Preserve error semantics: workbook API errors do not advance fingerprint; empty workbook gets a fingerprint and `skip_reason`.

### Task 3: Verify and integrate

**Files:**
- Modify: `internal/datasource/connector/dingtalk/connector_test.go`

- [ ] Run `gofmt`, the focused regression tests, and the full DingTalk connector suite.
- [ ] Run `git diff --check`, commit, merge to `main`, rerun DingTalk tests, clean the worktree, and push `main` to the personal GitHub remote over HTTPS.
