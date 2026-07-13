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
  grep -Eq '^WEKNORA_ASYNQ_CONCURRENCY=2$' "$ENV_FILE" || die "WEKNORA_ASYNQ_CONCURRENCY 必须为 2"
  grep -Eq '^CONCURRENCY_POOL_SIZE=1$' "$ENV_FILE" || die "CONCURRENCY_POOL_SIZE 必须为 1"
  grep -Eq '^FRONTEND_BIND=127\.0\.0\.1$' "$ENV_FILE" || die "FRONTEND_BIND 必须绑定到 127.0.0.1"
  grep -Eq '^APP_BIND=127\.0\.0\.1$' "$ENV_FILE" || die "APP_BIND 必须绑定到 127.0.0.1"
  grep -Eq '^DB_BIND=127\.0\.0\.1$' "$ENV_FILE" || die "DB_BIND 必须绑定到 127.0.0.1"
  grep -Eq '^REDIS_BIND=127\.0\.0\.1$' "$ENV_FILE" || die "REDIS_BIND 必须绑定到 127.0.0.1"
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

  local postgres_id
  postgres_id="$(compose ps --status running -q postgres)"
  if test -n "$postgres_id"; then
    local db_concurrency
    db_concurrency="$(compose exec -T postgres \
      psql -U "${DB_USER}" -d "${DB_NAME}" -Atqc \
      "SELECT COALESCE(value::text, '') FROM system_settings WHERE key = 'asynq.concurrency' LIMIT 1" \
      | tr -d '[:space:]')"
    case "$db_concurrency" in
      ''|'2') ;;
      *) die "system_settings.asynq.concurrency=${db_concurrency}，会覆盖环境变量 2；请在系统设置中改为 2 后重启 app" ;;
    esac
  fi
}

status() {
  require_env_file
  load_env

  compose ps
  curl --fail --silent --show-error http://127.0.0.1:"${APP_PORT}"/health
  curl --fail --silent --show-error "${OLLAMA_BASE_URL}/api/tags" >/dev/null
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
