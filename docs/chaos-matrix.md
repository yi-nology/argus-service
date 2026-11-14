# 混沌测试矩阵（P2-01，issue #24）

> 状态：**全量落地**（注入点抽象 + 五族故障可执行矩阵 + 报告输出 +
> **容器级 testcontainers pg/MySQL 五探针**，批 C 2026-11-15 收官）。
> 关联：拆自 [Meta] #21 的 P2-01；fencing 基建见 #6（迁移/任务租约）、
> fail-closed 迁移见 v6.7.0。

## 定位

把「任务中途死亡要能恢复、依赖不可达要有界失败、限流要退避、租约失守要
fencing」从口头约定变成可执行契约。三层形态（与 issue #24 建议分层对齐）：

| 层 | 载体 | CI |
|---|---|---|
| 注入点抽象 + 规范矩阵 | `internal/chaos`（L0 零 internal 依赖） | 在线链路不 import（旁路形态，同 obs evalmetrics） |
| 单测级实弹回归 | `test/chaos`（sqlite 真存储 + 打桩 LLM/Git 面） | `make chaos`（`go test ./...` 全量自动含） |
| 容器级实弹 | `test/chaos/container_test.go` + `internal/runner/budget_container_test.go`（testcontainers 真 pg/MySQL）；43 环境 k3s Pod Kill 手动 job | CI `chaos-container` job（`make chaos` 单测级 + `make chaos-container` 容器级；本地独立入口不进 `make test`） |

## 故障族词表与规范矩阵

五族（`chaos.Family`）× 注入点（`chaos.Matrix()` 为机器可读单源）：

| 故障族 | 故障（Kind） | 注入点 | 期望恢复语义 |
|---|---|---|---|
| process_kill | task_worker_sigkill | worker.process | kill -9 后心跳冻结，staleAfter 窗口内 `ResetRunningToPending` 复位回 pending；死亡实例僵尸写入被 `FinishTaskFenced` 拒绝 |
| network_partition | git_api_unreachable | git.api | 瞬态：有界重试；恢复后同调用成功（不 hammer） |
| network_partition | llm_gateway_unreachable | llm.review | 瞬态：有界重试后任务按失败分类落 fail_reason，`ResetFailedForRetry` 退避复活 |
| lease_expiry | task_lease_expired | db.finish | 失租后旧执行体 `FinishTaskFenced` 被 fencing 拒绝，新执行体正常落终态 |
| rate_limit_429 | git_api_429 | git.api | 瞬态：退避重试至成功；非瞬态（kill）fail-fast 单次 |
| llm_fault | llm_timeout | llm.review | 瞬态：有界重试 |
| llm_fault | llm_429 | llm.review | 瞬态：退避重试至成功 |
| llm_fault | llm_empty_output | llm.review | 契约故障（非瞬态）：不盲目重试，失败分类 + 退避复活 |
| llm_fault | llm_malformed_output | llm.review | 同上 |
| db_timeout | db_write_timeout | db.write | 写路径超时/回滚后重试不产生重复任务（webhook event_id 幂等键兜底） |

## 注入点抽象（契约面）

```go
inj := chaos.NewScriptedInjector()
inj.Script("llm.review", chaos.Script{Fault: chaos.ErrUnreachable, FailFirst: 2}) // 前 2 次注入，之后放行（恢复窗）
err := chaos.Retry(ctx, inj, chaos.Op{Kind: chaos.NetLLMUnreachable, Target: "llm.review"}, 3, 0, fn)
```

- `Injector`：宿主（打桩 LLM/Git 客户端、存储包装层）每次外部调用前询问；
  部署侧容器级 job 用同一接口包真实客户端。
- 哨兵故障：`ErrKilled / ErrUnreachable / ErrLeaseLost / ErrRateLimited /
  ErrDeadline / ErrEmptyOutput / ErrMalformedOutput`（`errors.Is` 判别）。
- `IsTransient(err)`：瞬态（断网/429/超时）→ 有界重试是正确恢复动作；
  非瞬态（死亡/失租/空输出/非结构化）→ fail-fast 走失败分类。注入面与恢复面
  共用的语义契约，`test/chaos` 逐格断言。
- `Retry`：有界重试骨架（每次尝试恰好询问注入器一次，`Calls` 计数 = 尝试
  次数——断言口径单源）。

## 可执行回归与报告

```bash
make chaos            # 单测级：sqlite 矩阵（go test ./... 全量自动含）
make chaos-container  # 容器级：真 pg/MySQL 五探针 + 预算闸停机（需 docker）
# ARGUS_CHAOS_DIALECT=postgres|mysql 单方言调试
```

- 覆盖门禁：`chaos.MissingCells` / `ExtraCells`——矩阵缺格（「每格至少一条
  脚本化回归」的验收口径）与越格场景都在测试里硬断言。
- 报告双形态落 `test/chaos/results/chaos-latest.{json,md}`（gitignore；CI
  artifact 面，全量数据归档而非只留 pass/fail 信号，口径同 golden eval）。
- 单格失败不短路、probe panic 单格捕获——混沌报告要全量数据。

### 容器级五探针（批 C，`TestContainerChaos` × pg/MySQL）

选「方言真实行为 / 真基础设施」才有价值的面（与单测级矩阵分层，不重复
打桩格）；`ARGUS_CHAOS_CONTAINER=1` 门控（env 开了 docker 不可用=Fatal，
防 CI 假绿）：

| 探针 | 实弹内容 | 对位矩阵格 |
|---|---|---|
| fencing_concurrent_finish | 真行锁：跨执行体终态被 WHERE 复检拒 + 同执行体并发双写恰一赢 | lease_expiry @ db.finish |
| claim_no_double_claim | `FOR UPDATE SKIP LOCKED` 双认领体清队列：20 任务零双抢零遗漏 | process_kill 重认领面 |
| unique_key_convergence | event_id / invocation_id 撞唯一索引并发收敛（pg 23505 / mysql 1062 实码） | db_timeout @ db.write 幂等 |
| reserve_no_oversell | 同仓 4 并发预留：租约串行化下授予和 ≤ 配额、至少一拒 | （#32 预留原子性真库面） |
| db_down_bounded_error_recovery | 停容器→写/判定 5s 有界失败→重启→重开 Store→复位+重认领恢复 | network_partition + process_kill 复位面 |

另有 `internal/runner.TestBudgetGateContainerDown`（探针④，budgetGate 未
导出故住 runner 包）：真 pg 停机下 soft fail-open / hard fail-closed +
重启恢复后正常授予——Phase 1 `ARGUS_BUDGET_ENFORCEMENT` 合同的真库实弹。

**容器级实现三实锤（2026-11-15 本机，防重踩）**：①Docker Desktop 对随机
宿主端口（`-p 0`）stop/start **重新分配端口**（55028→55029），恢复必须
重取 MappedPort + 重开 Store（Linux 不漂移时重取=同端口）；②重启 wait
strategy 的 log occurrence 命中 Stop 前**旧日志=假就绪**，恢复判定只认
Open 成功（AutoMigrate 真写）；③wait 就绪后仍有竞态窗（mysql bad
connection / proxy 先监听后端未起），Open 必须重试且**失败不覆盖旧引用**
（防 nil Cleanup panic）。报告落
`results/chaos-container-latest.{json,md}`（与矩阵报告分名）。

## 完成度边界（批 C 后）

- 已落地：注入点抽象、五族故障全格实弹回归（sqlite + 打桩面）、瞬态分类
  契约、矩阵覆盖门禁、报告输出、`make chaos` 入口、**容器级 testcontainers
  pg/MySQL 五探针 + 预算闸停机分支（`make chaos-container` + CI
  `chaos-container` job，本地双方言 10/10 pass 实测）**。
- 未含：43 k3s Pod Kill 手动工作流（现场搁置，恢复后手动 job 形态）；
  生产 LLM/Git 客户端的 Injector 接线（runner 侧装配点留接口，未改在线
  链路）。
