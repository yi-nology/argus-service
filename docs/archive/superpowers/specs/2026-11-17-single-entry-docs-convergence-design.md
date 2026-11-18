# 单入口与文档四层收敛设计

日期：2026-11-17
状态：已批准（设计对话批准，实施中）
分支：`refactor/single-entry-docs`（基于 `main` @ 9a60632）

## 1. 目标

仓库存在两个收敛问题：

1. **启动入口分散**：根 `main.go` 是唯一服务入口，另有 `cmd/` 下 10 个独立 main
   （archcheck / archgen / argus-copilot / covergate / dbcopy / docgen / exportdata /
   modcheck / ocrshim / qconsole）。构建、镜像、部署与文档各自记住不同的二进制名，
   `dbcopy` 甚至不在镜像内（`deploy/k8s43/README.md` 声称 `/app/dbcopy` 可用，实际
   Dockerfile 从未 COPY）。
2. **文档分散且无门禁**：根目录与 `docs/` 混放现行/历史/过程产物；`design.md`、
   `ARCHITECTURE.md`、`docs/architecture-deep-dive.md` 三份架构文档并列；已完成的
   批次报告、已停用的服务集成文档、被 43 替代的 215 部署文档仍与现行运维文档同级。

收敛为：**一个二进制（子命令分发）+ 四层文档（guide/ops/design/archive）+ 文档门禁**。

## 2. 启动入口：单二进制

### 2.1 进程入口

根 `main.go` 保留，新增分发层：

```text
argus                      → serve（兼容 Docker ENTRYPOINT / 现役清单）
argus serve                → serve
argus help | -h | --help   → 用法表，退出 0
argus version              → 打印构建版本，退出 0
argus <command> [flags]    → 子命令
未知命令                    → 用法到 stderr，退出 2
argv[0] basename=ocrshim   → 路由到 ocrshim 子命令（旧 sidecar 清单兼容）
```

服务启动逻辑（`runApp` / `initOTel` / `openStore` / `resetRunningOnBoot` /
`startHTTP` / `registerCustom`）全部留在 `main.go`，行为零变化。`main.go` 只新增
`run(args []string) int` 与命令查找；`version` 与 ldflags 注入的 `main.version` 同源。

### 2.2 子命令包

`cmd/<name>` → `internal/cli/<name>`，每个包导出 `Run(args []string) int`：

| 现目录 | 新包 | 子命令 |
|---|---|---|
| `cmd/argus-copilot` | `internal/cli/copilot` | `argus copilot` |
| `cmd/qconsole` | `internal/cli/qconsole` | `argus qconsole` |
| `cmd/dbcopy` | `internal/cli/dbcopy` | `argus dbcopy` |
| `cmd/exportdata` | `internal/cli/exportdata` | `argus exportdata` |
| `cmd/ocrshim` | `internal/cli/ocrshim` | `argus ocrshim` |
| `cmd/archcheck` | `internal/cli/archcheck` | `argus archcheck` |
| `cmd/archgen` | `internal/cli/archgen` | `argus archgen` |
| `cmd/covergate` | `internal/cli/covergate` | `argus covergate` |
| `cmd/docgen` | `internal/cli/docgen` | `argus docgen` |
| `cmd/modcheck` | `internal/cli/modcheck` | `argus modcheck` |

改造纪律：

- `main()` → `Run(args []string) int`；文件名 `main.go` → `run.go`。
- 全局 `flag.CommandLine` → `flag.NewFlagSet(name, flag.ContinueOnError)`；解析失败
  返回 2（`flag.ErrHelp` 返回 0）。
- 深层 `os.Exit` 改为返回码：门禁 0=通过 / 1=发现或漂移 / 2=工具无法给结论；
  copilot 的 SIGINT/SIGTERM=130 保留。
- `internal/cli/cli.go` 持有命令表（Name/Summary/Run）、`Lookup`、`Usage`；根 main
  仅依赖该表。
- 各包的测试随包迁移，保持原断言；新增根级分发表单测（空参/`serve`/help/version/
  未知命令/argv0=`ocrshim`）。

### 2.3 架构治理

- `architecture-policy.yaml` 新增 module：

  ```yaml
  - id: cli
    roots: [internal/cli]   # kernel 按最长目录前缀归属，覆盖全部子命令子包
    requires: [config, execx, learning, ocrshim, qconsole, storage,
               ext:github.com/glebarez/sqlite, ext:github.com/mark3labs/mcp-go,
               ext:github.com/oekit/concurrency, ext:github.com/oekit/core,
               ext:github.com/oekit/logx, ext:github.com/oekit/pkg,
               ext:github.com/yi-nology/goagent-kernel,
               ext:golang.org/x/tools/cover, ext:gorm.io/driver, ext:gorm.io/gorm]
    owner: argus
    managed: true
    notes: 单二进制子命令入口（服务启动仍在仓根 main.go）
  ```

- `ext_prefixes` 补充 `golang.org/x/tools/cover`（covergate 消费；现有登记只有
  go/packages 与 go/types/typeutil 两条子路径）。
- `ignore_dirs` 移除 `cmd`：目录删除后 kernel 自校验会因“登记的 ignore_dir 不存在”而红。
- `make arch` 通过；`make docs` 重新生成 `ARCHITECTURE.md` 生成区与 `llms.txt`
  （internal 包计数随 module 增加而变）。

### 2.4 构建与部署

| 面 | 改动 |
|---|---|
| Makefile | 删 `build-ocrshim`；门禁目标改 `go run . archcheck|covergate|docgen|modcheck`，`arch-baseline` 同理 |
| Dockerfile | 只构建 `/out/argus`；运行层 `ln -s /app/argus /app/ocrshim`；ENTRYPOINT 不变 |
| compose | ocrshim 服务 `entrypoint: ["/app/argus", "ocrshim"]` |
| k8s 示例 | `08-ocrshim-sidecar.yaml.example` 的 args 前置 `ocrshim` |
| build-deploy | `Dockerfile.runtime` 只 COPY argus + 建 symlink |
| 文档/脚本 | `scripts/install-ocr.sh`、`deploy/README.md`、`docs/ops/runbook.md`、`plugins.yaml` 示例、`deploy/k8s43/README.md`（dbcopy → `argus dbcopy`） |
| .gitignore | 去掉 `/argus-copilot`、`/dbcopy` |

发布产物仍只有 `argus`，CI release 口径不变；镜像 job 随 Dockerfile 自然收敛。

## 3. 文档四层

### 3.1 目录

```text
docs/
├── README.md            # 索引：四层导航、状态、维护约定
├── guide/               # 用户与接入
├── ops/                 # 运维、交付、质量
├── design/              # 设计、契约、路线、债账本
└── archive/             # 过程与历史（链接校验豁免）
```

根目录 `README.md` / `ARCHITECTURE.md` / `CHANGELOG.md` / `CONTRIBUTING.md` /
`SECURITY.md` 保留原位。`deploy/README.md`、`deploy/k8s/README-scaling.md` 作为
目录说明保留。`knowledge/`（运行时提示词资产）、`web/`（宣传站）、`eval/`、
`skills/` 原地不动（仅更新其中的命令示例）。

### 3.2 迁移映射

| 现路径 | 新路径 |
|---|---|
| `design.md` | `docs/design/design.md` |
| `PILOT.md` | `docs/ops/pilot.md` |
| `review-service-integration.md` | `docs/archive/review-service-integration.md` |
| `docs/API-COMPAT.md` | `docs/guide/API-COMPAT.md` |
| `docs/github-app-setup.md` | `docs/guide/github-app-setup.md` |
| `docs/ide-mcp.md` | `docs/guide/ide-mcp.md` |
| `docs/plugin-authoring.md` | `docs/guide/plugin-authoring.md` |
| `docs/qconsole.md` | `docs/guide/qconsole.md` |
| `docs/architecture-deep-dive.md` | `docs/design/architecture-deep-dive.md` |
| `docs/review-closed-loop-design.md` | `docs/design/review-closed-loop-design.md` |
| `docs/roadmap.md` | `docs/design/roadmap.md` |
| `docs/tech-debt.md` | `docs/design/tech-debt.md` |
| `docs/deploy-production.md` | `docs/ops/deploy-production.md` |
| `docs/chaos-matrix.md` | `docs/ops/chaos-matrix.md` |
| `docs/quality-ops.md` | `docs/ops/quality-ops.md` |
| `docs/compaction-playbook.md` | `docs/ops/compaction-playbook.md` |
| `docs/eval-baseline.md` | `docs/ops/eval-baseline.md` |
| `docs/skills-release.md` | `docs/ops/skills-release.md` |
| `deploy/ops-runbook.md` | `docs/ops/runbook.md` |
| `docs/deploy-215.md` | `docs/archive/deploy-215.md` |
| `docs/execution-13items.md` | `docs/archive/execution-13items.md` |
| `docs/verification-2026-11-15-v730.md` | `docs/archive/reports/verification-2026-11-15-v730.md` |
| `docs/archive/2026-11-11-arch-hygiene-report.md` | `docs/archive/reports/2026-11-11-arch-hygiene-report.md` |
| `docs/releases/*.md` | `docs/archive/releases/*.md` |
| `docs/superpowers/{plans,specs}` | `docs/archive/superpowers/{plans,specs}`（含本 spec） |
| `research/mimo-vs-argus/` | `docs/archive/research/mimo-vs-argus/` |
| `-idl/=`（误建跟踪目录） | 删除 |

### 3.3 引用重写

全仓更新引用（含代码注释）：

- 根 README：设计/试点/闭环/目录导览/副驾命令
- `ARCHITECTURE.md` 手写头（三份文档分工）
- `llms.txt` 与 `docgen` 硬编码文档清单
- `deploy/README.md`、`docs/ops/runbook.md` 内的相互链接与 `PILOT.md` 引用
- `main.go` 头注释 `design.md`
- `skills/argus-mcp/SKILL.md`、`skills/argus-mcp/references/tools.md`、
  `docs/guide/ide-mcp.md`、`docs/guide/qconsole.md` 的命令示例
- `.go` 注释中的 `docs/review-closed-loop-design.md` → `docs/design/...`

命令口径统一为：

```text
已构建/镜像内：argus <command> ...
源码树开发：  go run . <command> ...
```

### 3.4 索引与状态

`docs/README.md` 包含：四层导航表（路径/状态/一句话）、根入口文档链接、
`deploy/README.md`、`knowledge/`、`web/` 的旁路说明、维护约定
（`docs/superpowers/` 为 skill 工作目录，收尾归档到 `docs/archive/superpowers/`）。

## 4. 文档门禁

`docgen -check` 在生成物一致性之外新增：

1. **索引完整性**：`docs/guide|ops|design` 下每个 `.md` 必须被 `docs/README.md` 引用。
2. **链接可解析**：根入口文档（README/ARCHITECTURE/CONTRIBUTING/SECURITY）+
   `docs/**`（archive/superpowers/research 豁免）+ `deploy/README.md` 中的相对
   `.md` 链接必须指向真实文件；跳过 http(s)/mailto/#、行内代码与 fenced code。
3. llms.txt「文档」清单改为四层入口 + `docs/README.md`。

门禁失败沿用现有退出码：生成物漂移或索引/链接缺失 = 1，工具自身错误 = 2。
新增单测覆盖：缺索引条目、断链、代码块豁免、外链豁免、正常通过。

## 5. 验收

- `make verify`：vet / lint / test(-race) / arch / cover / eval / crossbuild /
  modcheck / docs-check 全绿。
- `make build` 后冒烟：
  - `bin/argus help`、`bin/argus version`
  - `bin/argus archcheck`（在干净树上应为 0）
  - `bin/argus docgen -check`、`bin/argus modcheck`、`bin/argus covergate`
    （先用 `make cover` 生成 profile）
  - `bin/argus ocrshim -h`；`ln -s bin/argus /tmp/ocrshim && /tmp/ocrshim -h`
    验证 argv0 路由
- 文档门禁在重写后全绿。
- 不改服务运行语义、不改数据库 schema、不改 HTTP/MCP 契约。

## 6. 风险与回滚

| 风险 | 处置 |
|---|---|
| 现役 43 sidecar 用 `/app/ocrshim` | 镜像内 symlink + argv0 路由双保险；新清单改用 `argus ocrshim` |
| `go run . <tool>` 编译面变大 | 根 main 导入全部子命令；Go 缓存后影响可接受，门禁命令是主要使用面 |
| 外部指向 docs 旧路径的链接断裂 | 仓内全部重写；仓外不可控，在 CHANGELOG/发布说明中声明路径迁移 |
| archive 历史文档路径失效 | 明确豁免，不参与链接门禁 |
| `ignore_dirs` 移除 cmd 后 kernel 自校验 | 删除目录与 policy 同步；`make arch` 验证 |
| 与在途 #66/#61 提交交错 | 独立工作树基于 main，不夹带 |

回滚：本变更为纯结构收敛，回退分支即可；数据库/服务契约无变化。