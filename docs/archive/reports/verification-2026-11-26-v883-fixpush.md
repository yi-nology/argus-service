# 验证报告：修复推送环端到端（v8.8.3 / 2026-11-26）

> 环境：43（k3s ARM64），后端 `v8.8.3`（buildCommit `ed3772f`），前端 `v8.8.3`。
> 结论：**preview→审批→await_confirm→READY 全链实证通过**；confirm（真实写远端）
> 由维护者执行，结果回填文末「confirm 执行记录」。

## 1. 配置（43 secret argus-env）

| 键 | 值 | 说明 |
|---|---|---|
| `ARGUS_FIX_PUSH` | `confirm` | 两阶段：验证+审批通过落 `await_confirm`，不自动推；`POST /fix-push/confirm` 绑定 `expected_remote_head` 后放行 |
| `ARGUS_FIX_VERIFY_CMD` | `/app/argus fixcheck {dir}` | 内置语法验证器（v8.8.3 起随二进制发布） |

未使用 `true` 之类空验证命令（那会把未验证修复标成 ✅，安全红线）。

## 2. fixcheck 验证器

- 单测 6 枚全绿（Go/JSON/YAML/Shell/Python/Node + 空改动/不可检查 fail-closed）
- 43 pod 内实弹：
  - 合法 `ok.go` → `rc=0`
  - 语法错误 `bad.go` → `rc=1`（`expected ')'`）
  - 错误 `bad.yaml` → `rc=1`
- fail-closed：改动文件全都不可检查（如 `.vue`/`.java`）→ 退出 1，**不产生** verified
- arch 门禁：子进程统一走 `internal/execx` 唯一出口；`ext:gopkg.in/yaml.v3` 已在
  architecture-policy cli 模块登记

## 3. 真实任务端到端

| 项 | 值 |
|---|---|
| 任务 | `t-b6fbe98d25d94c4411260316`（`gitlab/obs/jianmu#63`，需求复核） |
| head | `27cd4b9fc417334a651e526bfe8b48a9a04940b7` |
| 验证 | staged `fix_push_findings`：`verified=1`，finding `internal/service/activation/activation.go:1108`（medium）经 fixcheck Go 语法校验通过 |
| 状态机 | `fix_status=await_confirm`（2026-11-26 03:29:51Z，`stageFixPushConfirm` 落库） |
| preview | `ready=true`、`pending_approvals=0`、`mode=confirm`、`message=就绪：可 POST /fix-push/confirm（绑定 expected_remote_head=27cd4b9f…）` |
| UI | `/fix-push?…repo=jianmu&number=63` 五项检查全绿（任务存在/head 已绑定/推送模式 confirm/高危审批齐备/后端判定就绪），显示「可推送」+ 确认按钮 |

对照组：`gitlab/obs/ops/deploy-kit#63`（无 suggested_fix）preview 正确显示
`ready=false / 任务未进入修复推送链`——候选产出不是全局放行，只对真实通过验证的任务成立。

## 4. 边界与口径

- 验证是**语法级**（Go/JSON/YAML/Shell；镜像有解释器时 .py/.js），不是编译/单测级；
  要更深验证把 `ARGUS_FIX_VERIFY_CMD` 换成仓库自己的包装命令即可
- confirm 时刻仍 fail-closed 复查：状态机（仅 `await_confirm`）、head 绑定、远端
  head TOCTOU 复核、高危审批票全通过、副作用闸 at-most-once
- 防循环护栏：被审 PR 源分支为 `argus/fix/*`（本环产物）时不进推送环

## confirm 执行记录

（待执行：43 VPN 恢复后由守望脚本自动 `POST /fix-push/confirm`，回填
response/task 终态/`fix.push`·`fix.cr` 审计日志/新建 CR 链接。）