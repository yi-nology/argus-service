# Argus 架构地图

> 三份文档的分工：**[docs/design/design.md](docs/design/design.md)** 是设计契约（§ 引用的出处）；**[docs/design/architecture-deep-dive.md](docs/design/architecture-deep-dive.md)** 是历史演进分析；
> 本文件是**当前实现分层事实**——与 [test/arch](test/arch/arch_test.go) 可执行守卫互为镜像：文档给人读，
> 测试给门禁跑，两者不一致时以能跑的为准，并修正另一方。

## 分层总览

```text
                     ┌─────────────────────────────┐
                     │  main (argus)               │  进程生命周期：配置/信号/停机 + HTTP 接线
                     └──────┬──────────────────────┘
                            │
  ┌─────────────────────────▼───────────────────────────┐
  │ L1 组合根  bootstrap                                │  装配一切、不被任何人 import（组合根单向性）
  │  Assemble(对象图) + StartSubsystems(后台子系统)     │  选主/worker 池/运维 ticker/dream/health/poller
  └──┬───────┬───────┬───────┬───────┬───────┬─────────┘
     │       │       │       │       │       └────────────┐
  ┌──▼────┐ ┌▼─────┐ ┌▼─────┐ ┌▼─────┐ ┌▼──────────┐ ┌──▼──────────┐
  │ runner │ │service│ │poller│ │dream │ │ channel/  │ │ worker      │  L2 编排 / 入口
  │        │ │       │ │      │ │      │ │ mcpserver │ │ (异步消费)  │
  └──┬─────┘ └┬──────┘ └┬─────┘ └┬─────┘ └┬──────────┘ └──┬──────────┘
     │        │         │        │        │★零 internal   │
     │        │         │        │        │ 依赖(窄接口)  │
  ┌──▼────────▼─────────▼────────▼────────▼───────────────▼──────────┐
  │ L3 能力层                                                        │
  │  workflow/requirement(R1-R5 编排)   poster(回帖)   qa(问答)      │
  │  argusctx(sdkctx*/diffprep/githubapp)  storage(持久化,gorm)       │
  │  reportview(渲染) toolpool(mcp) sastscan sink notify progress    │
  │  ocrdeleg/ocrshim/gitdeepen(OCR 链) health(体检) memorykernel    │
  └──┬───────────────────────────────────────────────────────────────┘
     │
  ┌──▼───────────────────────────────────────────────────────────────┐
  │ L4 核心域  plugin                                                 │  契约单源：Task/Outcome/Registry/
  │  + domain(域类型) learning(学习模型)                             │  Hook* 词表/manifest 全在这里
  └──┬───────────────────────────────────────────────────────────────┘
     │
  ┌──▼───────────────────────────────────────────────────────────────┐
  │ L0 纯底座  config(配置) obs(指标)            │  横切件：config→learning、obs→plugin（钩子接线）
  └──────────────────────────────────────────────────────────────────┘
```

## 依赖规则（由 `architecture-policy.yaml` 生成；`make docs` 同步）

<!-- BEGIN POLICY -->
| 包 | 允许的 internal / external 依赖 | 说明 |
|---|---|---|
| internal/acpxsandbox | execpolicy, ext:github.com/oagentkit/guardx, ext:github.com/oagentkit/toolgate, ext:github.com/oagentkit/tools, ext:github.com/yi-nology/goagent-kernel | acpx 执行沙箱（#37）——kernel 五项 backend/gatecheck/preset/cmdpolicy/egress 的装配面；IsolationProcess + EgressUnrestricted 自报，不冒充容器/禁网 |
| internal/agentstream | ext:github.com/yi-nology/goagent-kernel | agentrun 原生流式事件 → kernel delta.Pacer 节流增量（纯适配层，无 internal 依赖） |
| internal/argusctx | config, ext:github.com/oagentkit/textx, ext:github.com/oekit/data, ext:github.com/oekit/logx, ext:github.com/oekit/pkg, ext:github.com/yi-nology/go-git-platform, ext:github.com/yi-nology/goagent-kernel, plugin, tenant, egress | - |
| internal/bootstrap | ext:github.com/cloudwego/eino, ext:github.com/cloudwego/eino-ext, ext:github.com/oagentkit/llm, ext:github.com/oagentkit/obs, ext:github.com/oagentkit/queue, ext:github.com/oagentkit/rag, ext:github.com/oagentkit/runtime, ext:github.com/oagentkit/skillkit, ext:github.com/oagentkit/tools, ext:github.com/oekit/bootstrap, ext:github.com/oekit/concurrency, ext:github.com/oekit/logx, ext:github.com/oekit/pkg, ext:github.com/oekit/tracing, ext:github.com/yi-nology/agent-memory, ext:github.com/yi-nology/go-git-platform, ext:github.com/yi-nology/goagent-kernel, acpxsandbox, adapter_acpx, adapter_builtin, adapter_cli, adapter_remote, argusctx, config, dispatch, dream, effect, egress, execpolicy, execx, goedges, health, knowledgefilter, memorykernel, notify, obs, outbound, plugin, poller, poster, progress, runner, semsearch, service, sink, storage, tenant, toolpool, worker | - |
| internal/channel/mcpserver | ext:github.com/cloudwego/hertz, ext:github.com/mark3labs/mcp-go, ext:github.com/oekit/middleware | - |
| internal/chaos | （无） | - |
| internal/cli | config, execx, learning, ocrshim, qconsole, storage, ext:github.com/glebarez/sqlite, ext:github.com/mark3labs/mcp-go, ext:github.com/oekit/concurrency, ext:github.com/oekit/core, ext:github.com/oekit/logx, ext:github.com/oekit/pkg, ext:github.com/yi-nology/goagent-kernel, ext:golang.org/x/tools/cover, ext:gorm.io/driver, ext:gorm.io/gorm | 单二进制子命令入口（服务启动仍在仓根 main.go） |
| internal/codesearch | （无） | - |
| internal/commentcmd | ext:github.com/yi-nology/go-git-platform | - |
| internal/config | ext:github.com/oagentkit/llm, ext:github.com/oekit/core, learning, reporttmpl | - |
| internal/dispatch | domain, ext:github.com/oagentkit/agent, ext:github.com/oagentkit/guardx, ext:github.com/oagentkit/llm, ext:github.com/oagentkit/textx, ext:github.com/oekit/concurrency, ext:github.com/oekit/logx, ext:github.com/oekit/pkg, learning, plugin, obs | - |
| internal/domain | ext:github.com/oagentkit/textx, learning | - |
| internal/dream | ext:github.com/oagentkit/guardx, ext:github.com/oagentkit/rag, ext:github.com/oagentkit/textx, ext:github.com/oekit/logx, learning, memorykernel, plugin, storage, tenant | - |
| internal/effect | ext:github.com/oekit/logx, ext:github.com/yi-nology/go-git-platform, ext:github.com/yi-nology/goagent-kernel, ext:github.com/yi-nology/goagent-runtime, obs, storage, tenant | 副作用至多一次宿主包装（kernel sideeffect：幂等键+租约+栅栏+未知态，fail-soft 边界） |
| internal/egress | ext:github.com/oekit/logx | LLM 出站治理（同意门/凭据双通道掩码/哈希审计） |
| internal/evalq | ext:gopkg.in/yaml.v3 | - |
| internal/execpolicy | ext:github.com/oagentkit/tools, ext:github.com/yi-nology/goagent-kernel, ext:mvdan.cc/sh, ext:github.com/oekit/logx | 子进程执行策略（basename 归一 + cmdpolicy 危险命令拒绝 + 危险 env 剥离；warn/enforce/off）。不是沙箱。kernel v0.73 起 cmdpolicy 归位 oagentkit/tools |
| internal/execx | execpolicy, ext:github.com/oagentkit/runtime | 唯一子进程执行出口（Run/LookPath；procx 托管纪律经同包门面；入口过 execpolicy） |
| internal/fixverifier | domain, execx, ext:github.com/oagentkit/guardx, ext:github.com/oagentkit/runtime, plugin | - |
| internal/gakloop | ext:github.com/yi-nology/goagent-kernel | agentloop 吸收（worker.Loop/coord 任务板/steer 接管/checkpoint 单调闸） |
| internal/gitdeepen | ext:github.com/oagentkit/tools | - |
| internal/goedges | ext:github.com/oagentkit/textx/gotyped, semsearch | - |
| internal/health | ext:github.com/oagentkit/guardx, ext:github.com/oekit/logx, learning | - |
| internal/impactview | domain, execx, ext:github.com/oagentkit/guardx, ext:github.com/oagentkit/runtime, ext:github.com/oagentkit/textx, gitdeepen, semsearch, storage | - |
| internal/knowledgefilter | ext:github.com/cloudwego/eino, ext:github.com/oagentkit/rag, tenant | rag.KnowledgeService 租户过滤装饰器（#84/#92）——装配单点在 bootstrap.buildKnowledge 出口 |
| internal/langfuseobs | ext:github.com/oagentkit/llm, ext:go.opentelemetry.io/otel, ext:go.opentelemetry.io/otel/sdk/trace | - |
| internal/learning | ext:github.com/oagentkit/textx | - |
| internal/loopadapt | agentstream, ext:github.com/cloudwego/eino, ext:github.com/yi-nology/goagent-kernel, ext:github.com/yi-nology/goagent-runtime | 宿主迁 goagent-runtime agentloop 的共用接缝（einoChat/工具转换/事件桥 + RunReAct 运行接线单源）——QA/builtin/R3 三条迁移线防漂移；判据（contract/roundguard）直取 kernel，装配走 runtime |
| internal/memorykernel | ext:github.com/oekit/logx, ext:github.com/yi-nology/agent-memory | - |
| internal/notify | ext:github.com/oagentkit/textx, ext:github.com/oekit/logx, obs, octopus, outbound, storage, tenant | - |
| internal/notify/octopus | （无） | - |
| internal/obs | ext:github.com/oekit/metrics, ext:github.com/prometheus/client_golang, ext:github.com/prometheus/client_model, plugin, storage, tenant | - |
| internal/ocrdeleg | execx, ext:github.com/oagentkit/runtime, ext:github.com/oagentkit/textx, gitdeepen, plugin | - |
| internal/ocrshim | adapter_cli, domain, execx, ext:github.com/mark3labs/mcp-go, ext:github.com/oagentkit/guardx, ext:github.com/oagentkit/runtime, ext:github.com/oagentkit/textx, ext:github.com/oekit/logx, gitdeepen, plugin | - |
| internal/outbound | ext:github.com/oagentkit/guardx, ext:github.com/oekit/concurrency, ext:github.com/oekit/logx, ext:github.com/yi-nology/goagent-kernel | 出站 SSRF 守卫（netguard 包装：可信/半可信两档 + warn/enforce/off；kernel v0.73 起 netguard 归位 guardx） |
| internal/plugin | agentstream, domain, ext:github.com/cloudwego/eino, ext:github.com/oagentkit/agent, ext:github.com/oagentkit/guardx, ext:github.com/oagentkit/llm, ext:github.com/oagentkit/textx, ext:github.com/oagentkit/tools, ext:github.com/oekit/core, ext:github.com/oekit/logx, ext:github.com/oekit/pkg, ext:gopkg.in/yaml.v3, reporttmpl, tenant | - |
| internal/plugin/adapter_acpx | acpxsandbox, dispatch, domain, execx, ext:github.com/oagentkit/runtime, ext:github.com/oagentkit/textx, ext:github.com/oagentkit/tools, ext:github.com/oekit/logx, ext:github.com/oekit/pkg, plugin | - |
| internal/plugin/adapter_builtin | agentstream, dispatch, domain, ext:github.com/cloudwego/eino, ext:github.com/oagentkit/llm, ext:github.com/oagentkit/skillkit, ext:github.com/oekit/logx, ext:github.com/yi-nology/goagent-kernel, ext:github.com/yi-nology/goagent-runtime, loopadapt, plugin, reporttmpl | - |
| internal/plugin/adapter_cli | domain, execx, ext:github.com/oagentkit/runtime, ext:github.com/oagentkit/textx, ext:github.com/oagentkit/tools, ext:github.com/oekit/logx, plugin | - |
| internal/plugin/adapter_remote | domain, ext:github.com/mark3labs/mcp-go, ext:github.com/oagentkit/guardx, ext:github.com/oagentkit/textx, ext:github.com/oagentkit/tools, ext:github.com/oekit/kitx, ext:github.com/oekit/logx, outbound, plugin, tenant | - |
| internal/poller | argusctx, domain, ext:github.com/oagentkit/runtime, ext:github.com/oekit/concurrency, ext:github.com/oekit/logx, ext:github.com/yi-nology/go-git-platform, obs, progress, storage | - |
| internal/poster | argusctx, dispatch, domain, effect, ext:github.com/oagentkit/guardx, ext:github.com/oagentkit/textx, ext:github.com/oekit/logx, ext:github.com/yi-nology/go-git-platform, plugin, reportview, storage, tenant | - |
| internal/progress | ext:github.com/oekit/pkg | - |
| internal/qa | agentstream, argusctx, codesearch, ext:github.com/cloudwego/eino, ext:github.com/oagentkit/guardx, ext:github.com/oagentkit/llm, ext:github.com/oagentkit/rag, ext:github.com/oagentkit/textx, ext:github.com/oagentkit/toolgate, ext:github.com/oagentkit/toolname, ext:github.com/oagentkit/tools, ext:github.com/oekit/logx, ext:github.com/yi-nology/go-git-platform, ext:github.com/yi-nology/goagent-kernel, ext:github.com/yi-nology/goagent-runtime, loopadapt, plugin, semsearch, storage, tenant | - |
| internal/qconsole | outbound, storage | - |
| internal/qualityops | （无） | - |
| internal/reporttmpl | （无） | - |
| internal/reportview | dispatch, domain, ext:github.com/oagentkit/guardx, plugin, reporttmpl | - |
| internal/reposettings | （无） | - |
| internal/runner | argusctx, config, dispatch, domain, effect, execx, ext:github.com/cloudwego/eino, ext:github.com/oagentkit/guardx, ext:github.com/oagentkit/llm, ext:github.com/oagentkit/obs, ext:github.com/oagentkit/rag, ext:github.com/oagentkit/runtime, ext:github.com/oagentkit/skillkit, ext:github.com/oagentkit/textx, ext:github.com/oagentkit/tools, ext:github.com/oekit/concurrency, ext:github.com/oekit/data, ext:github.com/oekit/logx, ext:github.com/oekit/pkg, ext:github.com/yi-nology/go-git-platform, fixverifier, gitdeepen, impactview, langfuseobs, learning, memorykernel, notify, obs, ocrdeleg, plugin, poster, progress, reporttmpl, reportview, reposettings, requirement, sastscan, semsearch, sink, storage, tenant, toolpool, writing, gakloop | - |
| internal/sastscan | dispatch, domain, ext:github.com/oagentkit/tools, plugin | - |
| internal/semsearch | ext:github.com/oagentkit/textx | - |
| internal/service | acpxsandbox, argusctx, commentcmd, config, dispatch, domain, effect, execx, ext:github.com/cloudwego/eino, ext:github.com/cloudwego/hertz, ext:github.com/oagentkit/agent, ext:github.com/oagentkit/guardx, ext:github.com/oagentkit/llm, ext:github.com/oagentkit/obs, ext:github.com/oagentkit/rag, ext:github.com/oagentkit/runtime, ext:github.com/oagentkit/textx, ext:github.com/oagentkit/tools, ext:github.com/oekit/concurrency, ext:github.com/oekit/data, ext:github.com/oekit/kitx, ext:github.com/oekit/logx, ext:github.com/oekit/middleware, ext:github.com/oekit/pkg, ext:github.com/yi-nology/go-git-platform, ext:github.com/yi-nology/goagent-kernel, ext:golang.org/x/time/rate, health, impactview, memorykernel, obs, outbound, plugin, progress, qa, qconsole, reporttmpl, reposettings, semsearch, storage, tenant, writing | 控制面裁决参数直接使用 kernel sideeffect.Resolution（存储契约入参），不引入 kernel controlplane 类型 |
| internal/sink | domain, ext:github.com/oekit/concurrency, ext:github.com/oekit/logx, ext:github.com/oekit/pkg, outbound, plugin, tenant | - |
| internal/storage | domain, ext:github.com/glebarez/sqlite, ext:github.com/go-sql-driver/mysql, ext:github.com/jackc/pgx, ext:github.com/oagentkit/queue, ext:github.com/oagentkit/textx, ext:github.com/oekit/concurrency, ext:github.com/oekit/data, ext:github.com/oekit/pkg, ext:github.com/yi-nology/goagent-kernel, ext:gorm.io/driver, ext:gorm.io/gorm, learning, progress, tenant | - |
| internal/tenant | ext:gorm.io/gorm | 多租户贯穿维度基元（#80 母票地基）——叶子包只依赖 gorm（Scoped）与 stdlib；Normalize/Key/WithoutScope 单源 |
| internal/toolpool | ext:github.com/cloudwego/eino, ext:github.com/oagentkit/mcpkit, ext:github.com/oekit/logx, plugin | - |
| internal/worker | storage | - |
| internal/workflow/requirement | argusctx, codesearch, dispatch, domain, ext:github.com/cloudwego/eino, ext:github.com/oagentkit/agent, ext:github.com/oagentkit/guardx, ext:github.com/oagentkit/llm, ext:github.com/oagentkit/rag, ext:github.com/oagentkit/textx, ext:github.com/oagentkit/toolgate, ext:github.com/oekit/logx, ext:github.com/oekit/pkg, ext:github.com/yi-nology/go-git-platform, ext:github.com/yi-nology/goagent-kernel, ext:github.com/yi-nology/goagent-runtime, loopadapt, obs, plugin, reporttmpl, semsearch, toolpool | - |
| internal/writing | ext:github.com/cloudwego/eino, ext:github.com/oagentkit/llm, ext:github.com/oagentkit/textx | - |

<!-- END POLICY -->

**变更流程**：新增/调整任何 internal 依赖 → 必须先改 `architecture-policy.yaml` 并跑
`make arch` → CHANGELOG 记档。门禁失败即"架构变更未显式化"。

## 同名不同义：internal/dispatch ≠ kernel dispatch（#41）

`internal/dispatch` 与 kernel（agent-harness）的 `dispatch` 同名而语义无关：

- **kernel dispatch**：「权限即拓扑」的**派发守卫门面**——agent 间可达性由 allow
  矩阵物理决定（构建期物化、运行期只读），拒绝返回结构化 DenyError；核心下沉在
  `oagentkit/agent/dispatch`，harness 侧只做 EdgeSource 透传与 Guard 构造单点。
  argus 不 import 它。
- **argus `internal/dispatch`**：审查编排领域词——评审任务向多专家插件的
  **分发执行**（Dispatcher fan-out/预算/熔断）、结果**聚合**（Merger 去重/抑制 +
  best-of-N 采样复现）、**编队共识**（squad_consensus 黑板跨轮合并）。纯确定性
  Go 代码，不含任何权限拓扑语义。

读者按「kernel=守卫门面、argus=领域编排」区分；重命名为 internal/squad 等领域名
的方案 import churn 高，另立项处理（包内 doc.go 有同款标注）。

## 仓根入口的依赖方向门禁（#39）

kernel archcheck 的 `packageDirs` 跳过仓库根——根 `main.go` 的 import 不受其
dependency_direction 规则约束。本仓的补位方式：`test/arch` 门禁把 `go list`
采集根扩到仓根，根 main 在 `layering` 白名单显式归类（允许集 = bootstrap +
channel/mcpserver + config + obs + service + storage，封闭）；`TestRootMainGated`
守护采集面本身（含白名单负例断言），`TestBootstrapNotImported` 声明根 main 是
组合根唯一合法上游。选扩扫描根而非「移 main 到 cmd/argus」：后者要动 Dockerfile /
Makefile 构建入口与 hz 根布局生成物（router*.go），影响面大。

## 架构不变量（评审时的硬检查项）

1. **契约单源**：plugin.Task 八字段输入契约、WorkflowOutput、Verdict/Layer 词表、
   Hook* 观测钩子、AgentManifest——只定义在 plugin，跨包引用不得克隆。
2. **消费侧窄接口**：跨层依赖一律"谁消费谁定义"（plugin.WorkflowRunner、
   mcpserver.Core、PrintStore→domain.PrintStore）。接口的实现数 >1 才配接口；
   单实现且无测试替身用途的用具体类型。
3. **插件适配器单向**：adapter_* → plugin 单向；新形态适配器（如 sandboxed CLI）
   落 plugin/adapter_* 子包，不进核心域。
4. **组合根单点**：一切对象图组装在 bootstrap（Assemble/BuildModels/BuildRegistry/
   StartSubsystems），main 只剩进程生命周期（v3.63.0 闭环）；任何包不得反向
   import bootstrap。
5. **观测旁路**：L4 纯逻辑不依赖 prometheus——业务包调 `plugin.Report*`（nil 安全），
   obs 装配期接线实现；指标词表（stage/reason label）与告警规则对齐，改值断 Grafana。
6. **数据防线上移**：半可信输入（diff/issue/模型输出）进提示词必须过 DataFence，
   输出面必须过 fence.EscapeUntrusted——单一出处，禁止本地转义克隆。

## 守卫

```bash
make test    # 全量测试（-race，与 CI test job 同参）
make lint    # 静态三合一：vet + staticcheck + gofmt（与 CI lint job 同参）
make arch    # 分层白名单 + 组合根单向性 + 包文档完整性（CI arch job）
make eval    # golden 编排语义回归
```

命令口径单源在 Makefile；CI 只编排 job 与矩阵环境。本地四连过 = 流水线绿。
