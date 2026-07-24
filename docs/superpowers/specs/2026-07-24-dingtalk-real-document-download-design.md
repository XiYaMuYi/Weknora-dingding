# DingTalk Real Document Download Design

## Goal

Make DingTalk native online-document synchronization succeed only when WeKnora
has downloaded the complete exported document file. Preserve the existing
blocks-based extraction implementation for later fallback or file-type routing,
but do not invoke it automatically during the initial download validation phase.

## Current Problem

`DownloadWikiDocContent` claims to download a complete DOCX, but routes through
`DownloadDocContent`, which treats the document as an online document and calls
the `/v1.0/doc/documents/{dentryId}/content` text endpoint. It therefore does not
download a DOCX file or preserve embedded media.

Errors from UUID resolution and the attempted download are discarded before the
code falls back to the blocks API. A partial blocks response can consequently
look like a successful synchronization, and the original failure cannot be
diagnosed from logs or sync records.

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

## Download Data Flow

For each supported native DingTalk document:

1. Resolve the wiki document UUID to its backing `spaceId` and `dentryId`.
2. Request a real export/download operation using DingTalk's supported API.
3. If the API is asynchronous, poll the operation using bounded retries and
   context-aware delays until it succeeds, fails, or times out.
4. Obtain the returned download URL and any required request headers.
5. Download the binary response.
6. Validate that the response is non-empty and is a DOCX/ZIP container rather
   than JSON, HTML, or plain text.
7. Return the bytes with a `.docx` filename so the normal WeKnora document
   parser processes the complete file.

The existing `/blocks` extraction remains isolated in a separately named
function and is not called by this flow.

## Routing

`DownloadWikiDocContent` remains the synchronization entry point for native
wiki documents. It will explicitly route to the real download implementation.

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
- create/request export;
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
- an export/download failure returns a stage-specific error;
- a failed real download does not invoke the blocks endpoint;
- JSON, HTML, plain text, and empty responses are rejected as invalid DOCX;
- asynchronous export polling respects success, failure, timeout, and context
  cancellation;
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
