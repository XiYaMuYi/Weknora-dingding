#!/usr/bin/env bash
set -euo pipefail

PROJECT_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$PROJECT_ROOT"

set -a
# shellcheck source=/dev/null
source .env
set +a

export DB_HOST=localhost
export DOCREADER_ADDR=localhost:50051
export DOCREADER_TRANSPORT=grpc
export MINIO_ENDPOINT=localhost:9000
export REDIS_ADDR=localhost:6379
export MILVUS_ADDRESS=localhost:19530
export NEO4J_URI=bolt://localhost:7687
export QDRANT_HOST=localhost

if [ -z "${LOCAL_STORAGE_BASE_DIR:-}" ] || [ "$LOCAL_STORAGE_BASE_DIR" = "/data/files" ]; then
  export LOCAL_STORAGE_BASE_DIR="$PROJECT_ROOT/.local-data/files"
fi
mkdir -p "$LOCAL_STORAGE_BASE_DIR"

exec "$PROJECT_ROOT/tmp/main"
