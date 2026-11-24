# API 兼容性声明

> 状态：**Stable**（本文件本身描述的契约面）
> 适用版本：Argus `v7.x` 起；版本号遵循 [SemVer 2.0](https://semver.org/lang/zh-CN/)。
> 维护位置：`docs/guide/API-COMPAT.md`。修改对外契约时，本文件与 `CHANGELOG.md` 必须同批更新。

本文件回答两个问题：**哪些接口算对外契约**、**怎么改不算破坏**。
「怎么用」分别见 [README.md](../../README.md)、
[skills/argus-mcp/references/tools.md](../../skills/argus-mcp/references/tools.md) 与
[skills/argus-mcp/SKILL.md](../../skills/argus-mcp/SKILL.md)；本文件只定义稳定性与变更纪律。

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
| `/findings` | GET | 🔒 Bearer | 意见治理台：按仓跨 PR 列 finding_prints（status/limit 过滤，read 档） |
| `/mcp` | GET / POST / DELETE | 🔒 Bearer | MCP streamable HTTP（JSON-RPC） |
| `/healthz` | GET | 无 | 存活探针 |
| `/readyz` | GET | 无 | 就绪探针（依赖 db/LLM 检查） |
| `/version` | GET | 无 | 版本元数据 |
| `/metrics` | GET | 可选 🔒 Bearer | Prometheus 指标（`ARGUS_METRICS_AUTH=1` 时要求 Bearer；默认无鉴权供集群抓取） |
| `/.well-known/agent.json` | GET | 无 | A2A Agent Card 发现文档（协议端点，不经响应信封） |

`/healthz`、`/readyz`、`/version` 统一返回 `{code,data,message,trace_id}` 信封。
`/metrics` 返回 Prometheus 文本格式，`/.well-known/agent.json` 按 A2A 规范返回
顶层字段（二者均跳过统一响应壳）。
其余辅助端点（`/findings`、`/reports`、`/repo-settings`、`/repos/map`、`/stats`、
`/usage`、`/webhook/:platform`）默认按 **Stable** 管理，除非对应版本说明显式标注
Experimental；破坏性变更同样走第 3 节流程。

#### SSE 增量事件 `agent.delta`（Experimental，P3 opt-in）

`/events` 既有事件类型（`task.created` / `task.running` / `task.stage` /
`task.finished` / `comment.replied`）的类型、字段与语义不变；`Event` 新增字段
全部 `omitempty`，旧事件的 JSON 逐字不变。设置 `ARGUS_REACT_STREAM=1` 后，
builtin Reviewer 与 QA 的 ReAct 改为 agentrun 原生流式，增量经 kernel
`delta.Pacer` 节流后以**追加事件类型** `agent.delta` 发布；默认 `0` = 整段
语义（回滚开关）。

| 字段 | 类型 | 说明 |
|---|---|---|
| `type` | string | 恒为 `agent.delta` |
| `task_id` | string | 审查任务 ID；QA 问答为空串 |
| `platform` / `owner` / `repo` / `number` | string | PR 身份（SSE 客户端可按仓/PR 过滤） |
| `agent_phase` | string | `reasoning` \| `text` \| `reset` |
| `agent_iteration` | int | 环节内轮次（从 1 起）；`tool_call` 之后下一条推理/正文事件进入下一轮 |
| `delta` | string | 增量合并片段 / 权威快照全文（`reset` 时缺省） |
| `seq` | int | >0 = 同一 Phase **同一轮**内单调递增的增量序号；缺省/0 = 权威快照 |
| `final` | bool | `true` = 权威快照或收口残尾（消费方以 `delta` 替换该 Phase 同轮累积） |

**语义边界**：

- `seq>0 && !final`：按 `(agent_iteration, agent_phase)` 按序**追加**（分片是合并
  片段，不是累积快照）。
- `final=true && seq==0`：权威快照，消费方用 `delta` **替换**该轮该 Phase 的累积
  （同轮快照到达即吸收增量缓冲；Pacer 只封本轮流，下一轮照常发布）。
- `agent_phase=reset`：整程重试（attempt_retry），消费方清空两路累积并重置轮次；
  时间线上不会出现两段无法解释的近似重复。
- 连接收口（`Close`）会把无权威快照的调用轮正文残尾以 `final=true` 落地。
- **跨实例分发（v7.5.0 #67）**：`agent.delta` 默认与可靠状态事件同管道——本地
  Bus 即时 + Outbox 跨实例 fanout 回灌（双副本下任务在 Pod A 执行、SSE 客户端
  连 Pod B 也能收到增量；fanout 轮询上界 500ms）。delta 行独立 1h 短保留期。
  `ARGUS_DELTA_FANOUT=0` 回滚 live-only（只发本实例本地 Bus，不落 outbox）。
- **reasoning 授权面（v7.5.0 #67）**：`agent_phase=reasoning` 的增量默认只对
  **admin 档** token 下发——`read`/`ci` 连接在 SSE 出口被过滤（普通只读 token
  收不到内部推理）；`ARGUS_DELTA_REASONING=all` 显式放宽（排障）、`off` 收紧
  （任何 token 只见 text/reset）。`text`/`reset` 相位不过此门。
- **能力边界**：状态事件（`task.*`）有 Outbox 兜底（跨实例 at-least-once、
  24h 保留，断连后的最终结果仍以任务终态/报告 API 为准）；`agent.delta` 是
  实时观测面（fanout 开启时跨实例实时、保留 1h、不承诺断连重播）——消费方
  应按 `id` 幂等去重并以 `final=true` 快照为权威。旧消费方忽略未知
  `type` 即可。

#### 控制面（admin，Experimental，#48）

控制面端点全部要求 **admin 档** Bearer token（`ARGUS_APITOKEN` 或
`ARGUS_API_TOKENS` 中 `scope=admin`）；`GET` 也要求 admin（没有只读档）。
权限在路由中间件（`requiredScope` 的 `/controlplane/` 前缀）与 service 层
`ScopeFromContext` 二次判定，未过中间件或档位不足一律 403（fail-closed）。

| 端点 | 方法 | 请求要点 | 响应要点 |
|---|---|---|---|
| `/controlplane/side-effects` | GET | 无 | `effects[]`：`key`/`resource`/`reason`/`retry_safety`（0=safe、1=unknown、2=unsafe）/`updated_at`（Unix 秒）；列出全部 uncertain，不设静置窗 |
| `/controlplane/side-effects/resolve` | POST | `key`（必填 ≤255）、`applied`、`payload?`（applied=true 时的结果 JSON，评论型给 `{"comment_id":N}`）、`force_retryable`、`reason?` | `outcome`（`replayed`/`applied`/`uncertain`）+ `payload?`；`applied=false` 且动作声明 Unsafe 时必须显式 `force_retryable=true`，否则 409；Key 非 uncertain → 409；坏 JSON/空 key → 400 |
| `/controlplane/leases` | GET | 无 | `leases[]`：`resource`/`holder`/`fence`/`expires_at`（Unix 秒）；仅活跃（holder 非空且未过期），按到期升序 |
| `/controlplane/leases/release` | POST | `resource`（必填 ≤255）、`dry_run`、`confirm`、`reason?` | **#64 两段式**：`dry_run=true` 返回 `plan`（受影响 action 清单：key/holder/fence/状态/retry_safety + `would_mark_uncertain` 预估），只读不改任何行、不落审计；正式执行必须 `confirm=true` 且 `reason` 非空（缺一 400），返回 `cleared_actions`/`marked_uncertain`/`audit_id`（持久化审计行 `side_effect_release_audits`，与释放同事务）。资源行 holder/lease_until 清空、fence 保留（令牌代际继续单调）；**Unsafe+在飞的 action 默认转入 `uncertain`**（等待 resolver 自动对账或人工 `force_retryable`，不由租约释放默许重试）——Git Push/开 PR 等第三方 API 不认识 fence，旧执行者在途请求由此兜底；done 不复活、已 uncertain 只清对账占用；资源不存在 → 404 |
| `/controlplane/tasks/cancel` | POST | `task_id`（必填）、`reason?` | `task_id`/`status`（`failed`）/`cancelled`；pending/running → 立即 failed 且 `next_retry_at=NULL`（取消自动重试）；重复终止幂等返回 `cancelled=false`；任务不存在 → 404，done/stale/非操作员终止的 failed → 409 |

**终止语义边界**：cancel 立即失败 + 检查点协作停机（start marker 前、平台上下文
拉取前、LLM 工作流前、回帖/修复验证前）；成功后补发 `task.finished` SSE 事件
（控制面绕过 runner 终态漏斗，不补则 `/events` 客户端看不到结束）。在途 LLM/工具调用**无法强杀**——取消在
下一个检查点生效；检查点之间已经发生的平台写操作由既有 sideeffect 闸/幂等键兜底
（不会重复施加），但不承诺「终止即零副作用」。控制面是数据库直连操作：具备 admin
token 的任一副本均可执行，无 leader 要求。

### 1.3 报告 / 评论 JSON 契约

**任务结果（`GET /tasks/:taskId`）**核心字段：

- `taskId`、`status`、`verdict`、`findings`、`report`、`failReason`、`debug`（可选）。

**词表（稳定，不得在 MINOR 内扩展语义）**：

- `status`：`pending` | `running` | `done` | `failed` | `stale`
- `verdict`：`approve` | `comment` | `request_changes`（可空：未裁决）
- `severity`：`critical` | `high` | `medium` | `low`
- `evidence_status`：`invalid` | `unverified` | `structurally_valid` | `verified`

**`evidence_status` 语义（v7.5 #68 起，词表不变但 `verified` 收紧）**：

- `invalid`：引用根本不可评估（文件不存在/读取失败）；
- `unverified`：有引用但未证实（行号越界、不在改动集/触达集、引用行为空行或
  纯注释行、`evidence_snippet`/`evidence_sha256` 与引用行内容对账失败），或
  未提供结构化证据（纯文字/空）；
- `structurally_valid`：文件+行号结构全对，但**未提供内容对账输入**或版本
  未与任务 head 对齐——「引用可定位」不等于「证据内容属实」；
- `verified`：结构全对 + 检出版本对齐 + **内容对账通过**（模型照抄的
  `evidence_snippet` 或上游给出的 `evidence_sha256` 与引用行归一内容一致）。
  `verified` 不再只说明「引用可定位」。

**Finding 结构化字段**：`file`、`line`、`severity`、`comment`、`by`、`fp`（完整指纹，
32 位十六进制）、`suggested_fix`、`evidence`、`evidence_snippet`（可选，模型照抄
的引用行原文）、`evidence_sha256`（可选，引用行归一内容的 SHA-256）、
`evidence_status`。消费方应把 `fp`
当作稳定身份键——抑制/恢复/单条复核接口接收完整 `fp`，不要解析 markdown 报告来
获取意见。只有报告正文与评论锚点把 `fp` 缩短为前 8 位十六进制；消费方不得用
锚点短码反推或拼接完整指纹。

**评论锚点（机器定位用，格式稳定）**：

```text
<!-- argus:report task_id=<id> head=<sha> ver=<version> -->
<!-- argus:review task_id=<id> head=<sha> verdict=<verdict> -->
<!-- argus:inline by=<agent> -->
@argus fix fp:<8-hex>
```

锚点中的 `fp:<8-hex>` 是完整 32 位十六进制指纹的前 8 位短码。

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
| [README.md](../../README.md) | 使用者入口：HTTP 端点与 MCP 工具摘要 | 对外面变化时同步 |
| [skills/argus-mcp/references/tools.md](../../skills/argus-mcp/references/tools.md) | MCP 参数级参考：字段类型、默认值、错误语义 | 参数变化时同步 |
| [skills/argus-mcp/SKILL.md](../../skills/argus-mcp/SKILL.md) | Agent 工作流与 `compatibility` 版本区间 | 契约/兼容区间变化时同步 |
| `docs/guide/API-COMPAT.md`（本文件） | 稳定性分级与破坏性变更流程 | 分级/弃用/移除时同步 |

冲突处理：参数级细节以仓库代码与 IDL（`biz/model/argus/`、`internal/channel/mcpserver/`）
为准，并须同批修正文档；**稳定性判定以本文件为准**。`llms.txt` 的文档入口清单包含本文件，
生成物由 `make docs-check` 守卫。
