# 单机 Docker Compose 生产运行基线 Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**目标：** 为 WeKnora 建立可直接用于内网单机生产环境的 Docker Compose 运行基线，限制本地 Ollama 场景下的异步解析并发，提供部署前检查、运行状态诊断、备份和可执行运维手册，同时不改动钉钉同步、增量更新、文档解析或切片实现。

**架构：** 第一阶段保留现有“一个 `app` 同时承载 HTTP API 与 Asynq Worker”的进程结构，明确强制单副本，并将 Asynq、Embedding 与 DocReader 的资源并发配置收敛到保守值。通过生产专用 Compose 环境文件、只绑定回环地址的端口配置、运维脚本和验收脚本约束生产操作；API/Worker 拆分只记录为后续独立工程，不在本计划实施。

**技术栈：** Docker Compose v2、Bash、PostgreSQL/ParadeDB、Redis/Asynq、Go App、DocReader gRPC、外部 Ollama、Markdown 运维文档。

## 全局约束

- 仅支持“单机 Docker Compose + 内网/受控网络 + 外部 Ollama”的第一阶段生产形态。
- 首次生产参数固定为：`WEKNORA_ASYNQ_CONCURRENCY=2`、`CONCURRENCY_POOL_SIZE=1`、三项 DocReader 重型解析 worker 均为 `1`。
- 当前一个 App 进程会同时启动 HTTP 和 Asynq Worker，因此生产运行时 `app` 只能为一个副本。
- `system_settings.asynq.concurrency` 的优先级高于环境变量；有效值必须为 `2` 或未设置，不能遗留 `32`。
- 严禁改动 `internal/datasource/connector/dingtalk/`、增量同步、已有解析/切片逻辑和已完成文档的 chunks/index。
- 生产部署不得使用 `latest` 镜像标签、`docker compose down -v`、Docker volume 删除命令或 Redis `FLUSH*` 命令。
- 生产密钥、真实域名、数据库密码、Redis 密码、Ollama 地址、第三方数据源凭证不进入 Git。

---

## 文件结构

| 文件 | 职责 |
|---|---|
| `docker-compose.yml` | 保持默认本地部署能力，新增可配置 host bind，供生产模板将服务限制在回环地址。 |
| `.env.example` | 补充环境文件选择与 host bind 的说明，不写入生产秘密。 |
| `deploy/production/.env.production.example` | 不含真实秘密的生产变量模板，固定保守并发、独立 Redis DB 和镜像标签变量。 |
| `deploy/production/docker-compose.production.yml` | 生产覆盖层：资源上限、日志轮转、单 App 约束、对外暴露策略。 |
| `scripts/production.sh` | 提供 `preflight`、`status`、`backup`、`deploy` 四个显式生产操作；拒绝危险状态。 |
| `scripts/test-production-compose.sh` | 无需启动业务容器的 Compose 渲染与脚本静态验证。 |
| `docs/技术安装部署维护操作说明.md` | 补充生产基线、部署/升级/回滚、队列卡住时的处置规则。 |
| `docs/生产环境配置说明.md` | 新增面向管理员的一页式生产配置与验收手册。 |

## Task 1：让 Compose 支持生产环境文件与安全端口绑定

**文件：**

- 修改：`docker-compose.yml: frontend/app/postgres/redis 的 ports 与 app env_file`
- 修改：`.env.example: Docker/网络配置区`
- 创建：`deploy/production/.env.production.example`
- 创建：`deploy/production/docker-compose.production.yml`
- 测试：`scripts/test-production-compose.sh`

**接口：**

- 使用者通过 `docker compose --env-file deploy/production/.env.production -f docker-compose.yml -f deploy/production/docker-compose.production.yml ...` 启动。
- `WEKNORA_ENV_FILE` 选择 App 容器读取的变量文件，默认值必须继续是 `.env`。
- `FRONTEND_BIND`、`APP_BIND`、`DB_BIND` 与 `REDIS_BIND` 控制 Docker 主机端口监听地址，默认值保持当前公开绑定行为。

- [ ] **步骤 1：先创建 Compose 渲染失败的检查脚本**

创建 `scripts/test-production-compose.sh`，先只包含下列断言，使其在生产模板尚未创建时失败：

```bash
#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
ENV_FILE="$ROOT/deploy/production/.env.production.example"
OVERRIDE_FILE="$ROOT/deploy/production/docker-compose.production.yml"

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

grep -Fq 'WEKNORA_ASYNQ_CONCURRENCY=2' "$rendered"
grep -Fq 'CONCURRENCY_POOL_SIZE=1' "$rendered"
grep -Fq '127.0.0.1:' "$rendered"
grep -Fq 'max-size: "20m"' "$rendered"
```

- [ ] **步骤 2：运行检查，确认模板缺失时失败**

运行：

```bash
bash scripts/test-production-compose.sh
```

预期：以 `deploy/production/.env.production.example` 或
`deploy/production/docker-compose.production.yml` 不存在失败。

- [ ] **步骤 3：修改基础 Compose 的可配置边界**

在 `docker-compose.yml` 中保留服务名、volume 名和现有默认端口；只将端口映射改为以下模式：

```yaml
frontend:
  ports:
    - "${FRONTEND_BIND:-0.0.0.0}:${FRONTEND_PORT:-80}:80"

app:
  ports:
    - "${APP_BIND:-0.0.0.0}:${APP_PORT:-8080}:8080"
  env_file:
    - ${WEKNORA_ENV_FILE:-.env}

postgres:
  ports:
    - "${DB_BIND:-0.0.0.0}:${DB_PORT:-5432}:5432"

redis:
  ports:
    - "${REDIS_BIND:-0.0.0.0}:${REDIS_PORT:-6379}:6379"
```

不得修改 `docreader` 不发布 host 端口的现有安全策略。将 `.env.example` 中新增变量注释为“生产环境由 `deploy/production/.env.production` 设置为 `127.0.0.1`；默认保留兼容当前本地 Compose 行为”。

- [ ] **步骤 4：创建生产环境变量模板**

创建 `deploy/production/.env.production.example`，包含下列非秘密值；所有带 `<...>` 的秘密必须保持占位符且不能被 Compose 自动加载为真实生产文件：

```dotenv
WEKNORA_ENV_FILE=deploy/production/.env.production
COMPOSE_PROJECT_NAME=weknora-prod
WEKNORA_VERSION=0.6.3

FRONTEND_BIND=127.0.0.1
FRONTEND_PORT=8088
APP_BIND=127.0.0.1
APP_PORT=8080
DB_BIND=127.0.0.1
DB_PORT=5432
REDIS_BIND=127.0.0.1
REDIS_PORT=6379

DB_DRIVER=postgres
DB_HOST=postgres
DB_NAME=WeKnora
DB_USER=postgres
DB_PASSWORD=<replace-with-secret>
REDIS_ADDR=redis:6379
REDIS_PASSWORD=<replace-with-secret>
REDIS_DB=0
REDIS_PREFIX=weknora:prod:stream:

OLLAMA_BASE_URL=http://ollama.internal:11434
WEKNORA_ASYNQ_CONCURRENCY=2
CONCURRENCY_POOL_SIZE=1
DOCREADER_ODL_MAX_WORKERS=1
DOCREADER_MARKITDOWN_MAX_WORKERS=1
DOCREADER_PDF_RENDER_MAX_WORKERS=1

AUTO_RECOVER_DIRTY=true
DISABLE_REGISTRATION=true
WEKNORA_TENANT_ENABLE_RBAC=true
JWT_SECRET=<replace-with-secret>
SYSTEM_AES_KEY=<replace-with-stable-secret>
TENANT_AES_KEY=<replace-with-stable-secret>
```

模板需额外注释：`REDIS_DB=0` 只适用于专属生产 Redis；若共享 Redis，必须替换为未被任何其他环境和 Langfuse 使用的数据库编号。`SYSTEM_AES_KEY` 与 `TENANT_AES_KEY` 一旦上线不得轮换或丢失，除非执行单独的密钥迁移。

- [ ] **步骤 5：创建生产 Compose 覆盖层**

创建 `deploy/production/docker-compose.production.yml`。只覆盖资源、日志和生产强制变量，不复制基础 Compose 服务定义：

```yaml
services:
  frontend:
    logging: &json_logs
      driver: local
      options:
        max-size: "20m"
        max-file: "5"

  app:
    environment:
      WEKNORA_ASYNQ_CONCURRENCY: "2"
      CONCURRENCY_POOL_SIZE: "1"
    mem_limit: 4g
    cpus: 2.0
    logging: *json_logs

  docreader:
    environment:
      DOCREADER_ODL_MAX_WORKERS: "1"
      DOCREADER_MARKITDOWN_MAX_WORKERS: "1"
      DOCREADER_PDF_RENDER_MAX_WORKERS: "1"
    mem_limit: 4g
    cpus: 2.0
    logging: *json_logs

  postgres:
    mem_limit: 4g
    cpus: 2.0
    logging: *json_logs

  redis:
    mem_limit: 1g
    cpus: 1.0
    logging: *json_logs
```

在文件开头写明：`app` 不能使用 `--scale app`；当前进程同时运行 API 和 Asynq Worker。资源数值是单机 8 核/16 GB 起步的上限，Ollama 不在该 Compose 内，必须有自己的容量预算。

- [ ] **步骤 6：完成渲染检查并验证默认 Compose 兼容性**

运行：

```bash
bash scripts/test-production-compose.sh
docker compose --env-file .env.example -f docker-compose.yml config >/dev/null
```

预期：两个命令均为 `0`；第一个命令的渲染结果含 `WEKNORA_ASYNQ_CONCURRENCY=2`、`CONCURRENCY_POOL_SIZE=1`、回环地址和日志轮转；第二个命令证明普通 Compose 默认入口未被破坏。

- [ ] **步骤 7：提交本任务**

```bash
git add docker-compose.yml .env.example deploy/production scripts/test-production-compose.sh
git commit -m "feat(deploy): add guarded production compose baseline"
```

## Task 2：实现生产操作脚本和数据库设置防护

**文件：**

- 创建：`scripts/production.sh`
- 修改：`scripts/test-production-compose.sh`
- 修改：`Makefile: help 和 production-* 目标`
- 测试：`scripts/test-production-compose.sh`

**接口：**

- `./scripts/production.sh preflight`：阻止不安全的配置或多 App 实例部署。
- `./scripts/production.sh status`：显示容器、健康接口、有效配置和长时间 processing 文档。
- `./scripts/production.sh backup <directory>`：输出 PostgreSQL 自定义格式备份、文件卷归档和校验清单。
- `./scripts/production.sh deploy`：先执行 preflight，再执行 `docker compose up -d`，不执行 volume 删除。

- [ ] **步骤 1：为 preflight 建立失败用例**

扩展 `scripts/test-production-compose.sh`，调用：

```bash
if ./scripts/production.sh preflight --env-file /tmp/does-not-exist; then
  echo "preflight unexpectedly accepted a missing environment file" >&2
  exit 1
fi
```

预期脚本尚不存在，因此测试失败。

- [ ] **步骤 2：实现统一 Compose 参数解析**

在 `scripts/production.sh` 中使用如下固定初始化，拒绝非生产模板路径，避免误用开发环境：

```bash
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
  test -f "$ENV_FILE" || die "缺少 $ENV_FILE；请从 .env.production.example 创建并填入生产秘密"
  grep -Eq '^WEKNORA_ASYNQ_CONCURRENCY=2$' "$ENV_FILE" || die "WEKNORA_ASYNQ_CONCURRENCY 必须为 2"
  grep -Eq '^CONCURRENCY_POOL_SIZE=1$' "$ENV_FILE" || die "CONCURRENCY_POOL_SIZE 必须为 1"
  grep -Eq '^FRONTEND_BIND=127\.0\.0\.1$' "$ENV_FILE" || die "FRONTEND_BIND 必须绑定到 127.0.0.1"
  grep -Eq '^APP_BIND=127\.0\.0\.1$' "$ENV_FILE" || die "APP_BIND 必须绑定到 127.0.0.1"
  grep -Eq '^DB_BIND=127\.0\.0\.1$' "$ENV_FILE" || die "DB_BIND 必须绑定到 127.0.0.1"
  grep -Eq '^REDIS_BIND=127\.0\.0\.1$' "$ENV_FILE" || die "REDIS_BIND 必须绑定到 127.0.0.1"
}
```

- [ ] **步骤 3：实现 preflight 的容器、渲染和数据库并发检查**

实现 `preflight()`，按顺序执行以下不可省略的检查：

```bash
preflight() {
  require_env_file
  test -f "$OVERRIDE_FILE" || die "缺少生产 Compose 覆盖文件"
  compose config >/dev/null

  local app_count
  app_count="$(compose ps -q app | sed '/^$/d' | wc -l | tr -d ' ')"
  test "$app_count" -le 1 || die "检测到 $app_count 个 app 容器；当前架构只允许一个"

  if compose ps --status running -q postgres >/dev/null 2>&1; then
    local db_concurrency
    db_concurrency="$(compose exec -T postgres \
      psql -U "${DB_USER}" -d "${DB_NAME}" -Atqc \
      "SELECT COALESCE(value::text, '') FROM system_settings WHERE key = 'asynq.concurrency' LIMIT 1" \
      | tr -d '[:space:]')"
    case "$db_concurrency" in
      ''|'2') ;;
      *) die "system_settings.asynq.concurrency=$db_concurrency，会覆盖环境变量 2；请在系统设置中改为 2 后重启 app" ;;
    esac
  fi
}
```

在调用 `compose exec` 前以 `set -a; source "$ENV_FILE"; set +a` 将 `DB_USER`、`DB_NAME` 引入当前脚本。只允许查询，不允许脚本写入 `system_settings`。

- [ ] **步骤 4：实现状态、备份和部署命令**

`status()` 必须输出以下内容：

```bash
compose ps
curl --fail --silent --show-error http://127.0.0.1:"${APP_PORT}"/health
curl --fail --silent --show-error "${OLLAMA_BASE_URL}/api/tags" >/dev/null
compose exec -T postgres psql -U "${DB_USER}" -d "${DB_NAME}" -P pager=off -c \
  "SELECT parse_status, count(*), min(updated_at) AS oldest_update
   FROM knowledges
   WHERE deleted_at IS NULL AND parse_status IN ('pending', 'processing')
   GROUP BY parse_status
   ORDER BY parse_status;"
```

`backup <directory>` 必须要求目标目录不存在或为空，生成：

```bash
compose exec -T postgres pg_dump -U "${DB_USER}" -Fc "${DB_NAME}" >"$backup_dir/postgres.dump"
data_volume="$(docker volume ls -q \
  --filter "label=com.docker.compose.project=${COMPOSE_PROJECT_NAME}" \
  --filter 'label=com.docker.compose.volume=data-files')"
test "$(printf '%s\n' "$data_volume" | sed '/^$/d' | wc -l | tr -d ' ')" = "1"
docker run --rm -v "${data_volume}:/source:ro" \
  -v "$backup_dir:/backup" alpine:3.21 \
  tar -C /source -czf /backup/data-files.tar.gz .
shasum -a 256 "$backup_dir/postgres.dump" "$backup_dir/data-files.tar.gz" >"$backup_dir/SHA256SUMS"
```

若查询结果不是唯一卷，脚本必须失败并打印 `docker volume ls` 的实际结果，不得猜测卷名。`deploy()` 必须先调用 `preflight`，再执行 `compose up -d --remove-orphans`，最后调用 `status`；不得执行 `down` 或 `-v`。

- [ ] **步骤 5：给 Makefile 添加不歧义的生产目标**

在 `.PHONY` 和 `help` 中新增：

```make
production-preflight:
	./scripts/production.sh preflight

production-status:
	./scripts/production.sh status

production-backup:
	@test -n "$(dir)" || (echo "Usage: make production-backup dir=/absolute/backup/path" && exit 1)
	./scripts/production.sh backup "$(dir)"

production-deploy:
	./scripts/production.sh deploy
```

- [ ] **步骤 6：验证脚本语法、失败保护和渲染检查**

运行：

```bash
bash -n scripts/production.sh scripts/test-production-compose.sh
bash scripts/test-production-compose.sh
./scripts/production.sh preflight --env-file /tmp/does-not-exist && exit 1 || true
make production-preflight
```

预期：语法与 Compose 测试通过；缺失环境文件的 preflight 以非零状态拒绝；本机尚未配置真实生产 `.env.production` 时最后一个命令明确拒绝，不会启动或修改任何容器。

- [ ] **步骤 7：提交本任务**

```bash
git add scripts/production.sh scripts/test-production-compose.sh Makefile
git commit -m "feat(ops): add production preflight and backup commands"
```

## Task 3：写入生产部署与任务处置手册

**文件：**

- 修改：`docs/技术安装部署维护操作说明.md`
- 创建：`docs/生产环境配置说明.md`
- 测试：Markdown 链接与命令检查

**接口：**

- 管理员只需按 `docs/生产环境配置说明.md` 从模板创建生产变量文件、执行 preflight、部署、验收、升级与恢复。
- 遇到 `processing` 文档、Ollama 高负载、钉钉附件权限跳过时，按固定证据采集顺序操作，不重复解析已完成文档。

- [ ] **步骤 1：先写文档验证命令**

在 `scripts/test-production-compose.sh` 末尾增加：

```bash
test -f "$ROOT/docs/生产环境配置说明.md"
rg -F 'make production-preflight' "$ROOT/docs/生产环境配置说明.md"
rg -F 'WEKNORA_ASYNQ_CONCURRENCY=2' "$ROOT/docs/生产环境配置说明.md"
rg -F 'docker compose down -v' "$ROOT/docs/生产环境配置说明.md"
```

最后一条必须出现于“禁止命令”说明中，而不是可执行启动步骤。

- [ ] **步骤 2：编写一页式生产配置说明**

创建 `docs/生产环境配置说明.md`，按以下固定章节写作：

1. 适用范围：单机 Compose、内网、一个 App、外部 Ollama。
2. 上线前条件：8 核/16 GB 起步（不含 Ollama 容量）、Docker Compose v2、HTTPS 反向代理、备份目录、内部 DNS。
3. 环境文件：从 `.env.production.example` 创建 `.env.production`，列出必须替换的秘密与不得变更的三项稳定密钥。
4. 并发基线表：Asynq=2、Embedding=1、三项 DocReader=1，并说明数据库系统设置会覆盖环境变量。
5. 首次上线命令：`make production-preflight`、`make production-deploy`、`make production-status`。
6. 反向代理：仅暴露 `127.0.0.1:8088`，由企业既有 Nginx/Caddy/网关终止 TLS；不得将 `5432`、`6379`、`8080`、`50051` 直接暴露公网。
7. 大文档和钉钉运行规则：一个文档同时只能有一个解析；附件权限失败属于跳过；已完成文件不重新切片。
8. 卡住处置：先执行 status、收集 App/DocReader/Ollama/Redis 证据、通过应用任务取消路径停止、确认状态终止后才允许一次重试。
9. 备份、升级、回滚：执行 backup，记录镜像 tag 和配置 hash，等待队列排空，部署后验证一份小文档与两份大钉钉表；禁止 `down -v`、Redis flush、删除 volume。
10. 未来扩容边界：在 API/Worker 拆分和幂等性验证完成前，禁止把 App 扩为多副本，也禁止照搬 Helm 的 `app.replicaCount: 3` 示例。

- [ ] **步骤 3：补强现有技术安装维护文档**

在 `docs/技术安装部署维护操作说明.md` 中：

- 在“部署前准备”后加入到新生产手册的链接，并说明默认 `docker compose up -d` 是功能启动说明，不是本项目的生产批准配置。
- 将“异步任务排查顺序”扩展为：确认单 App、确认有效并发、检查 processing 文档年龄、检查 Redis 队列、检查 DocReader、检查 Ollama，再决定取消或一次重试。
- 在“升级维护”前增加“队列排空与升级窗口”小节，写入本计划的八步部署规则。
- 在“备份与恢复”中明确 PostgreSQL/向量索引与 data-files 必须同批备份；已完成文档恢复后验证检索，不进行重新解析。
- 在“运维检查清单”中增加：App 容器数必须为 1、`asynq.concurrency` 必须为 2 或空、Ollama 延迟/CPU、最长 processing 文档年龄、最近一次备份校验结果。

- [ ] **步骤 4：运行文档与脚本检查**

运行：

```bash
bash scripts/test-production-compose.sh
git diff --check
```

预期：两条命令均通过且无输出。

- [ ] **步骤 5：提交本任务**

```bash
git add docs/技术安装部署维护操作说明.md docs/生产环境配置说明.md scripts/test-production-compose.sh
git commit -m "docs(ops): document safe production operations"
```

## Task 4：执行隔离验证与生产准入演练

**文件：**

- 修改：`docs/生产环境配置说明.md: 验收记录模板`
- 测试：渲染测试、干净临时 Compose 项目、数据库恢复演练

**接口：**

- 验收输出是一份不含秘密的部署记录，包含镜像 tag、配置 hash、并发值、备份校验和、两份大文档的最终状态与耗时。

- [ ] **步骤 1：建立部署验收记录模板**

在 `docs/生产环境配置说明.md` 末尾加入如下模板，真实环境中复制到受控运维记录，而不是提交包含秘密的数据：

```markdown
| 项目 | 记录 |
|---|---|
| 镜像 tag / Git commit | |
| `WEKNORA_ASYNQ_CONCURRENCY`（环境/数据库） | `2` / |
| `CONCURRENCY_POOL_SIZE` | `1` |
| App 容器数量 | `1` |
| PostgreSQL + data-files 备份 SHA256 | |
| Ollama 模型与地址 | |
| 30000 行边界表状态/耗时 | |
| 超过 30000 行分段表状态/耗时 | |
| 重启后检索验证 | |
| 执行人 / 审核人 / 时间 | |
```

- [ ] **步骤 2：在不含真实秘密的环境执行静态准入测试**

运行：

```bash
bash scripts/test-production-compose.sh
git diff --check
```

预期：两个命令均成功且不启动容器。`scripts/test-production-compose.sh`
会在临时环境文件中将 `WEKNORA_ENV_FILE` 指向可提交的示例文件后再渲染
Compose；不能直接对示例文件执行 Compose，因为生产模板必须让真实部署中的
App 容器读取未提交的 `.env.production`。

- [ ] **步骤 3：在隔离测试主机完成运行准入**

使用真实但非生产的 `.env.production` 副本执行：

```bash
make production-preflight
make production-deploy
make production-status
make production-backup dir=/absolute/path/to/backup-test
```

预期：一个 App 容器、健康接口成功、Ollama 成功、备份产生 `postgres.dump`、`data-files.tar.gz`、`SHA256SUMS`。

- [ ] **步骤 4：验证任务并发与数据恢复**

在隔离环境中依次执行而非并发执行：

1. 同步一个钉钉在线文档；确认完成。
2. 解析 `在线表格-大表30000边界内.md`；记录 `completed` 与耗时。
3. 解析 `在线表格-超过30000分段读取.md`；记录 `completed` 与耗时。
4. 重启 `app`，确认两份文档仍可检索且不增加 chunks。
5. 从 Task 3 的备份恢复到新的隔离数据库和 data-files volume，确认两份已完成文档仍可检索，且没有触发重新解析。

预期：没有同一 document ID 的并发 processing attempt；Ollama 的请求串行化；恢复后 chunk 数与恢复前一致。

- [ ] **步骤 5：提交验收模板变更**

```bash
git add docs/生产环境配置说明.md
git commit -m "test(ops): add production acceptance record"
```

## 自检

- 规格覆盖：任务 1 给出安全配置载体，任务 2 给出可执行保护，任务 3 给出管理员流程，任务 4 验证配置、长文档和恢复；第二阶段只记录边界，未混入本次实现。
- 占位符扫描：本计划没有未完成占位步骤；秘密占位符仅存在于提交到 Git 的示例文件，并明确不能投入生产。
- 一致性：所有脚本都以 `deploy/production/.env.production` 与 `deploy/production/docker-compose.production.yml` 为唯一生产入口；并发值统一为 Asynq `2`、Embedding `1`、DocReader `1`。

## 执行交接

计划已保存到 `docs/superpowers/plans/2026-07-13-production-compose-baseline.md`。建议按任务逐项执行：先建立安全生产配置与 preflight，再写运维手册，最后只在隔离环境做大文档和恢复演练。第二阶段 API/Worker 拆分必须另立设计与实施计划。
