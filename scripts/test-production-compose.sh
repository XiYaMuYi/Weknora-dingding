#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
ENV_FILE="$ROOT/deploy/production/.env.production.example"
OVERRIDE_FILE="$ROOT/deploy/production/docker-compose.production.yml"

test -x "$ROOT/scripts/production.sh"
test -f "$ENV_FILE"
test -f "$OVERRIDE_FILE"

rendered="$(mktemp)"
render_env="$(mktemp)"
trap 'rm -f "$rendered" "$render_env"' EXIT
# 真实生产文件会让 App 容器读取 .env.production；静态测试必须改为读取
# 可提交的示例文件，不能要求开发机存在任何真实生产秘密。
sed "s|^WEKNORA_ENV_FILE=.*$|WEKNORA_ENV_FILE=$ROOT/deploy/production/.env.production.example|" \
  "$ENV_FILE" >"$render_env"
docker compose --env-file "$render_env" \
  -f "$ROOT/docker-compose.yml" \
  -f "$OVERRIDE_FILE" config >"$rendered"

grep -Fq 'name: weknora-prod' "$rendered"
# Compose v2 emits normalized map syntax, while some older releases retain
# the list syntax, so accept either representation of each required value.
grep -Eq 'WEKNORA_ASYNQ_CONCURRENCY(: "?2"?|=2)' "$rendered"
grep -Eq 'CONCURRENCY_POOL_SIZE(: "?1"?|=1)' "$rendered"
grep -Eq '127\.0\.0\.1:|host_ip: 127\.0\.0\.1' "$rendered"
grep -Eq 'max-size: "?20m"?' "$rendered"

if "$ROOT/scripts/production.sh" preflight --env-file /tmp/does-not-exist >/dev/null 2>&1; then
  echo "preflight unexpectedly accepted a missing environment file" >&2
  exit 1
fi

test -f "$ROOT/docs/生产环境配置说明.md"
rg -F 'make production-preflight' "$ROOT/docs/生产环境配置说明.md" >/dev/null
rg -F 'make production-deploy' "$ROOT/docs/生产环境配置说明.md" >/dev/null
rg -F 'make production-status' "$ROOT/docs/生产环境配置说明.md" >/dev/null
rg -F 'make production-backup dir=/absolute/backup/path' "$ROOT/docs/生产环境配置说明.md" >/dev/null
rg -F 'WEKNORA_ASYNQ_CONCURRENCY=2' "$ROOT/docs/生产环境配置说明.md" >/dev/null
rg -F 'docker compose down -v' "$ROOT/docs/生产环境配置说明.md" >/dev/null
