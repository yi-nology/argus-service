# goagent-kernel 方法论移植与 argus 深度重构设计

> 状态：**Approved — 2026-11-16**。用户指令："全都弄 所有的都弄 不满足的提issue"。
> 三轨全做；不能满足/本期不做的项进 §9 的 issue 台账。
>
> 依据：<https://yi-nology.github.io/goagent-kernel/llms.txt>、kernel v0.38.0 的
> `architecture-policy.yaml` / `archcheck` / `docs/USAGE.md`，以及本项目实测基线。

---

## 0. 结论摘要

本重构把 goagent-kernel 的**治理方法**移植进 argus，包收编只占其中一小块：

- **A 轨（治理面）**：用 `architecture-policy.yaml` + kernel `archcheck` 取代手写分层白名单，
  建立"声明面 = 可执行面"、规则 examples 运行时自检、未登记包检测、文件/方法门槛、
  覆盖率棘轮、`llms.txt` AI 入口。**先落地，不动业务代码。**
- **B 轨（结构清债）**：在 A 的门禁保护下，分批拆分 31 个超 400 行的内部生产文件、
  拆巨型类型、裸 goroutine 归零、`os/exec` 收口到 `internal/execx`，
  覆盖率只升不降。**行为零变化。**
- **C 轨（能力收编）**：kernel 升到 v0.38.0；按"机制 vs 领域"判据逐包决策。
  本批确定采用 `netguard`、`sideeffect`+`sidetest`、`finalize` 终态漏斗模式；
  `reconcile` 经探针判定**不适配**（原子 SQL 路径）不采用；其余包给出候选/拒绝
  及理由（§5.3）。
  **C 轨是唯一允许改变运行行为的轨道，行为变化点全部带配置开关与实弹用例。**

推进方式：**门禁先行 + 债务棘轮**。存量债以带 `until`/`removal` 的 exceptions 登记，
新代码立即受约束；每批消债后 exceptions 单调收缩，最终归零。

---

## 1. 背景与实测基线

| 维度 | 实测值 | 来源 |
|---|---|---|
| 生产代码 | 62,587 行 / 468 个 Go 文件 | `wc -l`（排除测试） |
| internal 包 | 48 个 | `go list ./internal/...` |
| cmd 包 | 5 个 | `go list ./cmd/...` |
| kernel 依赖 | `v0.35.0`，实际使用 6/51 包 | `go.mod` + `grep` |
| 已用 kernel 包 | `spill` `pii` `ptc` `budget` `toolname` `checkpoint` | 同上 |
| 现有架构门禁 | `test/arch`：分层白名单 + 组合根单向 + 包文档 | `test/arch/arch_test.go` |
| 现有质量门禁 | `make test`（-race）/ `make lint`（vet+staticcheck+gofmt）/ `make arch` / `make eval` | Makefile |
| 覆盖率门禁 | **无**；最近实测约 43.8% | 历史批次记录 |
| 超 400 行生产文件 | **31 个 internal + 1 个 cmd** | §4.1 |
| 裸 goroutine | **1 个 internal + 1 个 cmd** | `service_eventfanout.go:48`、`cmd/argus-copilot/main.go:56` |
| `os/exec` 站点 | **3 个 internal + 1 个 cmd** | `service_repomap.go` ×3、`cmd/argus-copilot/review.go` ×1 |
| std `errors` 导入 | 28 个文件；无 `github.com/pkg/errors` | `grep -rln '^\s*"errors"$'` |

kernel 侧关键事实（v0.38.0）：

- `archcheck.Load/Run` 是**可导入的库**，宿主仓可复用同一套判据，不必自写。
- `Policy` 支持 `modules`（id/roots/requires/owner/managed/notes）、`rules`
  （regex/import/scope/examples）、`dependency_direction`（含 `ext_prefixes`）、
  `thresholds`（max_file_lines/max_methods_per_type/scan_roots/enforce）、
  `exceptions`（强制 `reason/until/removal`）、`facades`、`execution_paths`、
  `egress_paths`、`sandbox_providers`、`tenant_gate`。
- `Load` 是严格模式：未知键报错，空 policy 报错——**门禁给不出结论时不许显示成通过**。
- `packageDirs` 跳过仓库根自身；`ignore_dirs` 是精确相对路径；`roots` 支持嵌套，
  依赖归属按最长前缀。因此根 `main.go` 不需要登记，`cmd`/`biz`/`test`/`third_party`
  可整体忽略，而 `internal/...` 的每个包必须登记，漏登记即红。
- `Exception` 没有到期日或拆除条件时，policy 自校验直接报红。
- v0.35.0 → v0.38.0 对 argus 唯一的 breaking 是 `commitgate.Seed` 弃用（argus 未使用）；
  v0.38.0 新增 `finalize`/`steer`/`deadline`/`dispatch`/`eventbus.DurablePublisher`。

---

## 2. 判据与边界

### 2.1 决策判据（kernel 原句）

> "这个包需要知道自己在给谁干活吗？" 需要 → 属于宿主，不进机制层；
> 不需要 → 才可能是机制层。

对 argus 的推论：

- `internal/dispatch`（评审小队/best-of-n/共识）、`internal/learning`、`internal/dream`、
  `internal/qa`、`internal/impactview` 等是**领域包**，不得为了"像 kernel"而改造成机制包。
- kernel 的 `dispatch`/`worker`/`taskboard`/`eventbus` 与 argus 现有机制**同名不同义**
  或已被 oekit/oagentkit 占位，重复接入是制造第二套真相，不是收编。

### 2.2 三条硬约束在 argus 的映射

1. **内核只判不做**：kernel 返回"该阻断/该重试/不可归类"，argus 执行动作。
   例：`netguard` 只返回拒绝原因，HTTP 请求由 argus 决定是否发出；
   `sideeffect` 只判幂等/租约，评论由 `poster` 发。
2. **门面朝外**：argus 不新增"转发壳"包；kernel 的 facade 约束不适用于 argus 的
   `plugin/adapter_*`（它们是真实适配器，不是门面）。
3. **单向依赖**：argus 只 import kernel 的公开包，禁止 import 任何
   `goagent-kernel/internal/*`、`oekit/*/internal/*`、`oagentkit/*/internal/*`；
   由 A 轨规则封口。

### 2.3 非目标

- 不改评审业务语义、verdict 词表、findings schema、MCP 工具面。
- 不重命名 `internal/dispatch`（与 kernel 同名但语义不同；重命名是高 churn 的
  纯外观改动，另立项）。
- 不引入 kernel 的 `contract`/`statemachine`/`commitgate` 到 argus 域模型。
- 不做 acpx 执行沙箱（C4，另立项；本 spec 只登记候选判据）。
- 不涉及发版/部署；交付物是代码 + 门禁 + 测试。

---

## 3. A 轨：治理面

### 3.1 交付物

| 交付物 | 路径 | 说明 |
|---|---|---|
| 策略文件 | `architecture-policy.yaml` | 48 个 internal 包的声明面 + 规则 + 门槛 + 例外 |
| 门禁运行器 | `cmd/archcheck/main.go` | 调 kernel `archcheck.Load/Run`，非零退出 |
| 覆盖率门禁 | `cmd/covergate/main.go` + `coverage-baseline.json` | 棘轮 |
| 阈值基线 | `arch-thresholds.baseline` | 400 行/巨型类型的棘轮基线（kernel exceptions 不覆盖阈值发现） |
| 文档生成器 | `cmd/docgen/main.go` | `llms.txt` + ARCHITECTURE.md 分层表 |
| Makefile | `arch` / `cover` / `verify` / `docs` | 单一入口 |
| CI | `.github/workflows/test.yml` | arch/coverage/docsync 三个 job |

### 3.2 策略文件骨架

完整 48 个 module 由基线生成后逐条评审；下面给出**已确认的形状**与代表性条目：

```yaml
version: 1
require_rule_examples: true

modules:
  - id: codesearch
    roots: [internal/codesearch]
    requires: []
    owner: argus
    managed: true
    notes: L0 纯底座，零 internal 依赖
  - id: semsearch
    roots: [internal/semsearch]
    requires: [langcap]          # langcap 是内部 module id（internal/langcap）
    owner: argus
    managed: true
    notes: 语义检索；语言能力矩阵消费 langcap
  - id: storage
    roots: [internal/storage]
    requires: [domain, learning,
               "ext:gorm.io/gorm", "ext:github.com/oekit/pkg"]
    owner: argus
    managed: true
    notes: L3 持久化；数据层不反向依赖域
  - id: runner
    roots: [internal/runner]
    requires: [argusctx, dispatch, domain, fixverifier, impactview, learning,
               memorykernel, notify, ocrdeleg, plugin, poster, progress,
               reposettings, reportview, sastscan, semsearch, sink, storage,
               toolpool, requirement, writing,
               "ext:github.com/oekit/pkg"]
    owner: argus
    managed: true
    notes: L2 编排层；能力层全集；requirement = internal/workflow/requirement 的 module id
  # ... 其余 44 个 module 同形，由 test/arch 现有 layering 表无损转换
  # ext_prefixes 完整清单由 `go list -deps` 的外部根前缀全集生成；
  # 未列入 ext_prefixes 的外部前缀不得出现在 requires 的 ext: 条目中。

dependency_direction:
  enabled: true
  severity: error
  ignore_dirs: [cmd, biz, test, third_party, web, scripts, deploy, build-deploy,
                knowledge, memory, research, video]
  ext_prefixes:
    - github.com/oekit
    - github.com/oagentkit
    - github.com/yi-nology/goagent-kernel
    - github.com/yi-nology/go-git-platform
    - github.com/yi-nology/agent-memory
    - github.com/cloudwego
    - gorm.io
    - go.opentelemetry.io

rules:
  - id: argus-bootstrap-single-entry
    desc: 组合根单向性：internal 包不得 import bootstrap
    severity: error
    scope: [internal]
    forbid_import_regex: '^github\.com/yi-nology/argus/internal/bootstrap$'
    examples:
      - { text: 'github.com/yi-nology/argus/internal/bootstrap', want: hit }
      - { text: 'github.com/yi-nology/argus/internal/plugin', want: miss }

  - id: argus-no-harness-internal
    desc: 宿主不得上溯 harness 内部包
    severity: error
    scope: [internal]
    forbid_import_regex: '^github\.com/yi-nology/goagent-kernel/internal(/|$)|^github\.com/oekit/[a-z0-9]+/internal(/|$)|^github\.com/oagentkit/[a-z0-9]+/internal(/|$)'
    examples:
      - { text: 'github.com/yi-nology/goagent-kernel/internal/subhub', want: hit }
      - { text: 'github.com/yi-nology/goagent-kernel/spill', want: miss }

  - id: argus-no-bare-goroutine
    desc: 生产代码禁止裸 go，必须走 oekit/concurrency async
    severity: error
    scope: [internal]
    ignore_test: true
    forbid_regex: '^\s*go\s+(func\(|(?:[A-Za-z_]\w*\.)*[A-Za-z_]\w*\()'
    examples:
      - { text: '	go func() {', want: hit }
      - { text: '	async.GoSafe(ctx, log, fn)', want: miss }

  - id: argus-single-errors
    desc: 单一 errors 包：stdlib errors；禁止第三方 errors 包装层
    severity: error
    scope: [internal]
    forbid_import_regex: '^github\.com/pkg/errors$|^github\.com/go-errors/errors$'
    examples:
      - { text: 'github.com/pkg/errors', want: hit }
      - { text: 'errors', want: miss }

  - id: argus-exec-single-entry
    desc: 子进程只能经 internal/execx
    severity: error
    scope: [internal]
    scope_exclude: [internal/execx]
    forbid_import_regex: '^os/exec$'
    examples:
      - { text: 'os/exec', want: hit }
      - { text: 'github.com/yi-nology/argus/internal/execx', want: miss }

  - id: argus-no-printf-log-callback
    desc: 禁止 logger 回调形态，统一 logx.Logger
    severity: warn
    scope: [internal]
    forbid_regex: '(?:logf|logFunc|logFn|logger)\s+func\('
    examples:
      - { text: 'logger func(name string, args ...any)', want: hit }
      - { text: 'log logx.Logger', want: miss }

thresholds:
  max_file_lines: 400
  max_methods_per_type: 25
  forwarding_shell_max_chars: 140
  enforce: true
  scan_roots:
    - internal/argusctx
    - internal/codesearch
    - internal/config
    # ... 其余 45 个 module root，由 A1 基线生成（必须覆盖全部 module roots）

execution_paths:
  enabled: false
  desc: argus 不接 kernel sandbox 登记簿；唯一子进程入口由 argus-exec-single-entry 保证（os/exec 仅 internal/execx）。kernel execution_paths 判的是对裸入口（如 ptc.Run）的调用，不判 os/exec；在本仓启用会反过来把 execx.Run 调用判红，属判据误用。

egress_paths:
  enabled: false
  desc: argus 暂无子进程出口策略；C4 acpx 沙箱项再启用（对 kernel 默认开启口径的有理由偏离）

exceptions:
  # 只豁免 rules 段的违规。thresholds 的 ThresholdFindings **不走 exceptions**
  # （kernel 的 HasBlocking 单独判阈值），由 arch-thresholds.baseline 棘轮。
  # 由 A1 基线报告生成；每条必须带 reason/until/removal。
  # 示例：
  - rule: argus-exec-single-entry
    path: internal/service/service_repomap.go
    reason: 存量 git rev-parse 调用，B4 收口 execx
    until: "2026-12-31"
    removal: "internal/execx 落地后删除本例外"
```

**关键口径**：

- `require_rule_examples: true`：每条自定义规则加载期跑 hit/miss 样例；规则词表漂移
  （包名/命令名改名）会当场报红，而不是"静静地不再命中"。
- `exceptions` 的 `until` 与 `removal` 不是注释，是自校验的必填项。
- `thresholds.scan_roots` 必须覆盖全部 module roots（kernel 自校验会核对）；
  `cmd` 按 kernel 口径不进门槛（CLI 薄壳），但 B4 仍会收口其 exec。
- `execution_paths`：**保持关闭**。kernel 这条门禁判的是「对裸执行入口（如 `ptc.Run`）的
  调用」，不是 `os/exec`；argus 不接 kernel sandbox 登记簿，唯一子进程入口由
  `argus-exec-single-entry`（`os/exec` 仅 `internal/execx`）保证。把 `execx.Run`
  登记成裸入口会语义反转（声明它是应被避免的入口），故不登记。
- `egress_paths`：本批**显式关闭**并在 `desc` 写明"argus 暂无子进程出口策略，
  C4 acpx 沙箱项再启用"；这是对 kernel 默认开启口径的**有理由偏离**，不是遗漏。
- `tenant_gate`：argus 无多租户授权上下文类型，关闭并写明理由。

### 3.3 门禁运行器

`cmd/archcheck/main.go`：

```go
pol, err := archcheck.Load(flag.Arg(0))   // 缺省找 architecture-policy.yaml
res, err := archcheck.Run(root, pol)
for _, v := range res.Violations { fmt.Printf("%s:%d [%s] %s\n", v.Path, v.Line, v.Rule, v.Text) }
if res.HasBlocking() { os.Exit(1) }
```

- `make arch` = `go run ./cmd/archcheck` + `go test ./test/arch/ -run TestPackageDocs`。
- `test/arch` 的 layering 表**退役**（单一事实源变成 policy）；`TestBootstrapNotImported`
  由 `argus-bootstrap-single-entry` 规则替代；`TestPackageDocs` 保留（archcheck 不覆盖）。
- CI：`.github/workflows/test.yml` 的 arch job 改为 `make arch`；新增 coverage、docsync job。
- **例外棘轮**：`make arch` 打印 `exceptions=N`；`cmd/archcheck` 支持 `--max-exceptions`
  从 `arch-exceptions.baseline` 读取上限（文件内容为一个十进制整数），N 超过基线即红；
  `make arch-baseline` 只允许下调（读取旧值取 `min(旧, 新)`）。
- **阈值棘轮**：`thresholds.enforce` 在 A/B 阶段设为 `false`（报告口径）；
  `cmd/archcheck` 把 `res.ThresholdFindings` 与 `arch-thresholds.baseline` 比较，
  出现基线外的新发现即红；`make arch-baseline` 只允许把基线收缩为旧基线的子集。
  基线清空后把 `enforce` 翻为 `true`，此后 kernel 直接阻断新超限。这样"消债"不是靠自觉，而是 CI 可判。
- 门禁必须能证明自己活着：A 轨验收时故意注入一条违规 import 和一个未登记包，
  确认非零退出；确认后还原。

### 3.4 覆盖率棘轮

- `cmd/covergate`：
  1. 跑 `go test ./internal/... -coverprofile=coverage.out`（不带 -race，单独 job）；
  2. 用 `go tool cover -func` 按包聚合；
  3. 与 `coverage-baseline.json` 比较：
     - 全局低于 baseline − 0.5% → 红；
     - 任一已有包低于 baseline − 0.5% → 红；
     - 新增包 < 70% → 红。
- `coverage-baseline.json` 格式（数值为示例；实际以 A1 首测为准）：

```json
{
  "global": 0.438,
  "packages": { "internal/x": 0.712 },
  "generated_at": "2026-11-15T00:00:00Z",
  "commit": "…"
}
```

- `make cover-baseline` 只允许**上调**：读取旧文件，逐项取 `max(旧, 新)` 后写回；
  降低基线必须手改文件并在 commit message 说明（门禁无法阻止手改，但 diff 可见）。
- 覆盖率口径排除：`biz/model`（生成物）、`cmd`、`test`。

### 3.5 文档面（AI 可读入口）

- `cmd/docgen` 生成两份产物：
  1. 根 `llms.txt`：模块事实（go.mod 行数/包数）、48 个包的分层与职责
     （从 policy 的 `notes` 派生）、门禁命令、文档入口、MCP 工具数、支持平台/语言；
  2. `ARCHITECTURE.md` 的 `<!-- BEGIN POLICY -->` / `<!-- END POLICY -->` 之间
     的分层表，从 policy 生成，禁止手改。
- `make docs` 生成；CI docsync job 跑 `make docs && git diff --exit-code`。
- 这是 kernel `site/gen.mjs` 方法的等价物：**文档与门禁互为镜像**。

### 3.6 单一本地入口

`make verify` = `fmt-check` + `vet` + `staticcheck` + `go test -race` + `arch` + `cover`。
CI 仍按 job 拆分（并行更快），但本地一条命令复刻四门禁。

### 3.7 A 轨验收

- `make arch` 绿；规则 examples 全过；自检无红。
- 注入违规 import / 未登记包 → 红；还原后绿。
- `make cover` 在基线上绿；手动降低一个包的覆盖率 → 红。
- `make docs` 后 `git diff --exit-code` 绿。
- 不修改任何业务代码（A 轨 diff 只含 policy/工具/文档/Makefile/CI）。

---

## 4. B 轨：结构清债

### 4.1 400 行文件拆分矩阵

实测 31 个 internal + 1 个 cmd 文件超限。按包分批，每批独立 commit、全量测试：

| 批次 | 文件（行数） |
|---|---|
| B1a | `impactview/html.go` 1554、`impactview/scriptgraph.go` 774、`impactview/build.go` 602、`reportview/report.go` 672 |
| B1b | `storage/store_task.go` 715、`storage/printalias.go` 479、`storage/store.go` 450、`storage/prints.go` 425、`config/config.go` 729 |
| B1c | `workflow/requirement/r3tools.go` 703、`workflow/requirement/workflow.go` 562、`dispatch/merger.go` 456 |
| B1d | `runner/writing.go` 625、`runner/runner.go` 472、`runner/inject.go` 458、`runner/runner_lifecycle.go` 418、`runner/runner_report.go` 407、`poster/poster.go` 593、`service/service_taskapi.go` 560、`service/service.go` 405 |
| B1e | `bootstrap/assemble.go` 495、`bootstrap/models.go` 475、`bootstrap/subsys.go` 411、`qa/qa_tools.go` 475、`qa/qa.go` 420、`obs/obs.go` 460、`semsearch/semsearch.go` 444、`semsearch/chunks.go` 419、`qconsole/qconsole.go` 411、`plugin/types.go` 404、`plugin/adapter_builtin/builtin.go` 405、`cmd/argus-copilot/main.go` 439（cmd 不进门禁，顺带清） |

拆分纪律：

- 只做**移动 + 归类 + 包内私有化**，不改导出 API、不改 SQL、不改提示词文本。
- 每个拆分文件必须仍满足 `go vet`/`staticcheck`，不得引入新依赖方向。
- 依赖现有测试；覆盖不足的文件先补 characterization test（锁当前行为），再拆。
- 每批结束跑 `make verify`，并从 policy exceptions 中删除已清零的条目。

### 4.2 巨型类型

以 `make arch` 的 `max_methods_per_type` 报告为准，逐个类型按职责拆分。
阈值发现由 `arch-thresholds.baseline` 棘轮；每拆完一批就收缩基线。
原则：

- 类型的方法按"聚合根/查询/生命周期/序列化"分组，能独立成小类型就独立；
- 转发壳（单语句转发）不计入实质方法数（kernel 的 `forwarding_shell_max_chars` 口径）；
- 拆分后公共 API 保持兼容；不为了过门禁而把方法改成包级函数。

### 4.3 裸 goroutine 归零

- `internal/service/service_eventfanout.go:48`：`go func()` → `async.GoSafe(ctx, log, fn)`，
  panic 走 `async.SetPanicHandler`（main 已接线）。
- `cmd/argus-copilot/main.go:56`：同批处理；cmd 不进门禁，但保持一致性。

### 4.4 `internal/execx` 单一执行出口

新包职责：所有子进程调用的唯一入口。

```go
type Option func(*config)
func WithDir(dir string) Option
func WithEnv(env []string) Option
func WithTimeout(d time.Duration) Option
func WithMaxOutput(n int) Option

type Error struct { Command string; ExitCode int; Stderr string; Err error }

func Run(ctx context.Context, name string, args []string, opts ...Option) (stdout, stderr []byte, err error)
```

- 默认不经过 shell；需要 shell 时显式 `WithShell`，并在调用点注释理由。
- 迁移 `service_repomap.go` 三处 `git rev-parse`/`git remote`/`git log`；
  迁移 `cmd/argus-copilot/review.go` 一处。
- policy 的 `argus-exec-single-entry` 转为 error，无例外；`execution_paths` **保持关闭**
  （理由见 §3.2：它判裸入口调用，不判 `os/exec`，启用会反转语义）。

### 4.5 覆盖率

- 每批消债顺带补测拆分边界；`coverage-baseline.json` 只升不降。
- 新增包（如 `execx`）强制 ≥70%。

### 4.6 B 轨验收

- `make verify` 全绿；`make arch` 的 `max_file_lines` 例外归零。
- `make test -race` 全绿（含 pg/mysql/sqlite 方言矩阵由 CI 跑）。
- `make eval` golden 不变。
- 覆盖率全局不低于 A 轨基线。

---

## 5. C 轨：能力收编

### 5.0 C0：kernel v0.35.0 → v0.38.0

- `go get github.com/yi-nology/goagent-kernel@v0.38.0 && go mod tidy`。
- 唯一 breaking：`commitgate.Seed` 弃用（argus 未使用，`grep` 确认）。
- 传递依赖地板按 kernel v0.38.0 的 `go.mod` 对齐；若 oekit/oagentkit 有 breaking，
  在 C0 单独 commit 处理，不混入业务改动。
- 验收：`make verify` 全绿；`make eval` 不变；三方言 CI 全绿。

### 5.1 C1：本批确定采用项

#### 5.1.1 `netguard`：出站 URL 守卫（安全修复）

**实锤问题**：`internal/plugin/adapter_remote/a2a.go` 的 `a2aDiscover` 会请求
repo 级配置的 `Endpoint`，然后**信任远端返回的 `card.URL`** 并继续向它发
`message/send` 与轮询请求。`.reviewagents.yaml` 是半可信面；恶意仓库可把 Argus
指向内网/元数据地址。这是当前代码里真实存在的 SSRF 面。

**落点**：

| 调用点 | 信任档 | 说明 |
|---|---|---|
| `adapter_remote/a2a.go` `Endpoint` 与 `card.URL` | 半可信 | 严格 allow（零值 `Allow`）；私网/loopback/link-local/metadata **一律拒绝**；`ARGUS_OUTBOUND_ALLOW` 只额外放行公网 host（精确匹配） |
| `notify/octopus` baseURL | 可信 | `Allow{Private:true, Loopback:true}`，但元数据/链路本地仍拒绝 |
| `service/audit_forward` url | 可信 | 同上 |
| `qconsole` BaseURL | 可信 | 同上 |

**实现**：

- 新包 `internal/outbound`：包装 kernel `netguard`，暴露两个 profile 与
  `Client(profile, base *http.Client) *http.Client`。
- 每次请求前做**名字级**校验；拨号后做**逐 IP**校验；重定向链用
  `netguard.CheckChainWith` 逐跳复验；A2A 的 `card.URL` 是链上的新起点，必须单独校验。
- 元数据地址（`169.254.169.254`、`fd00:ec2::254`）、unspecified、link-local
  在任何 profile 下都拒绝。
- 配置：`ARGUS_OUTBOUND_MODE=off|warn|enforce`，首版默认 `warn`（记录违规但放行），
  e2e 通过后切 `enforce`；`ARGUS_OUTBOUND_ALLOW` 为逗号分隔的**公网 host 精确
  名单**（不支持 CIDR/通配/子域；私网、loopback、link-local、metadata 永不因
  白名单放行——白名单只豁免名字层，地址族仍逐 IP 复核）。

**测试**（表驱动）：

- 半可信：`http://127.0.0.1`、`http://10.0.0.1`、`http://169.254.169.254` → 拒绝；
- 半可信 + allowlist：放行白名单 host；
- 可信：私网 LLM 网关 → 放行；元数据 → 拒绝；
- 重定向：公网 → 内网 → 拒绝；
- A2A：card.URL 指向内网 → 拒绝；
- `warn` 模式：拒绝理由入日志/指标，但请求继续；`enforce` 模式：返回错误。

**行为边界**：`warn` 模式不改变现有部署行为；`enforce` 模式会拒绝未配置可信
profile 的私网远端插件（白名单不能把私网「放行」），需在 runbook 写明迁移步骤。

#### 5.1.2 `sideeffect` + `sidetest`：副作用至多一次

**实锤问题**：总结评论当前靠 `summary_comment_id` 回写做幂等；若评论已发出、
落库失败或进程崩溃，重试会重复发评论。fix PR 创建同理。

**落点**：

- 新表 `side_effects`：`effect_key`（PK，避开 MySQL 保留字）、`resource`、
  `status`（prepared/applied/failed）、`lease_until`、`payload_hash`、`created_at`、
  `updated_at`。
  迁移落在 `internal/storage/migrations.go`，三方言（sqlite/mysql/pg）建表 + 索引；
  迁移随 C1 单独 commit。
- `internal/storage/side_effects.go` 实现 kernel `sideeffect.Store`。
- `internal/effect` 宿主包装：`Runner.Do(ctx, key, resource, prepare, apply)`，
  内部用 kernel `sideeffect.NewRunner/ExecuteWith`。
- 接入：
  - 总结评论：key `comment:summary:<task_id>:<head_sha>`；
  - fix PR：key `fix:cr:<taskID>:<headSHA>:branch`，资源 `pr:<platform>:<owner>/<repo>#<number>`；
  - fix 分支推送：key `fix:push:<taskID>:<headSHA>:<mode>`，资源 `branch:<platform>:<owner>/<repo>:<targetBranch>`。

**语义边界（M7：判不了什么）**：

- 若 `Apply` 成功但"标记 applied"失败，下一次仍可能重复——provider 侧没有幂等键时，
  任何客户端都无法完全消除这个窗口。本设计把它缩小到"DB 写失败"窗口，并用
  `payload_hash` 让重试可审计。
- store 不可用时 fail-soft：记录 `argus.sideeffect.store_unavailable` 指标并继续，
  保持现有可用性；此时不承诺 at-most-once。
- 不改变现有 `summary_comment_id` 语义；sideeffect 是额外防线，不是替代。

**测试**：

- 并发同 key 两次调用 → 只有一次 Apply；
- 租约过期 → 可重试；
- applied 后重放 → 不重复 Apply，返回成功；
- 外来 token 尝试 abandon → 拒绝（kernel 契约测试已覆盖，宿主侧补接线用例）；
- store 故障 → fail-soft + 指标。

#### 5.1.3 `reconcile`：探针结论——不采用

原计划采用 `OrphanAtBoot/JudgeLease/Stalled` 三个纯函数。探针发现它们**不适配**
argus 的原子路径：

- `ResetRunningToPending` 是单语句条件 UPDATE（`heartbeat_at < cutoff`），
  原子性由 DB 保证；改成先读后判会引入 TOCTOU。
- `TryAcquire` 的 `holder = ? OR expires_at < ?` 也是单语句原子判占；
  `JudgeLease` 需要先读 beats 再判，替换会削弱多副本下的竞争安全。
- `OrphanAtBoot` 的 boot-grace 语义在现有 bulk SQL 中没有消费点。

裁决：**不接入**（判据不匹配，不是"以后再说"）。若未来需要 boot-grace 的 per-row
孤儿判定，另立项（§8.1 issue 台账）。验收：现有复位/租约行为零变化（既有测试全绿）。

#### 5.1.4 `finalize` 终态漏斗（模式优先）

**现状**：runner 有 **7 个 `FinishTaskFenced` 调用点**，覆盖 `done`/`failed`/`stale`
三种终态（`writing.go` ×3 全 done；`runner_lifecycle.go` ×2 失败路径；同文件 ×1
无新变更 stale；`finalizeAndAnnounce` ×1 按 `res.Stale` 在 done/stale 间二选一）。
各调用点各自做 `announceTerminal` + 指标，`fenced` 分支的日志文本也不一致。

**目标**：单一 `finishTask(ctx, task, cause, out, reason)`：

| Cause | 终态 | 是否公告 |
|---|---|---|
| `success` | `done` | 是 |
| `stale` | `stale` | 是（findings 入库、不回帖） |
| `fenced`（条件更新 0 行） | 不写 | 否（他路已接手） |
| `failed` | `failed` | 是 |
| `panic` | `failed` | 是（带 panic 原因） |
| `timeout` | `failed` | 是（reason=timeout） |
| `shutdown` | 不写（保持 running，启动期复位） | 否 |

> `fenced` 行描述的是**写入结果的分流**，不是调用方传入的 cause：
> `finishTask` 先按 cause 选 status，再调 `FinishTaskFenced`；返回 `fenced=true`
> 时按该行处理（跳过公告与指标）。

- 终态写使用 `context.WithoutCancel`（kernel `finalize.Detached` 语义）——请求 ctx
  已取消时仍要落库。
- 若 `FinishTaskFenced` 返回 `fenced=true`，跳过公告与指标，记录
  `argus.task.terminal_fenced`。
- **包 vs 模式**：先探针 kernel `finalize.Route`/`Converge` 的入参。如果调用它需要
  argus 实现 kernel `contract.RunContext` 或引入 `steer`/`drain` 的 cause 类型，
  就**不 import 包**，按上述模式在 `internal/runner/finalize.go` 落地；否则采用
  `finalize.Detached` 与 `finalize.Route`，并删掉本地重复实现。判定在计划阶段一次完成，
  结果写入本 spec 的修订记录。
- 测试：7 个调用点的表格测试（done/failed/stale/fenced/panic/timeout）+
  `fenced` 跳过公告 + panic/timeout 的 reason 文本。

### 5.2 C2：候选与探针标准

候选包**不预先承诺采用**；每个候选必须同时通过三条探针，否则拒绝并记录理由：

1. **边界探针**：接入不需要把 kernel 的 `contract`/`steer`/`drain` 类型带进
   argus 域模型；若需要，则只能采用其"模式"而非包。
2. **净收益探针**：能删除至少一处手写机制，或补上 argus 当前缺失的安全/正确性属性；
   仅"与 kernel 对齐"不算收益。
3. **测试探针**：能用单元/契约测试覆盖，不引入新的 e2e flakiness。

| 候选 | 可能的落点 | 探针重点 |
|---|---|---|
| `guardset` | 入站 HTTP guard 链（auth → repo guard → 限流） | 是否与 oekit/middleware 冲突；排序语义是否真的需要 |
| `retryplan` | fix 重试 + 副作用去重 | 是否与 sideeffect 重复计账 |
| `deadline` | QA 多轮会话墙钟 | 是否需要跨续跑的锚点；现有 per-plugin Timeout 是否已够 |
| `controlplane` | console 任务视图/人工介入契约 | 纯接口是否值得替换现有 qconsole DTO |
| `cmdpolicy`/`egress`/`preset`/`backend`/`gatecheck` | acpx 执行沙箱 | 需 C4 立项，单独评估 |
| `cost`/`tenant`/`metrics` | 预算/租户/指标口径 | 现有一套是否真的需要换词表 |
| `ticket` | 人工介入一次性授权 | 现 RBAC 是否缺"一次性+用途绑定" |
| `delegate`/`handoff` | remote/acpx 适配器契约 | 是否只是重命名现有接口 |
| `trajectory`/`eval` | 评测事件投影/回放 | argus evalq 已是领域实现，收益是否为正 |
| `skilldisclose`/`hookengine`/`sessionref`/`pending`/`delta` | 技能注入/钩子/会话引用 | 现有 skillkit/memorykernel/outbox 是否已覆盖 |

### 5.3 51 包处置表

| # | 包 | 处置 | 理由 |
|---|---|---|---|
| 1 | archcheck | **采用** | A 轨门禁引擎 |
| 2 | netguard | **采用** | C1 SSRF 修复 |
| 3 | sideeffect | **采用** | C1 评论/PR 至多一次 |
| 4 | sidetest | **采用** | 随 sideeffect 的契约测试 |
| 5 | reconcile | 拒绝 | 纯判定需先读后判，argus 的复位/租约是单语句条件 UPDATE；替换削弱原子性（§5.1.3） |
| 6 | finalize | **采用（模式优先）** | 终态漏斗；包 API 探针后定 |
| 7 | budget | 已用 | QA 历史压实 |
| 8 | checkpoint | 已用 | 工作流续跑 |
| 9 | pii | 已用 | 出站脱敏 |
| 10 | ptc | 已用 | 程序化工具编排 |
| 11 | spill | 已用 | 输出预算溢出 |
| 12 | toolname | 已用 | 工具名单一来源 |
| 13 | contract | 拒绝 | 需要 kernel 编排语义；argus 任务是领域模型 |
| 14 | statemachine | 拒绝 | 5 态域模型 + DB 条件更新已足够；只吸收"终态谓词单源"纪律 |
| 15 | commitgate | 拒绝 | `FinishTaskFenced` 是跨进程 DB 级互斥，强于进程内 guard |
| 16 | dispatch | 拒绝 | 与 argus 领域 `internal/dispatch` 同名不同义；kernel 包是权限拓扑门面 |
| 17 | worker | 拒绝 | 已由 `oagentkit/queue` 占位 |
| 18 | taskboard | 拒绝 | 已由任务表 + queue 占位 |
| 19 | coord | 拒绝 | 同上 |
| 20 | scheduling | 拒绝 | 同上 |
| 21 | eventbus | 拒绝 | argus outbox 是观测面语义（本地优先、best-effort），kernel 是"落库才广播" |
| 22 | drain | 拒绝 | oekit/bootstrap + queue 两阶段排水已覆盖 |
| 23 | protocol | 拒绝 | 报告格式是 argus 领域契约 |
| 24 | memorypipe | 拒绝 | memorykernel 已有注入/引用/打标 + 防伪 + 证据链 |
| 25 | worktree | 拒绝 | kernel 不碰真实 FS；argus 修复链需要真实 worktree |
| 26 | tooldisclose | 拒绝 | MCP 是固定 24 工具面，外部客户端消费，无模型动态发现循环 |
| 27 | watchdog | 拒绝 | argus 是任务级编排，无工具调用环；INV1/2/3 没有对应事件面 |
| 28 | steer | 拒绝 | 评论触发创建新任务，无运行中接管语义 |
| 29 | offload | 拒绝 | 大对象外置已由 spill/artifacts 覆盖；无新增场景 |
| 30 | guardset | 拒绝 | 鉴权是 token→scope→限流的链式依赖；guardset 只收独立判定 |
| 31 | retryplan | 拒绝 | 无工具调用重试层；任务退避与 LLM 重试各一套，接入添第二分类器 |
| 32 | deadline | 拒绝 | 无跨续跑共享墙钟；Status() 绑 contract 状态词表（域外） |
| 33 | controlplane | 延后（issue） | 缺操作员介入 API 是真缺口，但 Plane 绑 taskboard/tenant，只能取模式 |
| 34 | cmdpolicy | 延后（C4） | execx 全固定 argv；需 C4 执行面才有策略消费者 |
| 35 | egress | 延后（C4） | 无出口声明面与兑现后端；单独接是假安全 |
| 36 | preset | 延后（C4） | 无隔离/审批/出口三元组，三面散参数尚不存在 |
| 37 | backend | 延后（C4） | 只有一个子进程入口，无第二后端可选 |
| 38 | gatecheck | 延后（C4） | 无审批层与沙箱 registry 两层可合成 |
| 39 | cost | 拒绝 | 预算靠预留+条件更新+SQL 双账本；cost 只聚合已发生事件 |
| 40 | tenant | 拒绝 | argus 无租户概念；tenant_gate 已关，接入是第二套身份真相 |
| 41 | metrics | 拒绝 | Specs 为封闭 harness 词表；obs 45 个 prometheus 指标无法登记 |
| 42 | ticket | 拒绝 | 无一次性授权消费面；share HMAC 设计为可重复查看 |
| 43 | delegate | 拒绝 | 插件选择由 plugins.yaml/repo settings 配置驱动，非能力协商 |
| 44 | handoff | 拒绝 | DB claim+心跳+RunnerID 栅栏已覆盖；无跨进程步骤续做场景 |
| 45 | trajectory | 拒绝 | 依赖 kernel eventbus（已拒）；evalq 是领域评测 |
| 46 | eval | 拒绝 | 同上；Assess 只判结构事实，evalq 已做 precision/recall/stability |
| 47 | skilldisclose | 拒绝 | skillkit 已做发现/允许清单/内容上限/按需加载 |
| 48 | hookengine | 拒绝 | Hook* 是观测回调，hookengine 是决策面；无外部 hook 消费者 |
| 49 | sessionref | 拒绝 | 无跨会话引用；QA 同会话历史+budget，记忆有 agent-memory 生命周期 |
| 50 | pending | 拒绝 | 无反问/消歧流程与待答列；A2A input-required 不是本地挂起态 |
| 51 | delta | 延后（issue） | ReAct/QA 流式显式关闭；增量消费另批评估 |

**处置口径**：`采用` = 本 spec 交付；`已用` = 保持并随 C0 升级；`拒绝` = 本程序不再评估；
`延后` = 有明确缺口但依赖后续批次/上游解耦，进 §8.1 issue 台账。

### 5.3.1 C2 探针结论（2026-11-16）

22 项候选全部完成三探针（边界/净收益/可测性），逐项证据见工作区
`.superpowers/sdd/2026-11-16-kernel-method-refactor/task-18-c2-probe.md`（不入库）：

- **0 项现在采用**：C4 五项（backend/gatecheck/preset/cmdpolicy/egress）净收益依赖尚不存在的
  执行后端；其余为机制错位或 argus 已有更强实现（预算原子账本、skillkit、evalq、
  memorykernel、DB claim/心跳栅栏等）。
- **15 项拒绝、7 项延后**（C4×5 + controlplane + delta）。
- 4 条 issue：acpx 沙箱（§8.1 #2）、控制面人工介入、流式增量消费、C2 未采用项结案。
- 4 条上游缺口：`deadline.Outcome.Status` 绑 contract 词表、`controlplane.Plane` 绑
  taskboard/tenant、`metrics.Specs` 封闭不可注册、`retryplan.Decide` 硬编码 toolfail 分类器。

### 5.4 C 轨验收

- C0：`make verify` 全绿；三方言 CI 全绿；`make eval` 不变。
- netguard：新增用例覆盖 §5.1.1 的 URL 表；`warn` 模式线上行为不变；
  `enforce` 模式在 e2e 环境实测通过。
- sideeffect：并发/重放/租约过期/store 故障用例全绿；`make eval` 不变。
- reconcile：探针结论不采用；现有复位/租约行为零变化（既有测试全绿）。
- finalize：7 个调用点的表格测试全绿；`fenced` 路径不公告；panic/timeout reason 正确。
- 所有行为变化点有配置开关，默认值在 §5.1 钉死；无开关的改动必须证明行为等价。

---

## 6. 实施顺序、批次与回滚

| 阶段 | 内容 | 合并条件 |
|---|---|---|
| P0 基线 | 跑 `make test`/`make lint`/`make arch`/覆盖率；生成 archcheck 基线报告 | 记录基线数字，工作树干净 |
| P1 (A) | policy + archcheck + covergate + docgen + CI | A 轨验收全绿 |
| P2 (B) | B1a→B1e、B2、B3、B4 | 每批 `make verify` 绿；exceptions 递减 |
| P3 (C0) | kernel v0.38.0 升级 | 全绿；无业务改动 |
| P4 (C1) | netguard → sideeffect → finalize（reconcile 探针不采用） | 每项独立 commit；验收用例绿 |
| P5 (C2) | 候选探针逐项决策 | 采用项按 C1 标准；拒绝项更新 §5.3 |

- 分支：`refactor/kernel-method`，每批小 commit；四门禁绿后快进合并 `main`。
- 回滚：A/B 为纯结构/门禁，revert 单批即可；C 的行为变化点走配置开关，
  回滚 = 关开关 + revert 对应 commit。
- 并行会话：每批开始前声明文件所有权；提交前 `git status` 核对，禁止 `git add -A`。

---

## 7. 风险与缓解

| 风险 | 缓解 |
|---|---|
| 31 个文件拆分引入细微行为变化 | 先补 characterization test；只做移动+归类；每批 `-race` + golden eval |
| policy exceptions 赖账 | `until` + `removal` 自校验；每批打印例外数，只降不升；CI 断言 |
| netguard 误伤内网 LLM 网关 | 首版 `warn`；allowlist；可信/半可信双档；e2e 后再 `enforce` |
| sideeffect 改变重试语义 | 只包评论/PR 写入；store 故障 fail-soft + 指标；e2e 实弹 |
| kernel 升级传递依赖 breaking | C0 独立 commit；三方言矩阵；失败则回退 v0.35.0 并只做 A/B |
| 与并行会话撞车 | 小批次 + 文件所有权 + 提交前 `git status` + 禁止 `git add -A` |
| 门禁本身失效（规则写法错误） | `require_rule_examples: true`；注入违规验收；policy 自校验 |

---

## 8. 明确排除项

- **C4 acpx 执行沙箱**：`backend`/`gatecheck`/`preset`/`cmdpolicy`/`egress` 的接入
  需要先定义 argus 的执行隔离档位与审批链，作为独立子项目立项；本 spec 只保留候选。
- **重命名 `internal/dispatch`**：与 kernel 同名但语义不同；重命名不影响行为，
  但会产生大量 import churn，另立项。
- **领域状态词表改造**：`pending/running/done/failed/stale` 保持不变；
  只新增终态谓词单源。
- **部署/发版**：本 spec 结束时交付可合并的代码与门禁，不包含 43 环境部署。

### 8.1 Issue 台账（"不满足的提issue"）

以下项本期不能满足或明确不做，逐条提 issue 跟踪；C2 探针失败项与 C1 发现的上游缺口
在执行中追加。

| # | 仓库 | 标题 | 触发条件 |
|---|---|---|---|
| 1 | argus | 覆盖率从棘轮基线上调到 kernel 60/70 口径 | 本期只做棘轮；达到 60/70 需单独测试冲刺 |
| 2 | argus | acpx 执行沙箱（backend/gatecheck/preset/cmdpolicy/egress） | C4 独立立项；本期 `egress_paths` 关闭 |
| 3 | argus | tenant_gate：多租户授权上下文与过滤漏拼门 | argus 当前无多租户上下文类型，`tenant_gate` 关闭 |
| 4 | argus | 根 `main.go` 纳入依赖方向门禁 | kernel `packageDirs` 跳过仓根；需移到 `cmd/argus` 或扩展 archcheck |
| 5 | argus | C2 未采用项结案（15 项） | 探针逐项拒绝，证据见 §5.3.1；重评条件写在各行理由 |
| 6 | argus | `internal/dispatch` 与 kernel `dispatch` 同名不同义 | 纯重命名，高 import churn，另立项 |
| 7 | argus | `argus-no-printf-log-callback` 从 warn 升 error | 存量清理完成后收紧 |
| 8 | argus | sideeffect 结果未知态自动对账（reconciler） | provider 无幂等键时 uncertain 会阻塞创建，需人工/对账解锁 |
| 9 | argus | `internal/sink/sink.go` 出站守卫 | 仍用 `http.DefaultClient`，与 netguard 同族 SSRF 面 |
| 10 | argus | 可信档拨号恢复 Happy Eyeballs | 当前按序拨已校验 IP，双栈黑洞首地址可能吃满 deadline |
| 11 | argus | execx 超时后的孙进程/管道收口（WaitDelay） | 孙进程持管道可让 Run 阻塞到 deadline 之后 |
| 12 | argus | reconcile boot-grace 的 per-row 孤儿判定 | 探针拒绝（原子 SQL 路径）；若未来需要按启动时刻区分孤儿再立项 |
| 13 | argus | 控制面人工介入 API（强制释放/裁决/终止） | C2 controlplane 延后：Plane 绑 taskboard/tenant，先取模式 |
| 14 | argus | ReAct/QA 流式增量消费（delta） | 当前显式关流；流式采纳另批评估 |
| 15 | goagent-kernel | `deadline.Outcome.Status` 绑 contract 状态词表 | 非 contract 宿主只能自映射；建议解耦 |
| 16 | goagent-kernel | `controlplane.Plane` 绑 taskboard/tenant | 宿主无法用自己的任务 ID/租户类型；建议拆窄接口 |
| 17 | goagent-kernel | `metrics.Specs` 封闭不可注册 | 宿主自有指标无法复用 Validate/基数门 |
| 18 | goagent-kernel | `retryplan.Decide` 硬编码 toolfail 分类器 | 无法注入宿主分类器 |
| 19 | argus | `procx.Run` 收口到 execx（15 处生产站点） | 终审 gate false-green：exec 规则只判直接 os/exec |
| 20 | argus | `effect.RetryBlocked` 补 `ErrUnknownOutcome` | 定向复审 residual：防御性收口，当前不可达 |

已创建（2026-11-16）：argus **#36–#49 + #50–#51**（16 条；#50/#51 为本表 19/20）；
goagent-kernel **#237–#240**（4 条，与上表 15–18 同序）。

---

## 9. 附录 A：判据面诚实声明（M7）

这套门禁**判不了**：

- 运行时行为等价性（文件拆分是否改变了执行顺序/错误路径）——靠测试与 golden eval。
- `execution_paths` 本批关闭：它判的是对裸执行入口（如 `ptc.Run`）的调用，不判 `os/exec`；
  argus 的单入口由 `argus-exec-single-entry` 保证。因此**判不了**"有人绕过 execx 直接
  持有后端实例执行"（需要 go/types），靠该规则与 review 兜底。
- `egress_paths` 本批关闭，因此**判不了**子进程出口策略；这是已知缺口，C4 补。
- `tenant_gate` 关闭，因此**判不了**租户过滤漏拼；argus 当前无多租户授权上下文。
- `dependency_direction` 判不了根 `main.go`（kernel `packageDirs` 跳过仓库根）：
  组合根单向性由 `argus-bootstrap-single-entry` 规则与 bootstrap 单点装配保证，
  main 只做进程生命周期（现状即如此）。
- `sideeffect` 判不了"Apply 成功但标记失败"的重复窗口；只能缩小并审计。
- `netguard` 判不了 DNS 重绑定之外的运行时代理解析差异；以逐 IP 拨号校验兜底。

## 10. 附录 B：术语

- **机制/领域**：机制不知道服务对象；领域知道。判据见 §2.1。
- **声明面/可执行面**：policy 文件既是给人读的声明，也是门禁执行的输入。
- **棘轮**：存量债可登记，但只许减不许增；覆盖率与例外数都按此口径。
- **判据面**：一条门禁实际能判什么、判不了什么，必须写明，禁止"看起来绿"。