# 单入口与文档四层收敛实施计划

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 把 10 个 `cmd/*` 独立二进制收敛为单个 `argus` 子命令入口，并把文档重组为 `docs/{guide,ops,design,archive}` 四层，同时用 `docgen -check` 守住索引完整性与相对链接。

**Architecture:** 根 `main.go` 保留服务启动并新增纯分发表；10 个工具迁移到 `internal/cli/<name>`，每个包导出 `Run(args []string) int`；`internal/cli` 持有命令表。文档迁移后统一重写仓内引用，`docgen -check` 增加索引/链接两项门禁。服务运行语义、HTTP/MCP 契约、DB schema 均不变。

**Tech Stack:** Go 1.27 / hertz / 标准库 flag / goagent-kernel archcheck / docgen / Makefile / Docker / k8s manifests

**Spec:** `docs/superpowers/specs/2026-11-17-single-entry-docs-convergence-design.md`

## Global Constraints

- 单二进制：根 `main.go` 是唯一进程入口；`argus`（无参）与 `argus serve` 行为等同于当前服务启动。
- 子命令名沿用旧名：`copilot qconsole dbcopy exportdata ocrshim archcheck archgen covergate docgen modcheck`。
- 退出码：0 成功 / 1 门禁发现或生成物漂移 / 2 用法或工具无法给结论；copilot 的 SIGINT/SIGTERM=130 保留。
- `argv[0]` basename 为 `ocrshim` 时必须路由到 ocrshim 子命令（旧 sidecar 清单兼容）。
- 文档四层：`docs/guide`（用户）、`docs/ops`（运维）、`docs/design`（设计）、`docs/archive`（过程与历史）。
- `docs/archive/**`、`docs/superpowers/**`（归档前）豁免链接校验；`CHANGELOG.md` 不改历史条目。
- 不改服务运行语义、数据库 schema、HTTP/MCP 契约；不改 `knowledge/`、`web/` 正文（只改其中的仓库链接与命令示例）。
- 工作树：`/Users/zhangyi/my_project/lacus/argus/.worktrees/single-entry-docs`，分支 `refactor/single-entry-docs`，基点 `main` @ 9a60632。
- 每个任务结束必须 `git commit`；提交信息带任务范围前缀。

---

### Task 1: CLI 注册表与根分发器

**Files:**
- Create: `internal/cli/cli.go`
- Create: `main_dispatch_test.go`
- Modify: `main.go`（`func main()` 改为分发层 + `runServe()`）

**Interfaces:**
- Produces:
  - `cli.Command{Name string; Summary string; Run func([]string) int}`
  - `cli.All() []cli.Command`
  - `cli.Lookup(name string) (func([]string) int, bool)`
  - `cli.Usage(w io.Writer)`
  - `runWith(args []string, argv0 string, stdout, stderr io.Writer) int`（package main）
  - `resolve(args []string) (name string, rest []string)`（package main）

- [ ] **Step 1: 写失败测试**

`main_dispatch_test.go`：

```go
package main

import (
	"bytes"
	"strings"
	"testing"
)

func TestResolveDefaultServe(t *testing.T) {
	name, rest := resolve(nil)
	if name != "serve" || len(rest) != 0 {
		t.Fatalf("resolve(nil) = %q, %v; want serve, []", name, rest)
	}
	name, rest = resolve([]string{"archcheck", "-policy", "x"})
	if name != "archcheck" || len(rest) != 2 || rest[0] != "-policy" {
		t.Fatalf("resolve = %q, %v", name, rest)
	}
}

func TestRunWithHelp(t *testing.T) {
	var out, errb bytes.Buffer
	if code := runWith([]string{"help"}, "argus", &out, &errb); code != 0 {
		t.Fatalf("help code = %d, want 0", code)
	}
	if !strings.Contains(out.String(), "用法") {
		t.Fatalf("help 输出缺少用法:\n%s", out.String())
	}
}

func TestRunWithVersion(t *testing.T) {
	var out, errb bytes.Buffer
	if code := runWith([]string{"version"}, "argus", &out, &errb); code != 0 {
		t.Fatalf("version code = %d, want 0", code)
	}
	if !strings.Contains(out.String(), version) {
		t.Fatalf("version 输出 = %q, want 含 %q", out.String(), version)
	}
}

func TestRunWithUnknownCommand(t *testing.T) {
	var out, errb bytes.Buffer
	if code := runWith([]string{"nope"}, "argus", &out, &errb); code != 2 {
		t.Fatalf("unknown code = %d, want 2", code)
	}
	if !strings.Contains(errb.String(), "未知命令") {
		t.Fatalf("stderr = %q, want 未知命令", errb.String())
	}
}
```

- [ ] **Step 2: 运行测试确认失败**

Run: `go test . -run 'TestResolve|TestRunWith' -count=1`
Expected: FAIL（`resolve` / `runWith` 未定义）

- [ ] **Step 3: 新建 `internal/cli/cli.go`**

```go
// Package cli 是单二进制 argus 的子命令注册表。
//
// 服务启动仍留在仓根 main.go（进程生命周期与组合根）；本包只登记工具与门禁
// 子命令。子命令名沿用 v7.3 之前的 cmd/<name> 目录名，避免肌肉记忆与脚本漂移。
package cli

import (
	"fmt"
	"io"
	"sort"
)

// Command 是一个子命令登记项。Run 返回进程退出码（0 成功 / 1 发现 / 2 用法或无法判定）。
type Command struct {
	Name    string
	Summary string
	Run     func([]string) int
}

// All 返回全部已登记子命令（按 Name 排序）。
func All() []Command {
	cmds := []Command{}
	sort.Slice(cmds, func(i, j int) bool { return cmds[i].Name < cmds[j].Name })
	return cmds
}

// Lookup 按名查找子命令。
func Lookup(name string) (func([]string) int, bool) {
	for _, c := range All() {
		if c.Name == name {
			return c.Run, true
		}
	}
	return nil, false
}

// Usage 打印用法表（stdout/stderr 由调用方决定）。
func Usage(w io.Writer) {
	fmt.Fprintln(w, "argus —— 可插拔多 Agent 审查平台")
	fmt.Fprintln(w, "\n用法:")
	fmt.Fprintln(w, "  argus [serve]                启动服务（缺省）")
	fmt.Fprintln(w, "  argus <command> [flags...]   运行工具/门禁")
	fmt.Fprintln(w, "\n命令:")
	for _, c := range All() {
		fmt.Fprintf(w, "  %-12s %s\n", c.Name, c.Summary)
	}
	fmt.Fprintln(w, "  version      打印构建版本")
	fmt.Fprintln(w, "  help         本帮助")
}
```

- [ ] **Step 4: 改根 `main.go`**

把现有 `func main()` 改名为 `func runServe() int`，函数体末尾补 `return 0`；在文件内新增：

```go
func main() {
	os.Exit(runWith(os.Args[1:], os.Args[0], os.Stdout, os.Stderr))
}

// runWith 是可测试的分发层：argv0 兼容旧 ocrshim sidecar 入口；无参/serve 走服务。
func runWith(args []string, argv0 string, stdout, stderr io.Writer) int {
	if filepath.Base(argv0) == "ocrshim" {
		if fn, ok := cli.Lookup("ocrshim"); ok {
			return fn(args)
		}
	}
	name, rest := resolve(args)
	switch name {
	case "serve":
		return runServe()
	case "help", "-h", "--help":
		cli.Usage(stdout)
		return 0
	case "version":
		fmt.Fprintln(stdout, version)
		return 0
	}
	fn, ok := cli.Lookup(name)
	if !ok {
		fmt.Fprintf(stderr, "argus: 未知命令 %q\n\n", name)
		cli.Usage(stderr)
		return 2
	}
	return fn(rest)
}

func resolve(args []string) (string, []string) {
	if len(args) == 0 {
		return "serve", nil
	}
	return args[0], args[1:]
}
```

导入区补 `io`、`path/filepath`、`github.com/yi-nology/argus/internal/cli`。

- [ ] **Step 5: 运行测试与构建**

Run: `go test . -run 'TestResolve|TestRunWith' -count=1 && go build .`
Expected: PASS + 构建成功

- [ ] **Step 6: 提交**

```bash
git add internal/cli/cli.go main.go main_dispatch_test.go
git commit -m "refactor(cli): 根分发器与子命令注册表骨架"
```

---

### Task 2: 迁移门禁五件（archcheck / archgen / covergate / docgen / modcheck）

**Files:**
- Move: `cmd/archcheck/{main.go,main_test.go}` → `internal/cli/archcheck/{run.go,run_test.go}`
- Move: `cmd/archgen/{main.go,main_test.go}` → `internal/cli/archgen/{run.go,run_test.go}`
- Move: `cmd/covergate/{main.go,main_test.go}` → `internal/cli/covergate/{run.go,run_test.go}`
- Move: `cmd/docgen/{main.go,main_test.go}` → `internal/cli/docgen/{run.go,run_test.go}`
- Move: `cmd/modcheck/{main.go,main_test.go}` → `internal/cli/modcheck/{run.go,run_test.go}`
- Modify: `internal/cli/cli.go`

**Interfaces:**
- Produces: 每个包 `func Run(args []string) int`
- Consumes: Task 1 的 `cli.Command`

**统一改造模板（五件通用）：**

```go
func Run(args []string) int {
	fs := flag.NewFlagSet("argus <name>", flag.ContinueOnError)
	// ... 原 flag.String/Bool 改为 fs.String/Bool ...
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	// ... 原 main 体，os.Exit(n) 全部改成 return n ...
}
```

- 文件首行 `package main` → 对应包名（`archcheck`/`archgen`/`covergate`/`docgen`/`modcheck`）。
- 包注释保留，首行 `// Package main` 改为 `// Package <name>`。
- 删除 `func main()` 包裹，改为 `func Run(args []string) int`。

**逐件特性：**

| 包 | flags | 特殊返回 |
|---|---|---|
| archcheck | `-policy` `-max-exceptions` `-thresholds-baseline` `-write-baseline` | 原 0/1/2 全部改 `return`；`write-baseline` 成功路径 `return 0` |
| archgen | 无 flag | `panic(err)` → `fmt.Fprintln(os.Stderr, ...); return 2`；成功 `return 0` |
| covergate | `-profile` `-baseline` `-write` | 原 0/1/2 改 `return`；成功尾 `return 0` |
| docgen | `-check` | `errDrift` → 1，其余 IO → 2；成功 `return 0` |
| modcheck | 无 flag | `run(".")` 错误 → 1；成功 `return 0` |

- [ ] **Step 1: 迁移并改造五件**

```bash
mkdir -p internal/cli/{archcheck,archgen,covergate,docgen,modcheck}
for n in archcheck archgen covergate docgen modcheck; do
  git mv cmd/$n/main.go internal/cli/$n/run.go
  git mv cmd/$n/main_test.go internal/cli/$n/run_test.go
done
```

按上表完成 `package`/`Run`/flag/exit 改造。

- [ ] **Step 2: 注册五件**

在 `internal/cli/cli.go` 的 `All()` 返回表里加入（导入对应包）：

```go
cmds := []Command{
	{Name: "archcheck", Summary: "架构门禁：policy + kernel archcheck + 阈值/例外棘轮", Run: archcheck.Run},
	{Name: "archgen", Summary: "生成 architecture-policy.yaml 的 modules 段", Run: archgen.Run},
	{Name: "covergate", Summary: "覆盖率棘轮门禁", Run: covergate.Run},
	{Name: "docgen", Summary: "生成/校验 llms.txt 与 ARCHITECTURE.md 生成区", Run: docgen.Run},
	{Name: "modcheck", Summary: "模块面门禁：replace 白名单 + 禁 go.work + go mod verify", Run: modcheck.Run},
}
```

（替换 `All()` 里的 `cmds := []Command{}`。）

- [ ] **Step 3: 运行包测试**

Run: `go test ./internal/cli/... -count=1`
Expected: PASS（原测试断言不变）

- [ ] **Step 4: 冒烟**

```bash
go run . archcheck
go run . docgen -check
go run . modcheck
go run . archcheck -h
go run . covergate -h
go run . archgen -h
```

Expected：`archcheck`/`docgen -check`/`modcheck` 0；`-h` 均 0（帮助文本到 stderr）。
`covergate` 无参数在无 `coverage.out` 时返回 2，属预期。

- [ ] **Step 5: 提交**

```bash
git add internal/cli cmd
git commit -m "refactor(cli): 门禁五件迁入 internal/cli 子命令"
```

---

### Task 3: 迁移运维三件（dbcopy / exportdata / qconsole）

**Files:**
- Move: `cmd/dbcopy/main.go` → `internal/cli/dbcopy/run.go`
- Move: `cmd/exportdata/main.go` → `internal/cli/exportdata/run.go`
- Move: `cmd/qconsole/main.go` → `internal/cli/qconsole/run.go`
- Modify: `internal/cli/cli.go`

**Interfaces:**
- Produces: `dbcopy.Run(args []string) int` / `exportdata.Run(args []string) int` / `qconsole.Run(args []string) int`

**逐件改造：**

- `dbcopy`：无 flag。签名 `func Run(args []string) int`；若 `len(args) > 0` 打印现有 usage 并 `return 2`。`fatal()` 改为 `func fail(msg string, err error) int`（打印后 `return 1`），所有 `fatal(...)` 调用改 `return fail(...)`；env 缺失 `return 2`；成功 `return 0`。
- `exportdata`：`fs := flag.NewFlagSet("argus exportdata", flag.ContinueOnError)`，三个 flag 用 `fs.*`；`fs.Parse` 错误 → 2（`ErrHelp` → 0）；所有 `os.Exit(1)` → `return 1`；成功 `return 0`。
- `qconsole`：同模板四个 flag（`-addr -token -days -out -text -eval-report`）；token 缺失 → 2；聚合/渲染/写文件失败 → 1；`-text` 分支与 `-out -` 分支 `return 0`；成功 `return 0`。

- [ ] **Step 1: 迁移三件**

```bash
mkdir -p internal/cli/{dbcopy,exportdata,qconsole}
git mv cmd/dbcopy/main.go internal/cli/dbcopy/run.go
git mv cmd/exportdata/main.go internal/cli/exportdata/run.go
git mv cmd/qconsole/main.go internal/cli/qconsole/run.go
```

按上表完成 `package`/`Run`/flag/exit 改造。

- [ ] **Step 2: 注册三件**

```go
{Name: "dbcopy", Summary: "sqlite→postgres 一次性全量迁移", Run: dbcopy.Run},
{Name: "exportdata", Summary: "学习环训练数据导出 JSONL", Run: exportdata.Run},
{Name: "qconsole", Summary: "审查质量控制台静态页/文本", Run: qconsole.Run},
```

- [ ] **Step 3: 验证**

```bash
go test ./internal/cli/... -count=1
go run . exportdata -h
go run . qconsole -h
go run . dbcopy            # 无 ARGUS_SRC_DSN/ARGUS_DST_DSN → 2
```

Expected：测试 PASS；`-h` 0；`dbcopy` 2。

- [ ] **Step 4: 提交**

```bash
git add internal/cli cmd
git commit -m "refactor(cli): 运维三件迁入 internal/cli 子命令"
```

---

### Task 4: 迁移 ocrshim 并打通 argv0 兼容

**Files:**
- Move: `cmd/ocrshim/main.go` → `internal/cli/ocrshim/run.go`
- Modify: `internal/cli/cli.go`
- Modify: `main_dispatch_test.go`（新增 argv0 路由测试）

**Interfaces:**
- Produces: `ocrshim.Run(args []string) int`（阻塞式 HTTP server；启动失败返回 1）
- Consumes: Task 1 的 `runWith` argv0 分支

- [ ] **Step 1: 迁移与改造**

```bash
mkdir -p internal/cli/ocrshim
git mv cmd/ocrshim/main.go internal/cli/ocrshim/run.go
```

`package main` → `package ocrshim`；`func main()` → `func Run(args []string) int`；
`flag.*` → `fs := flag.NewFlagSet("argus ocrshim", flag.ContinueOnError)` + `fs.*`；
`fs.Parse(args)` 错误（`ErrHelp`→0，其余→2）；`srv.ListenAndServe()` 错误 → `return 1`。
包注释中的用法示例 `ocrshim -listen ...` 改为 `argus ocrshim -listen ...`。

- [ ] **Step 2: 注册**

```go
{Name: "ocrshim", Summary: "OCR MCP sidecar（streamable HTTP）", Run: ocrshim.Run},
```

- [ ] **Step 3: 加 argv0 兼容测试**

```go
func TestRunWithOCRShimArgv0(t *testing.T) {
	var out, errb bytes.Buffer
	if code := runWith([]string{"-h"}, "/tmp/ocrshim", &out, &errb); code != 0 {
		t.Fatalf("argv0=ocrshim -h code = %d, want 0\n%s", code, errb.String())
	}
	if !strings.Contains(errb.String(), "listen") {
		t.Fatalf("未路由到 ocrshim 帮助:\n%s", errb.String())
	}
}
```

- [ ] **Step 4: 验证**

```bash
go test . -run TestRunWithOCRShimArgv0 -count=1
go test ./internal/cli/... -count=1
go run . ocrshim -h
```

Expected：PASS；`-h` 输出含 `-listen`。

- [ ] **Step 5: 提交**

```bash
git add internal/cli cmd main_dispatch_test.go
git commit -m "refactor(cli): ocrshim 迁入子命令并保留 argv0 兼容"
```

---

### Task 5: 迁移 copilot

**Files:**
- Move: `cmd/argus-copilot/{main.go,client.go,poll.go,review.go,review_test.go}` → `internal/cli/copilot/{run.go,client.go,poll.go,review.go,review_test.go}`
- Modify: `internal/cli/cli.go`

**Interfaces:**
- Produces: `copilot.Run(args []string) int`（0/1/2；SIGINT/SIGTERM 仍为 130）

- [ ] **Step 1: 迁移**

```bash
mkdir -p internal/cli/copilot
git mv cmd/argus-copilot/main.go internal/cli/copilot/run.go
git mv cmd/argus-copilot/client.go internal/cli/copilot/client.go
git mv cmd/argus-copilot/poll.go internal/cli/copilot/poll.go
git mv cmd/argus-copilot/review.go internal/cli/copilot/review.go
git mv cmd/argus-copilot/review_test.go internal/cli/copilot/review_test.go
```

- [ ] **Step 2: 改造 `run.go`**

```go
package copilot

func Run(args []string) int {
	g := globals{}
	fs := flag.NewFlagSet("argus copilot", flag.ContinueOnError)
	fs.StringVar(&g.url, "url", envOr("ARGUS_MCP_URL", "http://127.0.0.1:8899/mcp"), "argus MCP 端点（ARGUS_MCP_URL）")
	fs.StringVar(&g.token, "token", os.Getenv("ARGUS_APITOKEN"), "Bearer token（ARGUS_APITOKEN）")
	fs.DurationVar(&g.timeout, "timeout", 30*time.Second, "单次调用超时")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	rest := fs.Args()
	// ... 信号 goroutine 与 ctx 原样 ...
	app := &cli.App{Name: "argus copilot", Commands: commands(g)}
	return app.Run(ctx, rest, os.Stdout, os.Stderr)
}
```

- 包注释与子命令用法示例：`argus-copilot ...` → `argus copilot ...`。
- `review.go` 等内部错误文案 `argus-copilot:` 统一改 `argus copilot:`（`grep` 后逐处改）。
- 信号转发 goroutine 内的 `os.Exit(130)` 保留（显式中断语义）。

- [ ] **Step 3: 注册**

```go
{Name: "copilot", Summary: "MCP 参考客户端（stages/findings/suggest/ask/review/fix/poll）", Run: copilot.Run},
```

- [ ] **Step 4: 验证**

```bash
go test ./internal/cli/copilot/ -count=1
go run . copilot -h
```

Expected：PASS；`-h` 0 且输出全局 flag。

- [ ] **Step 5: 提交**

```bash
git add internal/cli cmd
git commit -m "refactor(cli): copilot 迁入子命令"
```

---

### Task 6: 架构治理与构建/部署面收敛

**Files:**
- Modify: `architecture-policy.yaml`（新增 cli module、ext_prefix、ignore_dirs）
- Modify: `Makefile`
- Modify: `Dockerfile`
- Modify: `deploy/docker-compose/compose.yaml`
- Modify: `deploy/k8s/08-ocrshim-sidecar.yaml.example`
- Modify: `deploy/README.md`、`deploy/ops-runbook.md`、`deploy/k8s43/README.md`
- Modify: `deploy/docker-compose/plugins.yaml`、`plugins.example.yaml`
- Modify: `scripts/install-ocr.sh`
- Modify: `.github/workflows/ci.yml`（镜像 job 注释）
- Modify: `.gitignore`

**Interfaces:**
- Consumes: Task 2/3/4/5 迁出的 `internal/cli` 包
- Produces: `make arch` / `make docs` / `make build` 全绿；镜像单产物

- [ ] **Step 1: policy 增加 cli 模块**

在 `modules:` 段加入：

```yaml
  - id: cli
    roots: [internal/cli]
    requires: [config, execx, learning, ocrshim, qconsole, storage, ext:github.com/glebarez/sqlite, ext:github.com/mark3labs/mcp-go, ext:github.com/oekit/concurrency, ext:github.com/oekit/core, ext:github.com/oekit/logx, ext:github.com/oekit/pkg, ext:github.com/yi-nology/goagent-kernel, ext:golang.org/x/tools/cover, ext:gorm.io/driver, ext:gorm.io/gorm]
    owner: argus
    managed: true
    notes: 单二进制子命令入口（服务启动仍在仓根 main.go）
```

`ext_prefixes:` 增加：

```yaml
    - golang.org/x/tools/cover
```

`ignore_dirs: [cmd, biz, test, third_party]` → `ignore_dirs: [biz, test, third_party]`；
头部注释「cmd/biz/test 等生成面」改为「biz/test 等生成面」。

- [ ] **Step 2: Makefile 收敛**

- `.PHONY` 删 `build-ocrshim`。
- 删 help 行 `make build-ocrshim ...` 与 `build-ocrshim:` 目标。
- `arch` → `go run . archcheck`；`arch-baseline` → `go run . archcheck --write-baseline`；
  `cover` → `go run . covergate`；`cover-baseline` → `go run . covergate -write`；
  `docs` → `go run . docgen`；`docs-check` → `go run . docgen -check`；
  `modcheck` → `go run . modcheck`。

- [ ] **Step 3: Dockerfile 单产物**

- 删除第 26 行 ocrshim 构建；保留单条 `go build ... -o /out/argus .`。
- 删除 `COPY --from=builder /out/ocrshim /app/ocrshim`，改为：

```dockerfile
COPY --from=builder /out/argus /app/argus
# 旧 sidecar 清单以 /app/ocrshim 启动；单二进制 + argv0 路由保持兼容。
RUN ln -s /app/argus /app/ocrshim
```

- [ ] **Step 4: 部署清单/脚本**

- `compose.yaml` ocrshim 服务：`entrypoint: ["/app/argus", "ocrshim"]`；同段注释「同一镜像双二进制」改「单二进制子命令」。
- `deploy/k8s/08-ocrshim-sidecar.yaml.example`：`args:` 首项加 `- "ocrshim"`；镜像注释改为「单二进制（/app/argus + argv0 兼容 symlink）」。
- `deploy/README.md`：`ocrshim` sidecar 描述改为 `argus ocrshim`；「同 tag 双二进制」措辞改为「同一镜像单二进制」。
- `deploy/ops-runbook.md`：`/app/ocrshim` → `/app/argus ocrshim`（保留一句 symlink 兼容说明）；「同镜像双二进制」→「单二进制」。
- `deploy/k8s43/README.md`：`cmd/dbcopy`/`/app/dbcopy` → `argus dbcopy`。
- `scripts/install-ocr.sh` 末尾提示：`ocrshim -ocr-bin ...` → `argus ocrshim -ocr-bin ...`。
- `plugins.example.yaml`、`deploy/docker-compose/plugins.yaml`：`cmd/ocrshim` → `argus ocrshim`。
- `.github/workflows/ci.yml` 镜像 job 注释「产出 argus + ocrshim」→「产出单个 argus 二进制」。
- `.gitignore` 删除 `/argus-copilot`、`/dbcopy` 两段。

- [ ] **Step 5: 重新生成并验证架构/文档/构建**

```bash
make docs
make docs-check
make arch
make modcheck
make build
./bin/argus help
./bin/argus version
./bin/argus archcheck
```

Expected：全 0；`help` 列出 10 个子命令；`archcheck` 通过（含 `cli` module）。

- [ ] **Step 6: 提交**

```bash
git add architecture-policy.yaml Makefile Dockerfile deploy plugins.example.yaml scripts .github .gitignore llms.txt ARCHITECTURE.md
git commit -m "build: 单二进制入口的治理与部署面收敛"
```

---

### Task 7: 文档四层迁移

**Files:**
- Create: `docs/README.md`
- Move: 见下表（不含 `docs/superpowers`，归档放 Task 10）
- Delete: `-idl/`（误建跟踪目录）

**Interfaces:**
- Produces: 四层目录与索引，供 Task 8/9/10 引用

- [ ] **Step 1: 建目录并迁移**

```bash
mkdir -p docs/guide docs/ops docs/design docs/archive/reports docs/archive/releases docs/archive/research

git mv design.md docs/design/design.md
git mv PILOT.md docs/ops/pilot.md
git mv review-service-integration.md docs/archive/review-service-integration.md

git mv docs/API-COMPAT.md docs/guide/API-COMPAT.md
git mv docs/github-app-setup.md docs/guide/github-app-setup.md
git mv docs/ide-mcp.md docs/guide/ide-mcp.md
git mv docs/plugin-authoring.md docs/guide/plugin-authoring.md
git mv docs/qconsole.md docs/guide/qconsole.md

git mv docs/architecture-deep-dive.md docs/design/architecture-deep-dive.md
git mv docs/review-closed-loop-design.md docs/design/review-closed-loop-design.md
git mv docs/roadmap.md docs/design/roadmap.md
git mv docs/tech-debt.md docs/design/tech-debt.md

git mv docs/deploy-production.md docs/ops/deploy-production.md
git mv docs/chaos-matrix.md docs/ops/chaos-matrix.md
git mv docs/quality-ops.md docs/ops/quality-ops.md
git mv docs/compaction-playbook.md docs/ops/compaction-playbook.md
git mv docs/eval-baseline.md docs/ops/eval-baseline.md
git mv docs/skills-release.md docs/ops/skills-release.md
git mv deploy/ops-runbook.md docs/ops/runbook.md

git mv docs/deploy-215.md docs/archive/deploy-215.md
git mv docs/execution-13items.md docs/archive/execution-13items.md
git mv docs/verification-2026-11-15-v730.md docs/archive/reports/verification-2026-11-15-v730.md
git mv docs/archive/2026-11-11-arch-hygiene-report.md docs/archive/reports/2026-11-11-arch-hygiene-report.md
git mv docs/releases/*.md docs/archive/releases/
git mv research/mimo-vs-argus docs/archive/research/mimo-vs-argus
git rm -r -- ./-idl
rmdir docs/releases 2>/dev/null || true
```

（`rmdir` 失败无害；`docs/releases` 若已空则被 git 忽略。）

- [ ] **Step 2: 新建 `docs/README.md`（索引全文）**

```markdown
# Argus 文档索引

> 四层结构：`guide/` 用户与接入 · `ops/` 运维与交付 · `design/` 设计与契约 ·
> `archive/` 过程与历史。根入口：[README](../README.md) · [ARCHITECTURE](../ARCHITECTURE.md) ·
> [CHANGELOG](../CHANGELOG.md) · [CONTRIBUTING](../CONTRIBUTING.md) · [SECURITY](../SECURITY.md)。

## Guide（用户与接入）

| 文档 | 状态 | 说明 |
|---|---|---|
| [`guide/ide-mcp.md`](guide/ide-mcp.md) | 现行 | IDE / MCP 客户端接入 |
| [`guide/API-COMPAT.md`](guide/API-COMPAT.md) | 现行 | 对外契约稳定性分级与破坏性变更流程 |
| [`guide/plugin-authoring.md`](guide/plugin-authoring.md) | 现行 | 插件编写指南 |
| [`guide/github-app-setup.md`](guide/github-app-setup.md) | 现行 | GitHub App 配置 |
| [`guide/qconsole.md`](guide/qconsole.md) | 现行 | 质量控制台 CLI 用法 |

## Ops（运维与交付）

| 文档 | 状态 | 说明 |
|---|---|---|
| [`ops/deploy-production.md`](ops/deploy-production.md) | 现行 | 生产部署规范（Helm + 手工） |
| [`ops/runbook.md`](ops/runbook.md) | 现行 | 运维手册与故障排查 |
| [`ops/pilot.md`](ops/pilot.md) | 现行 | 试点上线与验收清单 |
| [`ops/chaos-matrix.md`](ops/chaos-matrix.md) | 现行 | 混沌测试矩阵 |
| [`ops/quality-ops.md`](ops/quality-ops.md) | 现行 | 质量运营口径 |
| [`ops/compaction-playbook.md`](ops/compaction-playbook.md) | 现行 | 上下文压缩处置手册 |
| [`ops/eval-baseline.md`](ops/eval-baseline.md) | 现行 | 评测基线口径 |
| [`ops/skills-release.md`](ops/skills-release.md) | 现行 | Skills 发布管理 |

## Design（设计与契约）

| 文档 | 状态 | 说明 |
|---|---|---|
| [`design/design.md`](design/design.md) | 设计契约 | 六轮评审定稿的原始设计 |
| [`design/architecture-deep-dive.md`](design/architecture-deep-dive.md) | 现行 | 架构演进深读 |
| [`design/review-closed-loop-design.md`](design/review-closed-loop-design.md) | 现行 | 审查闭环设计 |
| [`design/roadmap.md`](design/roadmap.md) | 现行 | v7.4–v8.0 演进路线 |
| [`design/tech-debt.md`](design/tech-debt.md) | 现行 | 工程债账本 |

## Archive（过程与历史）

- [`archive/deploy-215.md`](archive/deploy-215.md) — 215 单机部署历史（现役 43 k3s）
- [`archive/execution-13items.md`](archive/execution-13items.md) — 13 项执行清单
- [`archive/review-service-integration.md`](archive/review-service-integration.md) — review-service 集成（已停用）
- [`archive/reports/`](archive/reports/) — 验证/审计报告
- [`archive/releases/`](archive/releases/) — 历史发布说明
- [`archive/research/`](archive/research/) — 竞品调研
- `archive/superpowers/` — 历史 spec/plan（Task 10 归档）

## 旁路文档

- [`../deploy/README.md`](../deploy/README.md) — 部署资产目录说明
- [`../deploy/k8s/README-scaling.md`](../deploy/k8s/README-scaling.md) — 扩容说明
- `../knowledge/` — 运行时提示词资产（审查标准/安全检查单，不是用户文档）
- `../web/` — 宣传站（VuePress）
- `../eval/` — 评测集与实验记录

## 维护约定

- 新用户文档进 `guide/`，运维/交付进 `ops/`，设计与路线进 `design/`；完成的批次报告、
  一次性验证、被替代的部署文档进 `archive/`。
- `docs/superpowers/` 是 superpowers skill 的工作目录；批次收尾时整体归档到
  `docs/archive/superpowers/`。
- `make docs-check` 校验：生成物一致 + 本索引覆盖 guide/ops/design 全部 `.md` +
  非 archive 文档的相对 `.md` 链接可解析。
```

- [ ] **Step 3: 验证目录**

Run: `find docs -maxdepth 2 -type f | sort && git status --short`
Expected：四层文件就位；`docs/releases`、旧 `docs/*.md` 不再存在。

- [ ] **Step 4: 提交**

```bash
git add -A -- .
git commit -m "docs: 文档重组为 guide/ops/design/archive 四层"
```

---

### Task 8: 全仓引用与命令示例重写

**Files（逐项检查/修改）:**
- `README.md`、`ARCHITECTURE.md`、`docs/design/design.md`、`main.go`
- `deploy/README.md`、`deploy/k8s43/README.md`、`deploy/k8s43/90-pg-restore-drill.sh`、
  `deploy/migrations/schema.sql`、`deploy/helm/argus/**`、`deploy/k8s/01-secret.yaml`
- `internal/learning/learning.go`、`internal/storage/patterns.go`、`internal/dream/dream.go`、
  `internal/workflow/requirement/{reflection.go,workflow_impl_r5.go}`、
  `internal/bootstrap/subsys_tickers.go`
- `internal/qualityops/{qualityops.go,qualityops_test.go}`
- `docs/guide/ide-mcp.md`、`docs/guide/qconsole.md`、`docs/design/architecture-deep-dive.md`、
  `docs/design/tech-debt.md`、`docs/archive/execution-13items.md`（仅链接路径）
- `skills/argus-mcp/SKILL.md`、`skills/argus-mcp/references/tools.md`
- `web/architecture.md`、`web/skills.md`
- `internal/cli/docgen/run.go`（llms 文档清单）
- `.github/workflows/ci.yml`（release notes 路径）

**Interfaces:**
- Consumes: Task 7 的最终路径
- Produces: 全仓不再有指向旧路径的活引用；docgen 清单与新结构一致

- [ ] **Step 1: 批量替换文档路径（只动非 archive、非 CHANGELOG）**

```bash
files=$(git grep -l -E 'docs/(API-COMPAT|github-app-setup|ide-mcp|plugin-authoring|qconsole|architecture-deep-dive|review-closed-loop-design|roadmap|tech-debt|deploy-production|chaos-matrix|quality-ops|compaction-playbook|eval-baseline|skills-release|deploy-215|execution-13items|verification-2026-11-15-v730)' -- ':!docs/archive' ':!CHANGELOG.md' ':!docs/superpowers')
perl -pi -e '
  s{docs/API-COMPAT\.md}{docs/guide/API-COMPAT.md}g;
  s{docs/github-app-setup\.md}{docs/guide/github-app-setup.md}g;
  s{docs/ide-mcp\.md}{docs/guide/ide-mcp.md}g;
  s{docs/plugin-authoring\.md}{docs/guide/plugin-authoring.md}g;
  s{docs/qconsole\.md}{docs/guide/qconsole.md}g;
  s{docs/architecture-deep-dive\.md}{docs/design/architecture-deep-dive.md}g;
  s{docs/review-closed-loop-design\.md}{docs/design/review-closed-loop-design.md}g;
  s{docs/roadmap\.md}{docs/design/roadmap.md}g;
  s{docs/tech-debt\.md}{docs/design/tech-debt.md}g;
  s{docs/deploy-production\.md}{docs/ops/deploy-production.md}g;
  s{docs/chaos-matrix\.md}{docs/ops/chaos-matrix.md}g;
  s{docs/quality-ops\.md}{docs/ops/quality-ops.md}g;
  s{docs/compaction-playbook\.md}{docs/ops/compaction-playbook.md}g;
  s{docs/eval-baseline\.md}{docs/ops/eval-baseline.md}g;
  s{docs/skills-release\.md}{docs/ops/skills-release.md}g;
  s{docs/deploy-215\.md}{docs/archive/deploy-215.md}g;
  s{docs/execution-13items\.md}{docs/archive/execution-13items.md}g;
  s{docs/verification-2026-11-15-v730\.md}{docs/archive/reports/verification-2026-11-15-v730.md}g;
  s{docs/releases/}{docs/archive/releases/}g;
' $files
```

- [ ] **Step 2: 手工修正上下文敏感的根文档引用**

- `README.md` 第 5 行：`design.md` → `docs/design/design.md`；`PILOT.md` → `docs/ops/pilot.md`。
- `README.md` 第 327 行：`review-service-integration.md` → `docs/archive/review-service-integration.md`（并注明「已停用」）。
- `README.md` 第 629/696/736 行：同路径替换；第 736 行链接改为
  `[design](docs/design/design.md)`、`[试点手册](docs/ops/pilot.md)`。
- `ARCHITECTURE.md` 第 3 行：
  `**design.md**` → `**[docs/design/design.md](docs/design/design.md)**`；
  `**docs/architecture-deep-dive.md**` → `**[docs/design/architecture-deep-dive.md](docs/design/architecture-deep-dive.md)**`。
- `docs/design/design.md` 第 7 行：`[README](README.md)` → `[README](../../README.md)`；
  `[架构深读](docs/architecture-deep-dive.md)` 相对链接改为
  `[架构深读](architecture-deep-dive.md)`。
- `main.go` 第 1 行：`design.md v3.0` → `docs/design/design.md v3.0`。
- `deploy/README.md` 第 87 行：`PILOT.md §7` → `docs/ops/pilot.md §7`。
- `README.md` 目录导览 §九：把 `cmd/*` 条目替换为
  `cmd/ 已收编为 internal/cli/（argus <command>）`，并把 `docs/` 条目改为四层说明。
- `docs/design/architecture-deep-dive.md` 第 286 行：`cmd/argus-copilot` → `argus copilot`。

- [ ] **Step 3: 命令示例统一为 `argus <cmd>` / `go run . <cmd>`**

逐文件替换：

- `README.md`：`go run ./cmd/exportdata` → `go run . exportdata`；
  `cmd/argus-copilot` → `argus copilot`；`go run ./cmd/argus-copilot` → `go run . copilot`；
  `cmd/qconsole` → `argus qconsole`。
- `docs/guide/ide-mcp.md`：`cmd/argus-copilot` → `argus copilot`；
  `go run ./cmd/argus-copilot` → `go run . copilot`。
- `docs/guide/qconsole.md`：`cmd/qconsole` → `argus qconsole`；
  `go run ./cmd/qconsole` → `go run . qconsole`。
- `skills/argus-mcp/SKILL.md`、`skills/argus-mcp/references/tools.md`：
  `go run ./cmd/argus-copilot` → `go run . copilot`；
  二进制名 `argus-copilot` → `argus copilot`。
- `deploy/k8s43/README.md`：`cmd/dbcopy`、`/app/dbcopy` → `argus dbcopy`。
- `scripts/install-ocr.sh`：`ocrshim` 命令提示 → `argus ocrshim`（若 Task 6 未覆盖彻底）。

- [ ] **Step 4: 更新 docgen 的 llms 文档清单**

`internal/cli/docgen/run.go` 中原：

```go
b.WriteString("- `README.md`\n- `ARCHITECTURE.md`\n- `design.md`\n")
b.WriteString("- `docs/API-COMPAT.md`（对外契约稳定性分级与破坏性变更流程）\n")
b.WriteString("- `docs/superpowers/specs/2026-11-15-kernel-method-refactor-design.md`\n\n")
```

改为：

```go
b.WriteString("- `docs/README.md`（四层索引：guide/ops/design/archive）\n")
b.WriteString("- `docs/guide/API-COMPAT.md`（对外契约稳定性分级与破坏性变更流程）\n")
b.WriteString("- `docs/design/design.md`（设计契约）\n")
b.WriteString("- `docs/design/architecture-deep-dive.md`（架构演进深读）\n")
b.WriteString("- `docs/design/roadmap.md`（演进路线）\n")
b.WriteString("- `docs/archive/superpowers/specs/2026-11-15-kernel-method-refactor-design.md`\n\n")
```

- [ ] **Step 5: 更新 CI release notes 路径与 rest 引用**

- `.github/workflows/ci.yml` 第 149 行注释与第 184 行：
  `docs/releases/${GITHUB_REF_NAME}.md` → `docs/archive/releases/${GITHUB_REF_NAME}.md`。
- `docs/design/tech-debt.md` 中 `docs/releases/v6.0.0.md` → `docs/archive/releases/v6.0.0.md`。
- `internal/qualityops/qualityops.go` 与 `qualityops_test.go`：
  `docs/quality-ops.md` → `docs/ops/quality-ops.md`。
- `internal/bootstrap/subsys_tickers.go`：`deploy/ops-runbook.md` → `docs/ops/runbook.md`。
- `deploy/k8s43/90-pg-restore-drill.sh`、`deploy/migrations/schema.sql`、
  `deploy/helm/argus/{values.yaml,Chart.yaml,templates/*.yaml,templates/NOTES.txt}`：
  按 Step 1 替换后的路径再检查（deploy/ 下多为注释）。
- `web/architecture.md`、`web/skills.md`：
  `blob/main/docs/architecture-deep-dive.md` → `blob/main/docs/design/architecture-deep-dive.md`；
  `blob/main/docs/ide-mcp.md` → `blob/main/docs/guide/ide-mcp.md`。

- [ ] **Step 6: 检查无活引用残留**

```bash
git grep -n -E 'cmd/(archcheck|archgen|argus-copilot|covergate|dbcopy|docgen|exportdata|modcheck|ocrshim|qconsole)' -- ':!docs/archive' ':!CHANGELOG.md' ':!docs/superpowers' || true
git grep -n -E 'docs/(API-COMPAT|github-app-setup|ide-mcp|plugin-authoring|qconsole|architecture-deep-dive|review-closed-loop-design|roadmap|tech-debt|deploy-production|chaos-matrix|quality-ops|compaction-playbook|eval-baseline|skills-release|deploy-215|execution-13items|verification-2026-11-15-v730)' -- ':!docs/archive' ':!CHANGELOG.md' ':!docs/superpowers' || true
git grep -n -E '(^|[^/])design\.md|PILOT\.md|review-service-integration\.md|ops-runbook\.md' -- ':!docs/archive' ':!CHANGELOG.md' ':!docs/superpowers' || true
```

Expected：三条均无输出（或仅剩有意保留的历史说明）。

- [ ] **Step 7: 重新生成并跑测试**

```bash
make docs
make docs-check
make arch
go test ./internal/qualityops/ ./internal/cli/... -count=1
```

Expected：全绿。

- [ ] **Step 8: 提交**

```bash
git add -A
git commit -m "docs: 重写四层路径引用与单二进制命令示例"
```

---

### Task 9: docgen 增加索引完整性与链接门禁

**Files:**
- Create: `internal/cli/docgen/doccheck.go`
- Create: `internal/cli/docgen/doccheck_test.go`
- Modify: `internal/cli/docgen/run.go`（`-check` 分支接入）

**Interfaces:**
- Produces:
  - `checkDocIndex(root string) error`
  - `checkDocLinks(root string) error`
  - `var errDocIssue = errors.New("文档门禁未通过")`
- Consumes: Task 7 的索引与四层目录

- [ ] **Step 1: 写失败测试**

`internal/cli/docgen/doccheck_test.go`：

```go
package docgen

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeFile(t *testing.T, root, rel, content string) {
	t.Helper()
	p := filepath.Join(root, rel)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestCheckDocIndexMissingEntry(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, "docs/README.md", "# 索引\n")
	writeFile(t, root, "docs/guide/a.md", "# A\n")
	err := checkDocIndex(root)
	if !errors.Is(err, errDocIssue) {
		t.Fatalf("err = %v, want errDocIssue", err)
	}
	if !strings.Contains(err.Error(), "docs/guide/a.md") {
		t.Fatalf("err 未点名缺项: %v", err)
	}
}

func TestCheckDocIndexOK(t *testing.T) {
	root := t.TempDir()
	// 索引通常用 docs/README.md 视角的相对路径（guide/a.md），
	// 也接受仓库相对路径（docs/guide/a.md）。
	writeFile(t, root, "docs/README.md", "- `guide/a.md`\n- docs/ops/b.md\n")
	writeFile(t, root, "docs/guide/a.md", "# A\n")
	writeFile(t, root, "docs/ops/b.md", "# B\n")
	if err := checkDocIndex(root); err != nil {
		t.Fatalf("err = %v, want nil", err)
	}
}

func TestCheckDocLinksDetectsBroken(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, "README.md", "[x](docs/guide/missing.md)\n")
	if err := checkDocLinks(root); !errors.Is(err, errDocIssue) {
		t.Fatalf("err = %v, want errDocIssue", err)
	}
}

func TestCheckDocLinksSkipsCodeFenceAndExternal(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, "README.md",
		"```\n[x](docs/guide/missing.md)\n```\n"+
			"[web](https://example.com/x.md) [anchor](#top)\n"+
			"[ok](docs/guide/a.md)\n")
	writeFile(t, root, "docs/guide/a.md", "# A\n")
	if err := checkDocLinks(root); err != nil {
		t.Fatalf("err = %v, want nil", err)
	}
}

func TestCheckDocLinksArchiveExempt(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, "docs/archive/old.md", "[x](gone.md)\n")
	if err := checkDocLinks(root); err != nil {
		t.Fatalf("archive 应豁免: %v", err)
	}
}
```

- [ ] **Step 2: 运行确认失败**

Run: `go test ./internal/cli/docgen/ -run 'TestCheckDoc' -count=1`
Expected: FAIL（函数未定义）

- [ ] **Step 3: 实现 `doccheck.go`**

```go
package docgen

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// errDocIssue 标记文档门禁违规（退出码 1），区别于 IO/工具错误（退出码 2）。
var errDocIssue = errors.New("文档门禁未通过")

var mdLinkRe = regexp.MustCompile(`\[[^\]]*\]\(([^)\s]+)(?:\s+"[^"]*")?\)`)

// checkDocIndex 要求 guide/ops/design 下每个 .md 都被 docs/README.md 引用。
func checkDocIndex(root string) error {
	index, err := os.ReadFile(filepath.Join(root, "docs/README.md"))
	if err != nil {
		return err
	}
	var missing []string
	for _, dir := range []string{"docs/guide", "docs/ops", "docs/design"} {
		base := filepath.Join(root, dir)
		entries, err := os.ReadDir(base)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return err
		}
		for _, e := range entries {
			if e.IsDir() || !strings.HasSuffix(e.Name(), ".md") {
				continue
			}
			rel := filepath.ToSlash(filepath.Join(dir, e.Name())) // docs/guide/a.md
			short := strings.TrimPrefix(rel, "docs/")             // guide/a.md
			if !strings.Contains(string(index), rel) && !strings.Contains(string(index), short) {
				missing = append(missing, rel)
			}
		}
	}
	if len(missing) > 0 {
		return fmt.Errorf("%w: docs/README.md 索引缺少 %s", errDocIssue, strings.Join(missing, ", "))
	}
	return nil
}

// checkDocLinks 校验根入口文档与 docs 非 archive 文档中的相对 .md 链接。
func checkDocLinks(root string) error {
	files := []string{"README.md", "ARCHITECTURE.md", "CONTRIBUTING.md", "SECURITY.md", "deploy/README.md"}
	docsDir := filepath.Join(root, "docs")
	if _, statErr := os.Stat(docsDir); statErr == nil {
		err := filepath.WalkDir(docsDir, func(path string, d os.DirEntry, err error) error {
			if err != nil {
				return err
			}
			rel, _ := filepath.Rel(root, path)
			rel = filepath.ToSlash(rel)
			if d.IsDir() {
				if rel == "docs/archive" || rel == "docs/superpowers" {
					return filepath.SkipDir
				}
				return nil
			}
			if strings.HasSuffix(rel, ".md") {
				files = append(files, rel)
			}
			return nil
		})
		if err != nil {
			return err
		}
	} else if !os.IsNotExist(statErr) {
		return statErr
	}
	var issues []string
	for _, rel := range files {
		raw, err := os.ReadFile(filepath.Join(root, rel))
		if err != nil {
			return err
		}
		for _, m := range mdLinkRe.FindAllStringSubmatchIndex(string(raw), -1) {
			target := string(raw[m[2]:m[3]])
			if strings.HasPrefix(target, "http://") || strings.HasPrefix(target, "https://") ||
				strings.HasPrefix(target, "mailto:") || strings.HasPrefix(target, "#") ||
				strings.HasPrefix(target, "/") {
				continue
			}
			target = strings.SplitN(target, "#", 2)[0]
			if !strings.HasSuffix(strings.ToLower(target), ".md") {
				continue
			}
			resolved := filepath.Clean(filepath.Join(root, filepath.Dir(rel), target))
			if _, err := os.Stat(resolved); err != nil {
				line := 1 + strings.Count(string(raw[:m[0]]), "\n")
				issues = append(issues, fmt.Sprintf("%s:%d → %s", rel, line, target))
			}
		}
	}
	if len(issues) > 0 {
		return fmt.Errorf("%w:\n%s", errDocIssue, strings.Join(issues, "\n"))
	}
	return nil
}
```

- [ ] **Step 4: 接入 `-check` 分支**

`run.go` 中 `if *check { ... }` 改为：

```go
if *check {
	if err := checkDocs(".", files); err != nil {
		fmt.Fprintln(os.Stderr, err)
		if errors.Is(err, errDrift) {
			return 1
		}
		return 2
	}
	if err := checkDocIndex("."); err != nil {
		fmt.Fprintln(os.Stderr, err)
		if errors.Is(err, errDocIssue) {
			return 1
		}
		return 2
	}
	if err := checkDocLinks("."); err != nil {
		fmt.Fprintln(os.Stderr, err)
		if errors.Is(err, errDocIssue) {
			return 1
		}
		return 2
	}
	return 0
}
```

- [ ] **Step 5: 跑测试并修真实断链**

```bash
go test ./internal/cli/docgen/ -count=1
go run . docgen -check
```

Expected：单测 PASS；仓库检查若报断链，逐条修正（常见：docs/guide 相互引用、
archive 迁移后的 `../superpowers/...`、README 目录导览、deploy/README 相对路径）。
索引缺项同样按报错补齐。直到 `go run . docgen -check` 返回 0。

- [ ] **Step 6: 提交**

```bash
git add internal/cli/docgen docs
git commit -m "feat(docgen): 文档索引完整性与相对链接门禁"
```

---

### Task 10: 归档 superpowers 并收口文档引用

**Files:**
- Move: `docs/superpowers/{plans,specs}` → `docs/archive/superpowers/{plans,specs}`
- Modify: `docs/README.md`（archive 小节路径）
- Modify: 引用 `docs/superpowers/...` 的活文档
- Modify: `internal/cli/docgen/run.go`（kernel spec 路径已在 Task 8 指向 archive；确认）

**Interfaces:**
- Consumes: Task 9 的链接门禁
- Produces: 最终四层结构（无 docs/superpowers 顶层）

- [ ] **Step 1: 归档**

```bash
mkdir -p docs/archive/superpowers
git mv docs/superpowers/plans docs/archive/superpowers/plans
git mv docs/superpowers/specs docs/archive/superpowers/specs
rmdir docs/superpowers 2>/dev/null || true
```

- [ ] **Step 2: 更新索引与活引用**

- `docs/README.md` archive 小节：
  `` `archive/superpowers/` — 历史 spec/plan ``（去掉「Task 10 归档」字样）。
- 全仓查找 `docs/superpowers`（排除 archive），逐个改到 `docs/archive/superpowers`：
  `docs/design/roadmap.md`、`docs/design/tech-debt.md`、README/ARCHITECTURE 若引用。
- `internal/cli/docgen/run.go` 的 llms 清单确认已是 `docs/archive/superpowers/specs/...`。

- [ ] **Step 3: 验证**

```bash
go run . docgen -check
find docs -maxdepth 2 -type d | sort
```

Expected：docs-check 0；`docs/superpowers` 不存在；archive 下有 `superpowers/plans|specs`。

- [ ] **Step 4: 提交**

```bash
git add -A
git commit -m "docs: superpowers 过程产物归档到 archive"
```

---

### Task 11: 全量门禁、冒烟与收尾

**Files:** 无新增（修验证中暴露的问题）

- [ ] **Step 1: 全量门禁**

```bash
make cover            # 生成 coverage.out
make verify
```

Expected：vet/lint/test(-race)/arch/cover/eval/crossbuild/modcheck/docs-check 全绿。
若跨平台或 dialect 测试因本机环境失败，记录具体 job 与原因，不静默跳过。

- [ ] **Step 2: 单二进制冒烟**

```bash
make build
./bin/argus help
./bin/argus version
./bin/argus archcheck
./bin/argus docgen -check
./bin/argus modcheck
./bin/argus covergate
ln -sf "$PWD/bin/argus" /tmp/ocrshim && /tmp/ocrshim -h
./bin/argus copilot -h
```

Expected：除 `covergate` 需 `coverage.out`（Step 1 已生成）外全部 0；
`/tmp/ocrshim -h` 输出 ocrshim 的 `-listen` 帮助。

- [ ] **Step 3: 服务启动冒烟（本地临时端口）**

```bash
ARGUS_ADDR=:18899 ARGUS_DB_DSN=/tmp/argus-smoke.db ./bin/argus &
sleep 2
curl -fsS http://127.0.0.1:18899/healthz && echo
curl -fsS http://127.0.0.1:18899/version && echo
kill %1
rm -f /tmp/argus-smoke.db
```

Expected：healthz 200 信封；version 含 `dev`（未注入 tag 时）。若缺少 LLM/PG 依赖，
仅验证进程启动与 healthz，readyz 的 db 检查可能 503，属预期，记录即可。

- [ ] **Step 4: 工作树与提交**

```bash
git status --short
git log --oneline main..HEAD
```

Expected：工作树干净（仅剩本地未跟踪产物不入库）；提交序列覆盖 Task 1–10。
如冒烟暴露修复，单独提交 `fix: ...`。

- [ ] **Step 5: 收尾说明**

记录到最终回复：分支名、提交数、`make verify` 结果、冒烟结果、已知未覆盖项
（如 Docker 镜像未在本机构建、43 线上未换芯）。