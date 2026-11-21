# Argus 验收清单（交付口径）

> 首次部署与升级后按序勾选；失败项对照 [docs/ops/runbook.md](../ops/runbook.md)。
> 能力边界见 [CAPABILITY-MATRIX.md](CAPABILITY-MATRIX.md)。

## 1. 启动与探针

- [ ] `GET /healthz` 200
- [ ] `GET /readyz` 200（db + llm 检查通过；未配 LLM 时按部署形态）
- [ ] `GET /version` 返回版本元数据
- [ ] `GET /metrics` 可抓（多副本下默认鉴权，见 ARGUS_METRICS_AUTH）
- [ ] 启动日志有 `platform_ready` / `plugin_registered` / `http_ready`

## 2. 鉴权（fail-closed）

- [ ] 未配任何 token 时 `/tasks` 503（或仅会话可用）
- [ ] `POST /auth/login` 用引导 admin 可登录，拿到会话 token
- [ ] `GET /auth/me` 返回角色与 `permissions` 投影
- [ ] 低档 token 调 `/controlplane/*` 或 `/merges/*` → 403
- [ ] `POST /auth/invites` 不能授予 `admin` 角色 → 400

## 3. 审查主链

- [ ] `POST /tasks` 对测试 PR 建任务，状态至 done
- [ ] PR 上有总结评论（原地更新锚点存在）
- [ ] 行内意见带 fp 短码
- [ ] webhook 重发同 delivery 不重复建任务
- [ ] push 新 head 后旧 head 报告不被覆盖（staleness）

## 4. 修复审批（T3）

- [ ] 含 critical/high 的审查完成后 `GET /fix-approvals` 能列出 pending 票
- [ ] 未批准时 `@argus fix` 被审批门拒绝并回帖原因
- [ ] `POST /fix-approvals/decide` approved 后同一 fp 可修复
- [ ] reject 后再 fix 仍拒绝
- [ ] 新 head 后旧 pending 票为 stale

## 5. 修复推送 confirm（T4）

- [ ] `ARGUS_FIX_PUSH=confirm` 时验证+审批通过后任务 `fix_status=await_confirm`
- [ ] `GET /fix-push/preview` 显示 ready 与待审批数
- [ ] `POST /fix-push/confirm` 错误 head → rejected
- [ ] 正确 head → 推送成功（branch 形态开修复 PR）

## 6. 合并资格门（T2）

- [ ] `GET /merges/eligibility` 七条 checks 全量透出
- [ ] 无 CI / CI 未通过 / 无当前 head 审查 → eligible=false
- [ ] 全绿时 `POST /merges/preview` 返回 op_id + expected_remote_head
- [ ] `POST /merges/confirm` 绑定错误 head → rejected
- [ ] 绑定正确 → merged（或平台保护拒绝时如实 failed）
- [ ] uncertain 后 `POST /merges/reconcile` 按平台状态定案

## 7. 多用户（T5）

- [ ] 首管引导：`ARGUS_BOOTSTRAP_ADMIN_USER/PASS` 仅在空用户表时生效
- [ ] admin 邀请 maintainer → 接受 → 登录 scope=admin
- [ ] viewer 登录 scope=read，调 POST /tasks → 403
- [ ] logout 后原 token 失效

## 8. 观测与运维

- [ ] `GET /stats/overview` 有数据
- [ ] `GET /usage/monthly` 有 token 计量
- [ ] runbook 场景可按文档复现（running 卡住 / 任务复位）

## 9. 交付物

- [ ] `docs/delivery/CAPABILITY-MATRIX.md` 与部署能力一致
- [ ] secret-scan workflow 绿（若启用）
- [ ] `make lint` / `make arch` / `make test` 本地绿
