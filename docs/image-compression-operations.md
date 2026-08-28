# Image compression operations

## OSS lifecycle safety net

Temporary originals used by first-pass OCR/VLM are written below
`tmp/processing/` (inside the configured `path_prefix` when present). The
	document worker deletes an object after the final failed retry. Successful
	objects remain available while asynchronous multimodal/post-processing work
	finishes and are removed by the lifecycle rule below.

- Scope: prefix `tmp/processing/` (or `<path_prefix>tmp/processing/` in the
  main bucket).
- Expiration: 3 days after object creation.
- Apply the same three-day expiration to `tmp/processing/` in the separate
  temporary bucket, when one is configured.
- Do not apply this rule to `exports/`; those are permanent knowledge/chat
  objects.

The application intentionally does not create or modify bucket lifecycle
rules at runtime because that requires broad bucket-management credentials.

## Historical migration

Open a document knowledge base, then go to **Knowledge Base Settings → Image
Storage Optimization**. Preview is database-only. Starting the task processes
only completed image knowledge larger than 1 MiB in that knowledge base.

The task uses the low-priority queue. It stores a new object, switches the
knowledge row, and then deletes the old object. Transient failures are held
until the current round finishes and retried after 30 seconds, 2 minutes, and
10 minutes. A knowledge-base lock prevents overlapping migration runs.

Existing chunks, OCR text, multimodal captions, embeddings, and RAG indexes
are not regenerated.
