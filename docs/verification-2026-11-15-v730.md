# argus v7.3.0 部署验证报告（2026-11-15）

- 部署：43 k3s（10.60.226.43）双副本，v7.1.0 → **v7.3.0**
- 通路：GitLab DNS 断（git.example.com 解析失败）→ **Mac 直连换芯新通路**
  （本地 alpine+postgres 层拷 CA 零外网 build → scp → ctr import）
- 版本：tag `v7.3.0`=a235680（双 remote 对账一致）
- 范围：Phase 1-3 + Phase 4/E/F/G 实施全量 + ggp v0.137/v0.138

## 1. 部署面（七点全绿）

| # | 检查项 | 结果 |
|---|---|---|
| 1 | 镜像 | `argus:v7.3.0`（双副本 RESTARTS=0） |
| 2 | 迁移模式 | `ARGUS_MIGRATE_MODE=verify`（Phase 1 同批换芯生效，业务 Pod 零 DDL） |
| 3 | 探针分离 | liveness=`/healthz`、readiness=`/readyz` |
| 4 | 版本端点 | `/version` = v7.3.0 |
| 5 | readyz | `db/llm/in_flight_guard` 全 ok（verifySchemaReady 过=新表在） |
| 6 | healthz | pong |
| 7 | 启动日志 | `http_ready version=v7.3.0` |

- migration-job（v7.3.0 镜像）先跑 DDL：`JOB_COMPLETE`——新表
  `fp_case_buckets`/`usage_events`/`budget_reservations` SQL 实证存在，
  `tasks.fix_status` 列实证存在。
- 存量：tasks done=1268 / stale=64 / failed=1。

## 2. 端到端实弹（真实 LLM，GitLab om-gitlab 真 MR）

### 2.1 requirement_compliance 全链（obs/kylin-insights-frontend#230）

- 任务 `t-db6decd5fa4a444711151016`：**done**，verdict=`comment`（R5.3
  需人工确认），tokens=995,038，耗时 ~6min（R1-R5 真实全链）。
- **findings=3**（真产出）；报告含「📎 证据」行 ×1、「⚠️ 证据待验证」×1
  ——Phase 2 渲染面消费实证。

### 2.2 Phase 2 Evidence 四态出口（核心验证）

- `result_json.findings[*].evidence_status`：**unverified × 3**（四态回写
  落库 ✓）。
- **MCP `list_findings` 全链**（initialize→session→tools/call，v7.3.0
  serverInfo）：3 条全带 `evidence_status=unverified` + fp 短码 +
  `⚠️ 证据待验证（未提供结构化证据：无 file:line 引用）`——**Ruling② 措辞
  线上实证**（无引用 unverified 只标不降级；medium/low 保级 ✓）。

### 2.3 批 F 追溯链（按设计正确不产）

- 报告无「需求追溯链」段：该 PR 无结构化验收清单 → R1 产 nil spec →
  `BuildTraceChain(nil)=nil` 不产（completeness 无 AC 检查项旁证 spec=nil）。
  **行为正确**——有 spec 路径由单测 `TestBuildTraceChain`/渲染测试覆盖。

### 2.4 批 G 意外冒烟（kind 传错的收获）

- 首次创建误传 `kind:2`=**visual_check**（枚举 REQUIREMENT_COMPLIANCE=0）：
  任务走 visual 面（SkillVisualCheck→frontend-visual discovery 失败→R5.0
  空输出 comment）——**批 G 任务类型分流线上冒烟 ✓**（verdict=comment
  未越权，comment-only 纪律未被违反）。

## 3. 服务面冒烟

| 面 | 结果 |
|---|---|
| `GET /console` | 质量控制台 HTML 正常（Phase 4 消费面） |
| `GET /metrics` | 269 条 argus_* 指标（含 task_duration_by_repo 等） |
| `GET /tasks/{id}` | 任务详情 findings 数组正常 |
| MCP `initialize` | v7.3.0，session 制正常 |

## 4. 发现的边界（如实记录，非回归）

1. ~~HTTP `GET /tasks/{id}` findings 是轻量 DTO~~ **已闭环（收尾批 e4e1ae1）**：
   thrift Finding 补 6:evidence/7:evidence_status 可选字段（与 MCP findingDTO
   字段族对称），HTTP 出参同源透出；thriftgo 0.4.5 对齐（PATH 独立 0.4.3 优先
   于 hz 内嵌致 version 漂移的坑已修）+ idl-gen 幂等验证。
2. **OCR 二进制缺失**（`/opt/argus/bin/ocr`）：alpine 重建镜像丢了 OCR
   层——**零影响**（在役 secret 无 `ARGUS_OCR_BIN`，OCR 本就未启用，
   启动仅 WARN 降级）。
3. **spec=nil 路径**：无验收清单的 PR 不产追溯链（§2.3）——按设计。
4. **修复链 FixStatus**：在役 secret 无 `ARGUS_FIX_PUSH`（修复环未启用）
   ——`fix_status` 列就位但无线上任务流经；启用后补验。
5. **kind 枚举**：`POST /tasks` 的 kind 是 thrift int
   （0=requirement_compliance/1=code_review/2=visual_check）——调用方注意。

## 5. 本地代码面（同批已验）

- 四门禁（lint/test 53 包/arch/eval）全绿；
- 批 C 容器级 chaos 双方言 10/10；批 D 覆盖率 43.8%；
- Phase 2 两 Ruling、Phase 3 按 PR 收敛/状态机/写凭证分离均有 TDD 钉。

## 6. 结论

**部署成功、验证通过**：七点部署面全绿；真实 LLM 端到端全链 done；
Phase 2 四态出口（落库+MCP+渲染措辞）线上实证；批 G 类型分流意外冒烟；
新表新列 SQL 实证。遗留=§4 五条边界（均非回归，后续小批/启用后补验）。

## 7. 收尾批（2026-11-15 晚）

| 项 | 结果 |
|---|---|
| **thrift Finding 证据族** | `e4e1ae1`：IDL 加 evidence/evidence_status（可选零破坏）+生成物对齐+HTTP 组装透出+断言测试；idl-gen 幂等绿 |
| **网关视觉能力探针**（批 G 余量） | 网关 9 模型中**含 `qwen3.8-vl-flash`**（VL 视觉模型）——**当前 API key 租户未授权**（"当前用户所属租户未被授权访问该模型"）。结论：**基础设施在、授权待开通**；VisualBrief 数据面就绪，授权开通即可接增强路径 |
| **真库数据面**（Phase 4/E） | suppressed_prints=0 / fp_case_buckets=0 / fix_status 无记录——表结构就绪、无历史数据（本环境从未人工抑制/未启用修复环）；候选清单真库扫=空清单（诚实空态，非故障） |
| **om-gitlab MR #221 实弹闭环** | argus 三轮自动审查（webhook 触发，push 即审+stale 去重）→ 按建议修 2 commit（tmp/ 落点+COLLATE→fail-closed+INPLACE/LOCK=NONE，真机 MySQL 四态验证）→ **已合并 main_v5（12b90dd5）** |
