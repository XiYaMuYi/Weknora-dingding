# Production Runtime Design

**Status:** approved design direction, pending implementation plan

## Goal

Deploy WeKnora on one internal Docker Compose host without allowing a local
Ollama instance, a large DingTalk document, or a duplicate reparse request to
exhaust the host or strand a document in `processing`. Keep the deployment
layout compatible with a later Kubernetes migration.

## Evidence From The Current Repository

- `docker-compose.yml` deploys one `app` container by default and gives it
  `restart: unless-stopped` plus a `/health` check.
- Every non-lite `app` process starts both the HTTP server and the Asynq task
  server. There is no existing API-only or worker-only execution mode.
- The Asynq default concurrency is `32` per app process. A database system
  setting `asynq.concurrency` takes precedence over
  `WEKNORA_ASYNQ_CONCURRENCY`; changing it requires an app restart.
- The Compose defaults also allow `CONCURRENCY_POOL_SIZE=5`. It governs
  embedding/model-call concurrency and is too aggressive for a CPU-hosted
  local Ollama embedding model.
- DocReader already exposes conservative parser limits, each defaulting to
  `1` (`DOCREADER_ODL_MAX_WORKERS`,
  `DOCREADER_MARKITDOWN_MAX_WORKERS`, and
  `DOCREADER_PDF_RENDER_MAX_WORKERS`).
- The existing technical operations document covers installation, secrets,
  backup, upgrade, health, and general task troubleshooting. It does not
  define a production concurrency baseline, a queue/drain procedure, or a
  multiple-app-instance safety rule.
- The Helm chart has resource probes and persistent storage, but its example
  of `app.replicaCount: 3` would start three HTTP servers and three Asynq
  consumers under the current application design. It is not a safe recipe for
  one local Ollama instance.

## Scope And Non-Goals

### Phase 1: production baseline

Phase 1 changes deployment configuration, validation tooling, and operating
documentation only. It does not modify DingTalk synchronization, incremental
updates, document parsing, chunking, or the original indexing pipeline.

The target is a single internal host running Docker Compose. PostgreSQL,
Redis, DocReader, the App, Frontend, and optional observability run in
containers. Ollama is treated as an external model service: it may run on the
same host or a dedicated internal host, but it is not part of this Compose
stack.

### Phase 2: scalability architecture

Phase 2 will add explicit application run modes so API pods and worker pods
can scale independently. It is not implemented as part of the first production
rollout.

## Phase 1 Architecture

```text
Users -> HTTPS reverse proxy -> Frontend -> one App container
                                            |-- HTTP API
                                            `-- one Asynq worker process
                                                     |
                                                     +-> Redis (production-only DB/instance)
                                                     +-> PostgreSQL / vector index
                                                     +-> DocReader (single replica)
                                                     `-> Ollama embedding service (concurrency 1)
```

Exactly one `app` container is permitted. It is both the API server and the
only asynchronous-task consumer. The reverse proxy, PostgreSQL, Redis,
DocReader, object storage, and Ollama are never exposed directly to the public
internet.

## Production Baseline

The production override must pin application images to a release or commit
tag, rather than `latest`, and must explicitly set these values:

| Setting | Phase 1 value | Reason |
|---|---:|---|
| App replicas | `1` | One Asynq server under the current coupled design. |
| `WEKNORA_ASYNQ_CONCURRENCY` | `2` | At most two background task slots while validating production behavior. |
| `asynq.concurrency` system setting | `2` or absent | Database settings override the environment; it must not retain `32`. |
| `CONCURRENCY_POOL_SIZE` | `1` | Serializes calls into CPU-hosted Ollama embeddings. |
| `DOCREADER_ODL_MAX_WORKERS` | `1` | Prevents heavy parser fan-out. |
| `DOCREADER_MARKITDOWN_MAX_WORKERS` | `1` | Prevents heavy parser fan-out. |
| `DOCREADER_PDF_RENDER_MAX_WORKERS` | `1` | Prevents PDF render fan-out. |
| `REDIS_DB` | dedicated production DB | Prevents test/development task queues from being consumed in production. |
| `REDIS_PREFIX` | environment-specific stream prefix | Separates application stream keys; Asynq isolation still depends on the dedicated Redis DB/instance. |
| `AUTO_RECOVER_DIRTY` | `true` | Preserves current migration recovery behavior. |

`WEKNORA_ASYNQ_CONCURRENCY=2` is deliberately a starting safety limit, not a
throughput promise. Raise it only after measuring queue age, CPU, memory,
Ollama latency, and successful completion of representative large documents.

The embedding model should use one request at a time while Ollama is CPU
hosted. Moving Ollama to an adequately sized GPU or managed embedding API is
the prerequisite for increasing this setting.

## Configuration And Persistence

- Use a production-only `.env.production` stored outside Git and injected into
  Compose. Do not reuse developer `.env` files or Docker volumes.
- Keep `JWT_SECRET`, `SYSTEM_AES_KEY`, `TENANT_AES_KEY`, database credentials,
  Redis credentials, third-party datasource credentials, and model API keys
  in the deployment secret store. `SYSTEM_AES_KEY` and `TENANT_AES_KEY` must
  remain stable across every restart and upgrade.
- Run PostgreSQL and Redis on persistent storage with documented backup and
  restore checks. When using PostgreSQL retrieval, the database contains the
  index; no re-chunking should be required after a verified database restore.
- Store uploaded/source files on a durable volume for the first rollout, or
  move to an internal object store. Back it up together with PostgreSQL.
- Use a dedicated Redis instance where possible. If one Redis server is shared
  with other services, allocate a dedicated production database number and
  never point development, staging, or Langfuse task traffic at it.

## Deployment And Upgrade Rules

1. Validate the rendered Compose configuration and confirm that exactly one
   `app` service will be started.
2. Snapshot PostgreSQL and application file/object storage before image or
   configuration changes.
3. Record the previous image tag, Git commit, effective environment values,
   current Asynq concurrency system setting, and queue depth.
4. Stop new manual reparses and scheduled datasource syncs before the upgrade.
5. Wait for active parsing tasks to finish, or explicitly cancel them through
   the application task path. Do not delete Redis keys by hand.
6. Deploy the pinned image, wait for `/health`, then verify Redis, DocReader,
   Ollama connectivity, and one normal document parse.
7. Re-enable scheduled syncs only after the validation succeeds.
8. Roll back by restoring the previous image and production configuration. A
   data restore is reserved for a confirmed data-corrupting release; it is not
   the normal rollback mechanism.

`docker compose down -v`, volume deletion, and ad-hoc Redis flushing are
forbidden in production runbooks because they can destroy persisted documents,
indexes, or queued task state.

## Runtime Guardrails

- One document may have only one active parse/reparse operation. Operators
  must wait for its terminal status before issuing another reparse.
- A DingTalk attachment download permission failure is a connector-level
  skipped-item event; it must not retry the whole document synchronization or
  enqueue document parsing for the skipped attachment.
- A document remaining in `processing` past its documented timeout is an
  incident. Capture its ID, current processing spans, app logs, DocReader
  logs, Redis queue state, and Ollama process metrics before cancelling or
  retrying it.
- Never use a browser refresh, container restart, or a second reparse as the
  first response to a slow large document. Those actions can create duplicate
  work or obscure the original failure.
- All production services receive CPU and memory limits. Ollama receives a
  dedicated capacity budget and is monitored separately because the App's
  health endpoint does not prove that embeddings are making progress.

## Required Operational Visibility

The first rollout needs a small, repeatable status command or script that
reports:

- the number of running App containers;
- the effective Asynq concurrency from both environment and system settings;
- active, pending, retry, and archived task counts by queue;
- `processing` documents grouped by age;
- DocReader health;
- Ollama health, loaded model, CPU, memory, and recent request latency;
- PostgreSQL, Redis, storage capacity, and backup age.

The existing Langfuse integration may be enabled for task and model traces,
but operational correctness must not depend on Langfuse being available.

## Acceptance Criteria For Phase 1

1. A rendered production Compose configuration starts exactly one App
   container and protects all persistent volumes from accidental removal.
2. A fresh production environment has effective Asynq concurrency `2` and
   embedding concurrency `1`, including after a restart.
3. A representative DingTalk online document, the 30,000-row boundary sheet,
   and the over-30,000-row segmented sheet all reach `completed` without
   duplicate processing spans or runaway host CPU.
4. A deliberate Ollama outage causes a visible task failure/retry state and
   leaves enough evidence to diagnose it without manually inspecting Redis
   keys.
5. A controlled App restart preserves completed chunks and indexes; it does
   not require re-chunking completed documents.
6. Backup restoration in a non-production environment restores a completed
   DingTalk document and makes it searchable without invoking a reparse.

## Phase 2: API And Worker Separation

Add two explicit startup modes:

- `api`: initialize dependencies, routes, and HTTP serving, but do not invoke
  `RunAsynqServer`.
- `worker`: initialize the same task dependencies and Asynq handlers, but do
  not bind the HTTP listener.

The worker deployment starts with one replica and low concurrency. The API
deployment may scale horizontally only after worker mode is proven. Both modes
share the same database, Redis database, secret set, and application image.

Before Phase 2 enables more than one worker replica, the implementation must
prove document-level task idempotency, safe cancellation, duplicate enqueue
handling, graceful task drain on shutdown, and task metrics. Kubernetes or
multiple Compose hosts are deployment choices made after those guarantees
exist, not substitutes for them.
