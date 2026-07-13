#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
ENV_FILE="$ROOT/deploy/production/.env.production"
OVERRIDE_FILE="$ROOT/deploy/production/docker-compose.production.yml"

compose() {
  docker compose --env-file "$ENV_FILE" \
    -f "$ROOT/docker-compose.yml" \
    -f "$OVERRIDE_FILE" "$@"
}

die() { printf '生产操作已拒绝：%s\n' "$*" >&2; exit 1; }

require_env_file() {
  test -f "$ENV_FILE" || die "缺少 ${ENV_FILE}；请从 .env.production.example 创建并填入生产秘密"
  local version
  version="$(sed -n 's/^WEKNORA_VERSION=//p' "$ENV_FILE" | tail -n 1)"
  case "$version" in
    ''|latest|*:latest) die "WEKNORA_VERSION 必须固定为非 latest 版本" ;;
  esac
  grep -Eq '^WEKNORA_ASYNQ_CONCURRENCY=2$' "$ENV_FILE" || die "WEKNORA_ASYNQ_CONCURRENCY 必须为 2"
  grep -Eq '^CONCURRENCY_POOL_SIZE=1$' "$ENV_FILE" || die "CONCURRENCY_POOL_SIZE 必须为 1"
  grep -Eq '^FRONTEND_BIND=127\.0\.0\.1$' "$ENV_FILE" || die "FRONTEND_BIND 必须绑定到 127.0.0.1"
  grep -Eq '^APP_BIND=127\.0\.0\.1$' "$ENV_FILE" || die "APP_BIND 必须绑定到 127.0.0.1"
  grep -Eq '^DB_BIND=127\.0\.0\.1$' "$ENV_FILE" || die "DB_BIND 必须绑定到 127.0.0.1"
  grep -Eq '^REDIS_BIND=127\.0\.0\.1$' "$ENV_FILE" || die "REDIS_BIND 必须绑定到 127.0.0.1"
}

read_database_concurrency() {
  compose exec -T postgres \
    psql -U "${DB_USER}" -d "${DB_NAME}" -Atqc \
    "SELECT COALESCE(value::text, '') FROM system_settings WHERE key = 'asynq.concurrency' LIMIT 1" \
    | tr -d '[:space:]'
}

validate_database_concurrency() {
  local db_concurrency
  db_concurrency="$(read_database_concurrency)"
  case "$db_concurrency" in
    ''|'2') ;;
    *) die "system_settings.asynq.concurrency=${db_concurrency}，会覆盖环境变量 2；请在系统设置中改为 2 后重启 app" ;;
  esac
}

wait_for_postgres() {
  local attempt
  for attempt in $(seq 1 60); do
    if compose exec -T postgres pg_isready -U "${DB_USER}" -d "${DB_NAME}" >/dev/null 2>&1; then
      return 0
    fi
    sleep 1
  done
  die "PostgreSQL 在 60 秒内未就绪，未启动 app"
}

load_env() {
  set -a
  # shellcheck disable=SC1090
  source "$ENV_FILE"
  set +a
}

preflight() {
  require_env_file
  test -f "$OVERRIDE_FILE" || die "缺少生产 Compose 覆盖文件"
  compose config >/dev/null
  load_env

  local app_count
  app_count="$(compose ps -q app | sed '/^$/d' | wc -l | tr -d ' ')"
  test "$app_count" -le 1 || die "检测到 $app_count 个 app 容器；当前架构只允许一个"

  if test -n "$(compose ps --status running -q postgres)"; then
    validate_database_concurrency
  fi
}

redis() {
  compose exec -T redis redis-cli --no-auth-warning -a "${REDIS_PASSWORD}" -n "${REDIS_DB:-0}" "$@"
}

queue_size() {
  local command="$1"
  local key="$2"
  redis --raw "$command" "$key"
}

report_backup_status() {
  local backup_dir="${PRODUCTION_BACKUP_DIR:-}"
  if test -z "$backup_dir" || ! test -d "$backup_dir"; then
    printf '备份状态: 不可用（PRODUCTION_BACKUP_DIR 不存在）\n'
    return
  fi

  local latest_backup
  latest_backup="$(find "$backup_dir" -type f -name SHA256SUMS -printf '%T@ %p\n' 2>/dev/null | sort -nr | head -n 1 || true)"
  if test -z "$latest_backup"; then
    printf '备份状态: 不可用（未找到 SHA256SUMS）\n'
    return
  fi
  printf '最近有效备份: %s\n' "${latest_backup#* }"
}

report_ollama_resources() {
  local host
  host="$(printf '%s' "$OLLAMA_BASE_URL" | sed -E 's#^https?://([^/:]+).*#\1#')"
  if [[ "$host" != "127.0.0.1" && "$host" != "localhost" && "$host" != "host.docker.internal" ]]; then
    printf 'Ollama 本地进程资源不可用（当前为远端地址：%s）\n' "$OLLAMA_BASE_URL"
    return
  fi

  local pids
  pids="$(pgrep -f 'ollama serve|llama-server' || true)"
  if test -z "$pids"; then
    printf 'Ollama 本地进程资源不可用（未发现 Ollama 进程）\n'
    return
  fi
  ps -o pid=,pcpu=,pmem=,command= -p "$(printf '%s' "$pids" | tr '\n' ', ' | sed 's/,$//')"
}

status() {
  require_env_file
  load_env

  compose ps
  printf '\n有效 Asynq 并发: 环境=%s，数据库=' "${WEKNORA_ASYNQ_CONCURRENCY}"
  read_database_concurrency || true
  printf '\nPostgreSQL 检查: '
  compose exec -T postgres pg_isready -U "${DB_USER}" -d "${DB_NAME}"
  printf 'Redis 检查: '
  redis PING
  printf 'DocReader 检查: '
  compose exec -T docreader grpc_health_probe -addr=localhost:50051
  printf 'App 检查: '
  curl --fail --silent --show-error http://127.0.0.1:"${APP_PORT}"/health
  printf '\nOllama 模型:\n'
  curl --fail --silent --show-error "${OLLAMA_BASE_URL}/api/tags"
  printf '\nOllama 运行模型:\n'
  curl --fail --silent --show-error "${OLLAMA_BASE_URL}/api/ps" || true
  printf '\nOllama 本地资源:\n'
  report_ollama_resources

  printf '\nAsynq 队列状态:\n'
  local queue
  local found_queue=false
  while IFS= read -r queue; do
    test -n "$queue" || continue
    found_queue=true
    printf '%s: pending=%s active=%s retry=%s archived=%s\n' "$queue" \
      "$(queue_size LLEN "asynq:{${queue}}:pending")" \
      "$(queue_size LLEN "asynq:{${queue}}:active")" \
      "$(queue_size ZCARD "asynq:{${queue}}:retry")" \
      "$(queue_size ZCARD "asynq:{${queue}}:archived")"
  done < <(redis --raw SMEMBERS asynq:queues)
  if test "$found_queue" = false; then
    printf '未发现 Asynq 队列\n'
  fi

  printf '\n文件系统容量:\n'
  df -h "$ROOT"
  report_backup_status
  printf '\n待处理文档:\n'
  compose exec -T postgres psql -U "${DB_USER}" -d "${DB_NAME}" -P pager=off -c \
    "SELECT parse_status, count(*), min(updated_at) AS oldest_update
     FROM knowledges
     WHERE deleted_at IS NULL AND parse_status IN ('pending', 'processing')
     GROUP BY parse_status
     ORDER BY parse_status;"
}

backup() {
  test "$#" = 1 || die "用法：$0 backup <directory>"

  local backup_dir="$1"
  if test -e "$backup_dir" && ! test -d "$backup_dir"; then
    die "备份目标必须是目录：$backup_dir"
  fi
  if test -d "$backup_dir" && test -n "$(find "$backup_dir" -mindepth 1 -maxdepth 1 -print -quit)"; then
    die "备份目标目录必须不存在或为空：$backup_dir"
  fi

  require_env_file
  load_env
  mkdir -p "$backup_dir"

  compose exec -T postgres pg_dump -U "${DB_USER}" -Fc "${DB_NAME}" >"$backup_dir/postgres.dump"

  local data_volume
  data_volume="$(docker volume ls -q \
    --filter "label=com.docker.compose.project=${COMPOSE_PROJECT_NAME}" \
    --filter 'label=com.docker.compose.volume=data-files')"
  if test "$(printf '%s\n' "$data_volume" | sed '/^$/d' | wc -l | tr -d ' ')" != "1"; then
    printf 'docker volume ls 返回：\n%s\n' "$data_volume" >&2
    die "未找到唯一的 data-files 卷"
  fi

  docker run --rm -v "${data_volume}:/source:ro" \
    -v "$backup_dir:/backup" alpine:3.21 \
    tar -C /source -czf /backup/data-files.tar.gz .
  shasum -a 256 "$backup_dir/postgres.dump" "$backup_dir/data-files.tar.gz" >"$backup_dir/SHA256SUMS"
}

deploy() {
  preflight
  load_env
  compose up -d postgres redis docreader
  wait_for_postgres
  validate_database_concurrency
  compose up -d --remove-orphans
  status
}

usage() {
  printf '用法：%s {preflight|status|backup <directory>|deploy} [--env-file <path>]\n' "$0" >&2
  exit 1
}

test "$#" -gt 0 || usage
command="$1"
shift

while test "$#" -gt 0; do
  case "$1" in
    --env-file)
      test "$#" -ge 2 || die "--env-file 需要路径"
      ENV_FILE="$2"
      shift 2
      ;;
    *)
      break
      ;;
  esac
done

case "$command" in
  preflight)
    test "$#" = 0 || usage
    preflight
    ;;
  status)
    test "$#" = 0 || usage
    status
    ;;
  backup)
    backup "$@"
    ;;
  deploy)
    test "$#" = 0 || usage
    deploy
    ;;
  *) usage ;;
esac
