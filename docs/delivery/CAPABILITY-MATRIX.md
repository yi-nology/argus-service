# Argus 能力矩阵（交付口径）

> 本文描述**当前已实现且有代码/测试背书**的能力与真实依赖；开发中能力单独标注。
> 与 MergePilot 能力矩阵同构：每条给出状态、依赖、边界（诚实声明不支持项）。
> 运行时快照另见 `GET /get_capabilities`（MCP）与 `GET /version`。

依赖符号：env=环境变量；DB=数据库表（AutoMigrate）；平台凭据=PAT/GitHub App；模型=LLM 端点；执行器=工作副本/沙箱命令。

## 1. 主链能力一览

| # | 能力 | 状态 | 依赖 | 说明 |
|---|---|---|---|---|
| 1 | PR 事件接入（webhook） | ✅ | 平台 secret + 公网入口（或 poller 兜底） | 七平台签名校验、delivery 幂等 |
| 2 | 轮询兜底 | ✅ | env `ARGUS_POLL_REPOS` | NAT/VPN 下 webhook 不可达时同语义发现 |
| 3 | TaskAPI 手动审查 | ✅ | 🔒 Bearer | `POST /tasks` 任意已有 PR |
| 4 | 需求复核 R1–R5 | ✅ | 模型 | AC 逐条 `file:line` 证据；无证据降级 unverifiable |
| 5 | 多专家并发审查（插件） | ✅ | plugins.yaml + 模型/CLI | builtin/cli/acpx/remote；失败降级注明 |
| 6 | 确定性裁决真值表 | ✅ | 仅 DB | R5.1–R5.5 零 LLM |
| 7 | 行内评论 + 总结报告 | ✅ | 平台写凭据 | CreateReview 三级降级；总结原地更新 |
| 8 | 增量审查 | ✅ | DB | 未变文件跳过 |
| 9 | 确定性 SAST 前置 | 可选 | env `ARGUS_SAST_*` 二进制路径 | gitleaks/semgrep，零 LLM 直进合并 |
| 10 | 修复建议 suggested_fix | ✅ | 模型 | 报告附建议，不自动写远端 |
| 11 | 修复命令 `@argus fix` | 可选 | `ARGUS_FIX_AGENT` + WC | 沙箱 agent 出 patch + `FixVerifyCmd` |
| 12 | 修复推送环 | 可选 | `ARGUS_FIX_PUSH` + FixVerifyCmd | branch/direct/confirm 三形态 |
| 13 | **高危修复审批票** | ✅ | DB `fix_approval_tickets` | critical/high 按 (PR,fp,head) 建票；fix/推送环 fail-closed |
| 14 | **修复推送 preview→confirm** | ✅ | `ARGUS_FIX_PUSH=confirm` | await_confirm 暂存 + 绑定 head 放行 |
| 15 | **合并资格门** | ✅ | 平台读+写凭据 | 七条 fail-closed；无 CI/读失败=阻断 |
| 16 | **受控合并 preview→confirm** | ✅ | admin token + `MergeCR` 能力 | 绑定 expected_remote_head；副作用闸 at-most-once；uncertain 对账 |
| 17 | 多用户登录/邀请 | ✅ | DB users/invites/sessions | 角色→scope；邀请永不授予 admin |
| 18 | my_permissions 投影 | ✅ | 会话/静态 token | `GET /auth/me` |
| 19 | 误报抑制 + 知识回流 | ✅ | DB；可选 Milvus | fp 短码；suppress/restore |
| 20 | 影响面 / codegraph / 语义检索 | 可选 | `ARGUS_CODESEARCH`/`ARGUS_SEMSEARCH` 等 | 语言矩阵见 README §5.5 |
| 21 | 审查评测 golden 回归 | ✅ | `make eval` | 48+ 夹具；eval-real 挂 `/metrics` |
| 22 | MCP 24 工具 | ✅ | Bearer | `/mcp` streamable http |
| 23 | 多租户 | ✅ | DB `tenant_id` | 凭证/缓存/路径分桶；webhook 坐标归租户 |

## 2. 角色与权限（T5 最小集）

| 角色 | scope | decide/merge/fix | manage_membership | 邀请可授予 |
|---|---|---|---|---|
| viewer | read | — | — | ✓ |
| auditor | read | — | — | ✓ |
| operator | ci | — | — | ✓ |
| maintainer | admin | ✓ | — | ✓ |
| admin | admin | ✓ | ✓ | **✗（仅部署者引导）** |

静态 `ARGUS_APITOKEN`/`ARGUS_API_TOKENS` 仍为 read/ci/admin 三档，与会话并存。

## 3. 明确边界（如实声明）

- **CI 双面**：GitHub 合并门读 **Commit Statuses + Check Runs** 双面取最坏（T16 补齐，Actions-only 不再误阻断）；其余平台以 Commit Statuses 为准（Gitee 适配层已折 check runs 进 statuses）。
- **无 CI 的仓库不能经门合并**：有意设计（与「审查完成 ≠ 可合并」一致）。
- **OAuth 登录（GitHub/Gitee）**：未实现；当前为用户名密码会话 + 邀请制。SSO/SCIM 不在范围。
- **非递增五角色**：maintainer 与 admin 在裁定面同 scope；平台管理与业务裁定的物理分离（admin 无 merge）留后续迭代。
- **PostgreSQL RLS / HA 配额**：无；多租户在应用层与复合主键。
- **容器回滚不能撤销已发生的远端 commit/merge**：远端回退只能平台 revert。

## 4. 事实源（代码出处）

| 条目 | 代码 |
|---|---|
| 合并资格门 | `internal/service/service_impl_merge.go` |
| 合并 preview/confirm | `internal/service/service_impl_merge_api.go` |
| 修复审批票 | `internal/storage/fix_approval.go`、`internal/service/service_fix_approval.go` |
| 修复推送 confirm | `internal/runner/runner_fixpush_confirm.go`、`internal/service/service_fix_push.go` |
| 会话/邀请 | `internal/storage/auth_user.go`、`internal/service/service_auth_user.go` |
| 副作用闸 | `internal/effect/` |
| 裁决真值表 | `internal/workflow/requirement/verdict.go` |
