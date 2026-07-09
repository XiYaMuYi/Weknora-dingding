# 钉钉数据源同步问题汇总 & 需求文档

> 创建时间：2026-07-07
> 最后更新：2026-07-07
> 状态：已完成主要修复，待在真实钉钉租户验证 Wiki 正文读取权限与正文格式兼容性

---

## 一、Bug 列表

### Bug 1. UnionID 不回显 🔴 P0 ✅ 已修复

**现象**：编辑数据源时，已配置的 `union_id` 显示为空（placeholder 显示"请输入"）

**原因**：前端编辑数据源表单没有从后端返回的配置中读取并回显 `union_id`

**修复方向**：
- 前端 `DataSourceForm` 组件在编辑模式下，正确回显已保存的凭证信息
- `union_id` 应该显示为脱敏值或原值

---

### Bug 2. 空间 ID 配置缺失 🔴 P0 ✅ 已修复

**现象**：
- 前端没有配置空间 ID 的入口
- `resource_ids` 中的 `space:29148929920` 看起来是 userId 而非真正的空间 ID

**根因分析**：
- `29148929920` 是用户的钉钉 userId，不是云盘空间 ID
- 后端没有调用钉钉 API 获取可用空间列表，而是错误地使用了 userId 作为 spaceId

**修复方向**：
- 前端增加空间/目录选择功能（调用后端 API 获取可用的云盘空间和知识库列表）
- 用户应能从列表中勾选要同步的空间或知识库

---

### Bug 3. 凭证编辑逻辑 🟡 P1 ✅ 已修复

**现象**：编辑数据源时，AppKey/App Secret 显示为加密值或空

**修复方向**：
- 已配置的凭证显示为 `••••••••`（脱敏）
- 只有用户主动修改时才更新
- 不修改时保持原值

---

### Bug 4. 前端配置数据不回显 🟡 P1 ✅ 已修复

**现象**：编辑数据源时，所有配置项（包括 resource_ids、settings 等）都不会回显

**修复方向**：
- 编辑模式下，前端需要从后端返回的 data source 对象中读取所有配置字段
- 包括 `config.resource_ids`、`config.settings`、`credentials` 等
- 表单组件需要正确绑定这些字段的初始值

---

## 二、核心需求：同时支持云盘和知识库两种数据源类型

### 背景

钉钉有两套独立的文档系统，API 完全不互通：

| 系统 | API 基础路径 | 空间 ID 格式示例 | 说明 |
|------|-------------|-----------------|------|
| **云盘（Drive）** | `/v1.0/drive/spaces`、`/v1.0/storage/spaces/{spaceId}/dentries` | 数字型，如 `29148929920` | 类似网盘，文件夹+文件结构 |
| **知识库（Wiki）** | 知识库专属 API | 字母数字混合，如 `3YxXA9P7xE27EXNy` | 企业知识管理，树形结构 |

**当前问题**：WeKnora 代码只调用了 Storage API（`/v1.0/storage/spaces/{spaceId}/dentries`），用户配置的 `3YxXA9P7xE27EXNy` 是知识库 ID，传入 Storage API 后钉钉返回 HTTP 500。

### 需求详述

#### 2.1 后端需求

**数据源类型字段**：
- 在 data source 的 `config` 中增加 `dingtalk_type` 字段，值为 `"drive"` 或 `"wiki"`
- 根据类型调用不同的钉钉 API
- ✅ 已实现：字段存储在 `config.settings.dingtalk_type`

**云盘（Drive）Connector**：
- 现有代码基本可用，需要修复空间 ID 获取逻辑
- 调用 `GET /v1.0/drive/spaces` 获取空间列表
- 调用 `GET /v1.0/storage/spaces/{spaceId}/dentries` 获取文件列表
- 所需权限：`Drive.Space.Read`、`Storage.File.Read`
- ✅ 已实现

**知识库（Wiki）Connector**：
- 需要新增知识库 API 的调用逻辑
- 调用知识库相关 API 获取知识库列表和文档列表
- 所需权限：待确认（参考钉钉开放平台文档）
- API 端点需要调研钉钉知识库 API 文档
- ✅ 已实现列表和节点遍历：`GET /v2.0/wiki/workspaces`、`GET /v2.0/wiki/nodes`
- ✅ 已实现正文读取：知识库文档使用 `GET /v1.0/doc/suites/documents/{docKey}/blocks`
- ⚠️ 正文读取仍需在真实租户验证权限；通常需要知识库/节点读取权限，以及钉钉文档块读取权限

**空间/知识库浏览 API**：
- 新增后端接口 `GET /api/v1/datasource/dingtalk/spaces` — 列出可用的云盘空间和知识库
- 新增后端接口 `GET /api/v1/datasource/dingtalk/dentries` — 列出指定空间/知识库下的文档目录
- 这些接口供前端在选择同步目录时调用
- ✅ 已通过现有通用接口实现：`GET /api/v1/datasource/{id}/resources?parent_id=...`

#### 2.2 前端需求

**数据源配置表单**：
- 新增「钉钉文档类型」选择：云盘 / 知识库
- 新增「选择同步目录」功能：
  - 调用后端 API 获取可用的空间/知识库列表
  - 用户勾选要同步的空间或知识库
  - 支持选择到具体的文件夹/目录级别
- 已配置的空间/目录需要正确回显
- ✅ 已实现，复用现有资源树选择器

**编辑回显**：
- 编辑数据源时，所有已配置项需要回显：
  - 钉钉文档类型（云盘/知识库）
  - 已选择的空间/知识库/目录
  - UnionID（作为非密配置原值回显）
  - AppKey/AppSecret（脱敏显示为 `••••••••`）
  - 同步设置（导出格式、是否包含子文件夹等）

---

## 三、修复优先级

| 优先级 | 项目 | 类型 | 状态 |
|--------|------|------|------|
| P0 | UnionID 不回显 | Bug | 已修复 |
| P0 | 空间 ID 配置缺失 | Bug | 已修复 |
| P0 | 支持云盘 + 知识库两种类型 | 需求 | 已开发 |
| P0 | 前端空间/目录选择功能 | 需求 | 已开发 |
| P1 | 凭证编辑逻辑（脱敏回显） | Bug | 已修复 |
| P1 | 前端配置数据不回显 | Bug | 已修复 |
| P1 | 后端空间/知识库浏览 API | 需求 | 已通过通用 resources API 实现 |
| P2 | 多知识库 + 多空间支持 | 需求 | 待规划 |

---

## 四、关键文件路径

### 后端
- 钉钉 Connector 主逻辑：`internal/datasource/connector/dingtalk/connector.go`
- 钉钉 API Client：`internal/datasource/connector/dingtalk/client.go`
- 钉钉类型定义：`internal/datasource/connector/dingtalk/types.go`
- 钉钉配置：`internal/datasource/connector/dingtalk/config.go`
- 数据源 Service：`internal/service/datasource_service.go`
- 数据源 Handler：`internal/handler/datasource.go`
- 路由定义：`internal/router/sync_task.go`

### 前端
- 数据源相关组件：待确认具体路径（在 frontend 目录下搜索 `datasource` 或 `DataSource`）

### 数据库
- 数据源表：`data_sources`
- 配置字段：`config` (jsonb)，包含 `resource_ids`、`settings` 等
- 当前数据源 ID：`4032928e-89e2-443d-ab11-63de450a6263`
- 关联知识库 ID：`5953a2f1-8c12-4488-b3f4-cb5d21dcd6c2`

---

## 五、当前临时状态

- 数据库已手动更新空间 ID 为 `space:3YxXA9P7xE27EXNy`（知识库 ID）
- 但由于代码只支持 Storage API，该 ID 不兼容，同步仍然报错
- 同步游标已清空，等待代码修复后重新测试
- 需要完成云盘+知识库双类型支持后，才能正常同步

---

## 六、钉钉 API 权限清单

已开通：
- ✅ `Drive.Space.Read` — 列出云盘空间
- ✅ `Storage.File.Read` — 列出空间内文件

可能需要开通（知识库）：
- ❓ 知识库列表 API 权限
- ❓ 知识库文档读取 API 权限

---

## 七、验收标准

1. 用户可以在前端选择「云盘」或「知识库」类型
2. 选择类型后，前端展示对应的空间/知识库列表供用户选择
3. 用户可以勾选要同步的空间/知识库/目录
4. 编辑数据源时，所有配置项正确回显
5. 云盘类型同步正常工作
6. 知识库类型使用文档块接口读取正文，不再复用云盘下载接口
7. 增量同步、冲突策略等现有功能不受影响

---

## 八、本次产品化交互修复

- 手动 ID 只作为列表不可用时的兜底入口，不再自动从已保存 `resource_ids` 回填，避免旧错误 ID 被隐藏带回。
- 切换「云盘 / 知识库」会清空旧选择，防止两种资源 ID 混用。
- 保存时按当前类型过滤资源：知识库只保存 `wiki:<ID>`，云盘只保存 `space:<ID>`。
- 手动 ID 会校验明显的类型错误和 URL 输入，提示配置人应该填写 workspaceId 或 spaceId。
- 后端将钉钉 400/401/403/404 等错误转换为中文操作提示，方便判断是凭证、权限、ID 类型还是接口路径问题。
