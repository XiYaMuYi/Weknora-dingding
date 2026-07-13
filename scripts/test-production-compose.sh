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

latest_env="$(mktemp)"
trap 'rm -f "$rendered" "$render_env" "$latest_env"' EXIT
sed 's/^WEKNORA_VERSION=.*/WEKNORA_VERSION=latest/' "$ENV_FILE" >"$latest_env"
if "$ROOT/scripts/production.sh" preflight --env-file "$latest_env" >/dev/null 2>&1; then
  echo "preflight unexpectedly accepted WEKNORA_VERSION=latest" >&2
  exit 1
fi

test -f "$ROOT/docs/生产环境配置说明.md"
RUNBOOK="$ROOT/docs/生产环境配置说明.md"
rg -F 'make production-preflight' "$RUNBOOK" >/dev/null
rg -F 'make production-deploy' "$RUNBOOK" >/dev/null
rg -F 'make production-status' "$RUNBOOK" >/dev/null
rg -F 'make production-backup dir=/absolute/backup/path' "$RUNBOOK" >/dev/null
rg -F 'WEKNORA_ASYNQ_CONCURRENCY=2' "$RUNBOOK" >/dev/null

require_exactly_one_down_command() {
  [[ "$1" == '1' ]]
}

synthetic_down_count="$(printf '%s\n' 'docker compose down -v; docker compose down -v' | rg -o -F 'docker compose down -v' | wc -l | tr -d '[:space:]')"
if [[ "$synthetic_down_count" != '2' ]]; then
  echo 'docker compose down -v occurrence counting must distinguish two matches on one line' >&2
  exit 1
fi

if require_exactly_one_down_command "$synthetic_down_count"; then
  echo 'docker compose down -v must reject two occurrences' >&2
  exit 1
fi

if ! require_exactly_one_down_command "$(rg -o -F 'docker compose down -v' "$RUNBOOK" | wc -l | tr -d '[:space:]')"; then
  echo 'docker compose down -v must appear exactly once in the production runbook' >&2
  exit 1
fi

if ! awk '
  /^#{2,}[[:space:]]+禁止命令[[:space:]]*$/ {
    in_prohibited_section = 1
    next
  }
  in_prohibited_section && /^#{1,3}[[:space:]]/ {
    in_prohibited_section = 0
  }
  in_prohibited_section && /docker compose down -v/ {
    found_in_prohibited_section = 1
  }
  END {
    exit(found_in_prohibited_section ? 0 : 1)
  }
' "$RUNBOOK"; then
  echo 'docker compose down -v must appear in the 禁止命令 section' >&2
  exit 1
fi

PRODUCTION_SCRIPT="$ROOT/scripts/production.sh"
BASE_COMPOSE="$ROOT/docker-compose.yml"
OPERATIONS_GUIDE="$ROOT/docs/技术安装部署维护操作说明.md"
failures=0

fail() {
  printf 'final-review static check failed: %s\n' "$1" >&2
  failures=$((failures + 1))
}

require_fixed() {
  local needle="$1"
  local file="$2"
  local description="$3"
  rg -F "$needle" "$file" >/dev/null || fail "$description"
}

require_regex() {
  local pattern="$1"
  local file="$2"
  local description="$3"
  rg "$pattern" "$file" >/dev/null || fail "$description"
}

function_body() {
  local name="$1"
  awk -v signature="${name}() {" '
    $0 == signature { in_function = 1 }
    in_function { print }
    in_function && /^}$/ { exit }
  ' "$PRODUCTION_SCRIPT"
}

redis_service="$(awk '
  /^  redis:$/ { in_service = 1 }
  in_service && /^  [[:alnum:]_-]+:$/ && $0 != "  redis:" { exit }
  in_service { print }
' "$BASE_COMPOSE")"
if ! grep -Fq -- '- redis-data:/data' <<<"$redis_service"; then
  fail 'Redis must mount the redis-data named volume at /data'
fi
require_regex '^  redis-data:$' "$BASE_COMPOSE" 'redis-data must be declared in the root volumes map'

require_fixed 'WEKNORA_VERSION 必须固定为非 latest 版本' "$PRODUCTION_SCRIPT" 'preflight must require a pinned WEKNORA_VERSION'
require_regex "WEKNORA_VERSION.*latest|latest.*WEKNORA_VERSION" "$PRODUCTION_SCRIPT" 'preflight must explicitly reject WEKNORA_VERSION=latest'

require_fixed 'mem_limit: 512m' "$OVERRIDE_FILE" 'frontend must have a 512m memory limit'
require_fixed 'cpus: 0.5' "$OVERRIDE_FILE" 'frontend must have a 0.5 CPU limit'

require_regex '^read_database_concurrency\(\)' "$PRODUCTION_SCRIPT" 'database concurrency SQL must be factored into a read function'
require_regex '^validate_database_concurrency\(\)' "$PRODUCTION_SCRIPT" 'database concurrency validation must be factored into a validation function'
if [[ "$(rg -o -F "WHERE key = 'asynq.concurrency'" "$PRODUCTION_SCRIPT" | wc -l | tr -d '[:space:]')" != '1' ]]; then
  fail 'database concurrency SQL must appear exactly once'
fi
require_fixed 'pg_isready' "$PRODUCTION_SCRIPT" 'deploy must wait for PostgreSQL readiness'

deploy_body="$(function_body deploy)"
preflight_line="$(grep -n -F '  preflight' <<<"$deploy_body" | head -n 1 | cut -d: -f1 || true)"
dependencies_line="$(grep -n -F 'compose up -d postgres redis docreader' <<<"$deploy_body" | head -n 1 | cut -d: -f1 || true)"
wait_line="$(grep -n -F 'wait_for_postgres' <<<"$deploy_body" | head -n 1 | cut -d: -f1 || true)"
validate_line="$(grep -n -F 'validate_database_concurrency' <<<"$deploy_body" | head -n 1 | cut -d: -f1 || true)"
app_line="$(grep -n -F 'compose up -d --remove-orphans' <<<"$deploy_body" | head -n 1 | cut -d: -f1 || true)"
if [[ -z "$preflight_line" || -z "$dependencies_line" || -z "$wait_line" || -z "$validate_line" || -z "$app_line" ]] ||
   ! ((preflight_line < dependencies_line && dependencies_line < wait_line && wait_line < validate_line && validate_line < app_line)); then
  fail 'deploy must preflight, start dependencies, wait for PostgreSQL, validate DB concurrency, then start the app'
fi
if rg 'compose down|redis-cli.*FLUSH|docker volume (rm|prune)|(^|[[:space:]])-v([[:space:]]|$)' <<<"$deploy_body" >/dev/null; then
  fail 'deploy must not stop the stack, flush Redis, or remove volumes'
fi

preflight_body="$(function_body preflight)"
status_body="$(function_body status)"
if rg 'compose up|compose down|redis-cli.*(SET|DEL|FLUSH)|docker volume (rm|prune)' <<<"$preflight_body$status_body" >/dev/null; then
  fail 'preflight and status must remain read-only'
fi

require_fixed '有效 Asynq 并发' "$PRODUCTION_SCRIPT" 'status must report effective environment and database Asynq concurrency'
require_fixed 'PostgreSQL 检查' "$PRODUCTION_SCRIPT" 'status must label the PostgreSQL check'
require_fixed 'Redis 检查' "$PRODUCTION_SCRIPT" 'status must label the Redis check'
require_fixed 'DocReader 检查' "$PRODUCTION_SCRIPT" 'status must label the DocReader check'
require_fixed 'SMEMBERS asynq:queues' "$PRODUCTION_SCRIPT" 'status must discover actual Asynq queues from Redis'
require_fixed 'LLEN "asynq:{${queue}}:pending"' "$PRODUCTION_SCRIPT" 'status must report per-queue pending counts'
require_fixed 'LLEN "asynq:{${queue}}:active"' "$PRODUCTION_SCRIPT" 'status must report per-queue active counts'
require_fixed 'ZCARD "asynq:{${queue}}:retry"' "$PRODUCTION_SCRIPT" 'status must report per-queue retry counts'
require_fixed 'ZCARD "asynq:{${queue}}:archived"' "$PRODUCTION_SCRIPT" 'status must report per-queue archived counts'
require_fixed '未发现 Asynq 队列' "$PRODUCTION_SCRIPT" 'status must safely report when no Asynq queues exist'
require_fixed '/api/tags' "$PRODUCTION_SCRIPT" 'status must request the Ollama model list'
require_regex 'ps .*pcpu.*pmem' "$PRODUCTION_SCRIPT" 'status must report locally observable Ollama CPU and memory'
require_fixed 'Ollama 本地进程资源不可用' "$PRODUCTION_SCRIPT" 'status must clearly report unavailable local Ollama process metrics'
require_regex 'df -h' "$PRODUCTION_SCRIPT" 'status must report filesystem capacity'
require_fixed '最近有效备份' "$PRODUCTION_SCRIPT" 'status must report backup age'
require_fixed '备份状态: 不可用' "$PRODUCTION_SCRIPT" 'status must report unavailable when no valid backup exists'
require_fixed 'PRODUCTION_BACKUP_DIR=/var/backups/weknora' "$ENV_FILE" 'the production environment example must define PRODUCTION_BACKUP_DIR'

require_fixed 'Redis AOF 数据保存在 `redis-data` 命名卷' "$RUNBOOK" 'runbook must document Redis persistence'
require_fixed '`WEKNORA_VERSION` 必须固定为明确版本且不能使用 `latest`' "$RUNBOOK" 'runbook must document pinned versions'
require_fixed '`PRODUCTION_BACKUP_DIR`' "$RUNBOOK" 'runbook must document the status backup directory'
require_fixed '每个 Asynq 队列' "$RUNBOOK" 'runbook must document expanded status output'
require_fixed '`make production-status`' "$OPERATIONS_GUIDE" 'operations guide must reference production status'
require_fixed 'Redis AOF' "$OPERATIONS_GUIDE" 'operations guide must document production Redis persistence'
require_fixed '`PRODUCTION_BACKUP_DIR`' "$OPERATIONS_GUIDE" 'operations guide must document the backup status directory'

if ((failures > 0)); then
  printf '%s final-review static check(s) failed\n' "$failures" >&2
  exit 1
fi
