# API 兼容性声明

> 状态：**Stable**（本文件本身描述的契约面）
> 适用版本：Argus `v7.x` 起；版本号遵循 [SemVer 2.0](https://semver.org/lang/zh-CN/)。
> 维护位置：`docs/API-COMPAT.md`。修改对外契约时，本文件与 `CHANGELOG.md` 必须同批更新。

本文件回答两个问题：**哪些接口算对外契约**、**怎么改不算破坏**。
「怎么用」分别见 [README.md](../README.md)、
[skills/argus-mcp/references/tools.md](../skills/argus-mcp/references/tools.md) 与
[skills/argus-mcp/SKILL.md](../skills/argus-mcp/SKILL.md)；本文件只定义稳定性与变更纪律。

## 1. 适用面

### 1.1 MCP 工具（24 个，`/mcp`）

以下 24 个常驻注册工具属于对外契约：

```text
review_pr            requirement_compliance  review_diff          review_diff_async
list_agents          get_report              list_tasks           get_task_stages
list_findings        get_suggestion          ask_argus            search_code
search_knowledge     search_sem              get_trace            get_symbol_context
get_capabilities     suppress_finding        restore_finding      get_repo_config
get_repo_map         get_impact              search_findings      fix_finding
```

稳定面包括：**工具名、必填参数名与类型、结构化返回字段名与语义**。
工具描述文案、可选参数默认值、错误提示文本可随 MINOR 调整（仍应记入 `CHANGELOG.md`）。

### 1.2 HTTP API

| 端点 | 方法 | 鉴权 | 说明 |
|---|---|---|---|
| `/tasks`、`/tasks/:taskId`、`/tasks/:taskId/impact` | GET / POST | 🔒 Bearer | 发起审查、任务列表、任务详情与影响面 |
| `/agents` | GET | 🔒 Bearer | Agent/插件清单与按仓配置生效视图 |
| `/console` | GET | 🔒 Bearer | 质量控制台聚合数据面 |
| `/events` | GET | 🔒 Bearer | SSE 任务进度事件流 |
| `/mcp` | GET / POST / DELETE | 🔒 Bearer | MCP streamable HTTP（JSON-RPC） |
| `/healthz` | GET | 无 | 存活探针 |
| `/readyz` | GET | 无 | 就绪探针（依赖 db/LLM 检查） |
| `/version` | GET | 无 | 版本元数据 |

`/healthz`、`/readyz`、`/version` 统一返回 `{code,data,message,trace_id}` 信封。
其余辅助端点（`/findings`、`/reports`、`/repo-settings`、`/repos/map`、`/stats`、
`/usage`、`/webhook/:platform`）默认按 **Stable** 管理，除非对应版本说明显式标注
Experimental；破坏性变更同样走第 3 节流程。

### 1.3 报告 / 评论 JSON 契约

**任务结果（`GET /tasks/:taskId`）**核心字段：

- `taskId`、`status`、`verdict`、`findings`、`report`、`failReason`、`debug`（可选）。

**词表（稳定，不得在 MINOR 内扩展语义）**：

- `status`：`pending` | `running` | `done` | `failed` | `stale`
- `verdict`：`approve` | `comment` | `request_changes`（可空：未裁决）
- `severity`：`critical` | `high` | `medium` | `low`
- `evidence_status`：`invalid` | `unverified` | `structurally_valid` | `verified`

**Finding 结构化字段**：`file`、`line`、`severity`、`comment`、`by`、`fp`（8 位十六进制短码）、
`suggested_fix`、`evidence`、`evidence_status`。消费方应把 `fp` 当作稳定身份键，
不要解析 markdown 报告来获取意见。

**评论锚点（机器定位用，格式稳定）**：

```text
<!-- argus:report task_id=<id> head=<sha> ver=<version> -->
<!-- argus:review task_id=<id> head=<sha> verdict=<verdict> -->
<!-- argus:inline by=<agent> -->
@argus fix fp:<8-hex>
```

## 2. 稳定性分级

| 级别 | 承诺 | 变更规则 |
|---|---|---|
| **Stable** | 生产依赖可长期使用；名称/路径/必填参数/字段语义不变 | 破坏性变更只能走第 3 节：先文档 + 兼容层/弃用期，再在下一个 MAJOR 删除 |
| **Experimental** | 供试用与快速迭代，不承诺跨 MINOR 兼容 | 可在 MINOR 内变更或删除；必须记入 `CHANGELOG.md`，并尽量给替代路径 |
| **Deprecated** | 仍可用但已计划移除 | 文档与 `get_capabilities`/响应告警中标注 since/removal 版本；至少保留一个 MINOR（且不短于 30 天）后才可删除 |

分级判定：

- 第 1 节的 MCP 工具、HTTP 端点、报告/评论 JSON 契约默认 **Stable**。
- 新增能力优先以 **Experimental** 发布，稳定后再升级为 Stable（升级本身不构成破坏）。
- 已标记 Deprecated 的接口按弃用期执行，不得静默删除。

## 3. 破坏性变更流程

破坏性变更指：删除/重命名 Stable 工具或端点；修改必填参数；修改字段类型或语义；
删除响应字段；变更 `status`/`verdict`/`severity`/`evidence_status` 词表；修改评论锚点格式。

必须按顺序执行：

1. **先文档**：提 issue 说明动机与迁移路径，同批更新本文件与 `CHANGELOG.md`，
   标注 since/removal 版本。
2. **兼容层 / 弃用期**：新旧形态并行——旧工具名/路径/字段继续工作并标记 Deprecated；
   需要时在服务端加适配层或双写。弃用期至少一个 MINOR 版本且不短于 30 天。
3. **再删除**：仅允许在下一个 MAJOR 版本移除；发布说明给出迁移示例。
4. **验证**：删除后跑 `make verify`（含 crossbuild/modcheck/docs-check），
   并更新 `skills/argus-mcp/SKILL.md` 的 `compatibility` 版本区间。

**argus 版本语义**：MAJOR = 破坏性变更；MINOR = 向后兼容的新增（含 Experimental 面）；
PATCH = 兼容修复。对外技能包（`skills/argus-*`）的 `compatibility` 区间必须与契约变更同批调整。

## 4. 契约单源关系

| 文件 | 角色 | 变更要求 |
|---|---|---|
| [README.md](../README.md) | 使用者入口：HTTP 端点与 MCP 工具摘要 | 对外面变化时同步 |
| [skills/argus-mcp/references/tools.md](../skills/argus-mcp/references/tools.md) | MCP 参数级参考：字段类型、默认值、错误语义 | 参数变化时同步 |
| [skills/argus-mcp/SKILL.md](../skills/argus-mcp/SKILL.md) | Agent 工作流与 `compatibility` 版本区间 | 契约/兼容区间变化时同步 |
| `docs/API-COMPAT.md`（本文件） | 稳定性分级与破坏性变更流程 | 分级/弃用/移除时同步 |

冲突处理：参数级细节以仓库代码与 IDL（`biz/model/argus/`、`internal/channel/mcpserver/`）
为准，并须同批修正文档；**稳定性判定以本文件为准**。`llms.txt` 的文档入口清单包含本文件，
生成物由 `make docs-check` 守卫。