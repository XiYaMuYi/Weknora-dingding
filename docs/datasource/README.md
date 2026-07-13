# 数据源（DataSource）文档

本目录存放 **知识库外部数据源同步** 相关设计、接入说明与连接器专项文档。运行时实现位于 `internal/datasource/`、`internal/application/service/datasource_service.go` 与 `internal/datasource/connector/*`。

## 文档索引

| 文档 | 说明 | 状态 |
|------|------|------|
| [钉钉在线文档同步 — 架构设计](./钉钉在线文档同步-架构设计.md) | 首期仅钉钉文档中心「在线文档（Doc）」；插件式 Connector、增量同步、与知识库解析流水线边界及红线约束 | 已实现（`internal/datasource/connector/dingtalk`） |
| [钉钉知识库和钉盘同步参数获取教程（给企业管理员）](./钉钉知识库和钉盘同步参数获取教程-给企业管理员.md) | 给企业管理员的 AppKey / AppSecret / UnionID / 权限 / workspaceId / spaceId 获取说明，覆盖钉盘、知识库、机器人聊天、AI 卡片和文档写权限；说明如何通过接口查询难找的 ID，以及不会调接口时应提供哪些线索 | 可交付 |
| [钉钉同步参数填写表（Markdown）](./钉钉同步参数填写表-给企业管理员.md) | 管理员填写参数、权限勾选、截图说明和兜底查询线索的 Markdown 表单 | 可交付 |
| [钉钉同步参数填写表（Excel）](./钉钉同步参数填写表-给企业管理员.xlsx) | 管理员填写参数、权限勾选、截图说明和兜底查询线索的 Excel 表单，含常用下拉项 | 可交付 |
| [接入与扩展指南](./接入与扩展指南.md) | 新增 Connector 的通用步骤、`Connector` 接口、注册与测试约定 | 待补充 |

## 与代码的对应关系

| 主题 | 代码路径 |
|------|----------|
| Connector 接口与注册表 | `internal/datasource/connector.go`、`internal/container/container.go` → `initConnectorRegistry` |
| 同步调度与任务 | `internal/datasource/scheduler.go`、`types.TypeDataSourceSync` |
| 同步执行与入库 | `internal/application/service/datasource_service.go` |
| HTTP API | `internal/handler/datasource.go`、`internal/handler/datasource_credentials.go` |
| 已上线连接器示例 | `internal/datasource/connector/feishu/`、`notion/`、`yuque/`、`rss/` |
| 类型与常量 | `internal/types/datasource.go`（含 `ConnectorTypeDingTalk` 等） |

## 相关文档（目录外）

- 产品能力概览：根目录 [README_CN.md](../../README_CN.md)（多源接入 / Feishu·Notion·Yuque 等）
- 集成中心（前端）：`frontend/src/views/integrations/datasource/`
- **钉钉 IM 机器人**（与文档数据源无关）：`internal/infrastructure/channels/dingtalk/`

## 阅读顺序建议

1. 若要**扩展新连接器**：先读（待编写的）接入与扩展指南，再对照 `feishu` 包实现。
2. 若要**开发钉钉在线文档同步**：直接阅读 [钉钉在线文档同步 — 架构设计](./钉钉在线文档同步-架构设计.md)。
