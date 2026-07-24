# DingTalk Real Document Download Design

## Goal

Make DingTalk native online-document synchronization succeed only when WeKnora
has downloaded the complete exported document file. Preserve the existing
blocks-based extraction implementation for later fallback or file-type routing,
but do not invoke it automatically during the initial download validation phase.

## Current Problem

`DownloadWikiDocContent` claims to download a complete DOCX, but routes through
the backing dentry's storage `downloadInfos/query` API. Live DingTalk responses
show that this API rejects native `.adoc` documents with HTTP 400,
`operationNotSupported`, and `unsupported file type`. Storage download-info is
for directly downloadable files; native online documents must first be exported
to an offline format.

The connector now preserves the failure and does not silently fall back to
blocks, but it still needs the official native-document export workflow.

## Selected Approach: Explicit Dual Routes

The connector will expose two independent internal routes:

1. A real exported-file download route.
2. The existing blocks extraction route.

The production router for native DingTalk documents will initially select only
the real download route. A download failure will fail that fetched item. The
blocks route remains implemented and tested, but is not an automatic fallback
in this phase.

This structure allows a later change to select routes by document kind or
configuration without restoring hidden fallback behavior.

## Export and Download Data Flow

For each supported native DingTalk document:

1. Resolve the wiki document UUID to its backing `spaceId` and `dentryId`.
2. Create an official DingTalk export task targeting DOCX.
3. Poll the official export-task status API using bounded retries and
   context-aware delays until it succeeds, fails, or times out.
4. Obtain the returned download URL and any required request headers.
5. Download the binary response.
6. Validate that the response is non-empty and is a DOCX/ZIP container rather
   than JSON, HTML, or plain text.
7. Return the bytes with a `.docx` filename so the normal WeKnora document
   parser processes the complete file.

The exact API paths, methods, request bodies, response fields, status values, and
required permission scopes must be taken from DingTalk's official OpenAPI
documentation or API Explorer for the target enterprise application. They must
not be inferred from storage APIs or undocumented web-client traffic.

The existing `/blocks` extraction remains isolated in a separately named
function and is not called by the export flow.

## Routing

`DownloadWikiDocContent` remains the synchronization entry point for native
wiki documents. It will explicitly route to the official export implementation.

Ordinary uploaded files remain eligible for the storage
`downloadInfos/query` route. Native `.adoc` documents must never use that route.

The old blocks implementation will be retained as an independent callable
method, with no production fallback edge from the download route during this
phase. A future router may support:

- download only;
- blocks only for document kinds that cannot be exported;
- download followed by an explicitly configured blocks fallback.

Those future routing policies are outside this change.

## Error Handling and Observability

Every failure must preserve its stage and underlying cause:

- resolve backing dentry;
- create export task;
- poll export status;
- obtain download information;
- fetch binary file;
- validate downloaded file.

Logs will include the document key/name, stage, DingTalk request path, HTTP
status, DingTalk error code, request ID, and message when available. Secrets,
access tokens, signed query strings, and authorization headers must not be
logged.

The returned item error will contain a concise stage-specific message so it is
stored in the existing sync log and data-source result. Detailed diagnostics
remain in backend logs.

Download failure will not call the blocks route and will not advance that
document's revision cursor. A later sync can retry it.

## Validation

Tests will be written before implementation and will cover:

- a successful real DOCX download returns binary DOCX bytes and a `.docx`
  filename;
- a create-task, task-failed, task-timeout, download-address, or binary-download
  failure returns a stage-specific error;
- a failed real download does not invoke the blocks endpoint;
- JSON, HTML, plain text, and empty responses are rejected as invalid DOCX;
- asynchronous export polling respects success, failure, timeout, and context
  cancellation;
- native `.adoc` documents never call storage `downloadInfos/query`;
- uploaded files can continue to use storage `downloadInfos/query`;
- the retained blocks route still passes its existing extraction tests and can
  be invoked independently;
- failed documents do not advance the incremental cursor.

The DingTalk connector package test suite and relevant data-source sync tests
must pass after the change.

## Non-Goals

- Removing the blocks extraction implementation.
- Enabling automatic blocks fallback.
- Adding a frontend or data-source configuration switch.
- Supporting every DingTalk online file type in this change.
- Changing uploaded-attachment handling.
