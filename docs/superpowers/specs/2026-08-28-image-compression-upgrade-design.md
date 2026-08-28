# Image Compression Upgrade Design

## Objective

Reduce permanent object-storage traffic and storage used by knowledge-base images while preserving first-pass OCR/VLM quality. New knowledge images, including DingTalk-synchronized files, use the original image for initial RAG processing and retain only a compressed permanent image. Existing completed knowledge images can be migrated safely in the background.

## Scope

- Knowledge-base image uploads through `CreateKnowledgeFromFile`, including DingTalk datasource ingestion.
- Chat image attachments stored through `AttachmentProcessor`.
- Existing image knowledge records in `knowledges` whose parse status is completed.
- JPEG, PNG, GIF, WebP, BMP, TIFF, and size-limited SVG inputs.
- OSS and every existing `FileService` implementation through the current storage abstraction.

Not in scope: images embedded inside PDFs/Office files, arbitrary objects found by scanning a bucket, or retaining original images after processing/retry expiry.

## Compression Contract

- Images below 100 KiB remain unchanged.
- Raster images are fitted within 1920x1920 without upscaling and encoded adaptively toward a hard 1 MiB output ceiling.
- Static raster output uses WebP, preserving alpha. Quality starts at 75 and may fall to 60; resolution may step down to 1600, 1280, 1024, then 960 pixels on the longest side.
- Animated GIF/WebP preserves animation and uses adaptive resizing/encoding where supported.
- SVG remains SVG, is validated as an image, and must already be at or below 1 MiB.
- Decode configuration is checked before full decode. Images over 40 megapixels are rejected as unsafe.
- A valid raster image that cannot meet 1 MiB at the quality/resolution floor is classified as a permanent compression failure. Transient storage/runtime failures are retryable.
- Output filename extension, MIME type, size, and stored bytes always agree.

## New Upload Data Flow

1. Validate and hash the original upload for duplicate detection.
2. Compress the original before permanent storage.
3. Save the compressed bytes as the permanent knowledge object and store its path/size/type.
4. Save the original bytes as a temporary processing object.
5. Enqueue document processing with the temporary original path and a cleanup flag.
6. Delete the temporary original after successful processing or the final retry. OSS lifecycle expiration at three days is the crash/orphan safety net.

If saving either object or persisting the knowledge record fails, delete every object created by the request. Reparse operations use the compressed permanent image because the original has expired.

## Historical Migration Data Flow

1. Dry-run selects completed knowledge images larger than 1 MiB and reports count/bytes without downloading them.
2. Execution runs in the low-priority Asynq queue with bounded concurrency and at most three retry rounds for transient failures.
3. For each candidate: download old bytes, validate/compress, save a new object, atomically update the knowledge record, then delete the old object.
4. If the database update fails, delete the new object. If deleting the old object fails, retain a cleanup-pending result without rolling back the valid new record.
5. Existing RAG chunks/vectors are not regenerated.

## Metadata and Progress

`knowledges.compression_info` records original/stored sizes, formats, hashes, ratio, algorithm version, timestamp, and migration status. Batch progress is stored in Redis following existing clone/move progress patterns and includes total, processed, succeeded, retrying, skipped, permanently failed, bytes saved, round, and per-item errors.

## Retry Policy

- Compression inability, corrupt image data, unsafe pixel count, and unsupported oversized SVG are permanent failures.
- Network timeouts and object-storage read/write/delete failures are transient.
- A round processes every pending item once. Retryable failures are held until the round finishes, then retried as a group after 30 seconds, 2 minutes, and 10 minutes. Three retry rounds are allowed after the initial attempt.

## Success Criteria

- Permanent raster images produced by the feature are no larger than 1 MiB.
- Original bytes are used for first-pass image RAG and are deleted on success/final failure; an OSS three-day lifecycle rule remains the cleanup safety net.
- DingTalk ingestion and direct knowledge upload share the same compression path.
- Historical replacement never overwrites the old key in place and never leaves the database pointing at a partially written object.
- Transparent and animated test fixtures retain their defining behavior.
- Existing non-image upload and parsing behavior remains unchanged.

## Verification

- Focused Go unit tests for formats, limits, transparency, animation, failure classification, and adaptive sizing.
- Service tests for original temporary path vs. compressed permanent path and cleanup across retries.
- Historical migration tests for dry-run, swap rollback, idempotency, retries, and progress.
- Backend package tests and frontend type/build checks.
