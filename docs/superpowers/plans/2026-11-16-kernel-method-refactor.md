# goagent-kernel 方法论移植 Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 把 goagent-kernel 的治理方法与机制包移植进 argus：声明式架构门禁、结构清债、按判据收编能力包；不满足项提 issue。

**Architecture:** A 轨先建 `architecture-policy.yaml` + `cmd/archcheck`/`covergate`/`docgen`，用 policy 取代手写分层白名单；B 轨在门禁棘轮下拆 31 个超限文件、收口 exec；C 轨升 kernel v0.38.0，按"机制 vs 领域"逐包决策，采用 netguard/sideeffect/reconcile/finalize。

**Tech Stack:** Go 1.27.4、`github.com/yi-nology/goagent-kernel v0.38.0`（`archcheck` 库）、`golang.org/x/tools/cover`、oekit/oagentkit、GitHub Actions。

**Spec:** `docs/superpowers/specs/2026-11-15-kernel-method-refactor-design.md`

## Global Constraints

- 生产代码行为零变化：A/B 轨只做门禁与结构移动；C 轨行为变化点必须有配置开关 + 实弹用例。
- 每批结束必须 `make test`（-race）、`make lint`、`make arch`、`make eval` 全绿。
- 禁止 `git add -A`；每批按文件名精确提交，提交前 `git status` 核对（并行会话纪律）。
- kernel 只 import 公开包；禁止 `goagent-kernel/internal/*`、`oekit/*/internal/*`、`oagentkit/*/internal/*`。
- 覆盖率与架构阈值只降不升：`coverage-baseline.json`、`arch-thresholds.baseline`、`arch-exceptions.baseline` 只能收缩。
- 不满足/本期不做项必须进 §Issue 台账并提 issue，不允许静默丢弃。

---

### Task 0: 基线测量

**Files:**
- Create: `docs/verify/2026-11-16-refactor-baseline.md`

- [ ] **Step 1: 记录 git 状态与门禁基线**

Run:
```bash
git status --short
make lint
make test
make arch
go test ./internal/... -coverprofile=/tmp/argus-cover.out -count=1
go tool cover -func=/tmp/argus-cover.out | tail -1
```
Expected: 记录当前 lint/test/arch 结果与全局覆盖率；若有既有红，先记入基线文档，不归因于本重构。

- [ ] **Step 2: 写基线文档并提交**

```bash
git add docs/verify/2026-11-16-refactor-baseline.md
git commit -m "docs(verify): 重构基线——lint/test/arch/覆盖率实测"
```

---

### Task 1: 生成 architecture-policy.yaml

**Files:**
- Create: `cmd/archgen/main.go`
- Create: `cmd/archgen/main_test.go`
- Create: `architecture-policy.yaml`
- Modify: `test/arch/arch_test.go`（Task 2 同步退役 layering）

**Interfaces:**
- Produces: `architecture-policy.yaml`，48 个 module + rules + thresholds + 空 exceptions；`cmd/archgen` 可重复生成 modules 段。

- [ ] **Step 1: 写 module ID / import 归类测试**

`cmd/archgen/main_test.go`:
```go
package main

import "testing"

func TestModuleID(t *testing.T) {
	cases := map[string]string{
		"github.com/yi-nology/argus/internal/codesearch":            "codesearch",
		"github.com/yi-nology/argus/internal/workflow/requirement":  "requirement",
		"github.com/yi-nology/argus/internal/plugin/adapter_acpx":   "adapter_acpx",
		"github.com/yi-nology/argus/internal/channel/mcpserver":     "mcpserver",
		"github.com/yi-nology/argus/internal/notify/octopus":        "octopus",
	}
	for in, want := range cases {
		if got := moduleID(in); got != want {
			t.Fatalf("moduleID(%q)=%q want %q", in, got, want)
		}
	}
}

func TestExtRoot(t *testing.T) {
	cases := map[string]string{
		"github.com/oekit/pkg/errors":            "github.com/oekit/pkg",
		"github.com/oagentkit/runtime/progress":  "github.com/oagentkit/runtime",
		"github.com/yi-nology/goagent-kernel/spill": "github.com/yi-nology/goagent-kernel",
		"gorm.io/gorm/clause":                    "gorm.io/gorm",
		"github.com/google/uuid":                 "github.com/google/uuid",
	}
	for in, want := range cases {
		if got := extRoot(in); got != want {
			t.Fatalf("extRoot(%q)=%q want %q", in, got, want)
		}
	}
}
```

- [ ] **Step 2: 跑测试确认失败**

Run: `go test ./cmd/archgen/ -run Test -v`
Expected: FAIL（`moduleID`/`extRoot` 未定义）。

- [ ] **Step 3: 实现生成器**

`cmd/archgen/main.go`:
```go
package main

import (
	"fmt"
	"os/exec"
	"sort"
	"strings"
)

func moduleID(importPath string) string {
	base := importPath[strings.LastIndex(importPath, "/")+1:]
	switch base {
	case "requirement":
		return "requirement"
	}
	return base
}

func extRoot(importPath string) string {
	parts := strings.Split(importPath, "/")
	switch {
	case strings.HasPrefix(importPath, "github.com/"):
		if len(parts) >= 3 {
			return strings.Join(parts[:3], "/")
		}
	case strings.HasPrefix(importPath, "gorm.io/"):
		if len(parts) >= 2 {
			return strings.Join(parts[:2], "/")
		}
	}
	return importPath
}

func dedup(in []string) []string {
	out := in[:0]
	for i, s := range in {
		if i == 0 || s != in[i-1] {
			out = append(out, s)
		}
	}
	return out
}

func main() {
	cmd := exec.Command("go", "list", "-f", `{{.ImportPath}}|{{join .Imports ","}}`, "./internal/...")
	out, err := cmd.Output()
	if err != nil {
		panic(err)
	}
	var b strings.Builder
	b.WriteString("modules:\n")
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		parts := strings.SplitN(line, "|", 2)
		id := moduleID(parts[0])
		var reqs []string
		if len(parts) == 2 && parts[1] != "" {
			for _, imp := range strings.Split(parts[1], ",") {
				switch {
				case strings.HasPrefix(imp, "github.com/yi-nology/argus/internal/"):
					reqs = append(reqs, moduleID(imp))
				case strings.HasPrefix(imp, "github.com/yi-nology/argus/"):
					// biz/router 等生成面：不登记为 module
				case strings.Contains(imp, "."):
					reqs = append(reqs, "ext:"+extRoot(imp))
				}
			}
		}
		sort.Strings(reqs)
		reqs = dedup(reqs)
		fmt.Fprintf(&b, "  - id: %s\n    roots: [%s]\n    requires: [%s]\n    owner: argus\n    managed: true\n",
			id, strings.TrimPrefix(parts[0], "github.com/yi-nology/argus/"), strings.Join(reqs, ", "))
	}
	fmt.Print(b.String())
}
```
> 生成器**只输出 modules 段到 stdout**，不覆盖 policy 文件——避免重跑时把 rules/thresholds/exceptions 冲掉。

- [ ] **Step 4: 生成并评审 policy**

Run:
```bash
go run ./cmd/archgen > /tmp/modules.yaml
grep -c '^  - id:' /tmp/modules.yaml
```
Expected: 48 个 `- id:`；`internal/workflow/requirement` 的 id 是 `requirement`；无重复 id；每个 module 的 `requires` 只含已登记 module id 或 `ext:` 前缀。把 `/tmp/modules.yaml` 的 modules 段并入 `architecture-policy.yaml` 骨架。

- [ ] **Step 5: 补 rules/thresholds/ext_prefixes**

按 spec §3.2 粘贴 6 条规则、`dependency_direction`、`thresholds`（`enforce: false`，先报告）、`execution_paths`/`egress_paths`（均 `enabled: false`，注明拆除条件）。`ext_prefixes` 取 modules 中所有 `ext:` 条目的根前缀并集。

- [ ] **Step 6: 提交**

```bash
git add cmd/archgen architecture-policy.yaml
git commit -m "feat(arch): 声明式架构策略——48 module + 规则 + 门槛"
```

---

### Task 2: archcheck 门禁运行器

**Files:**
- Create: `cmd/archcheck/main.go`
- Create: `cmd/archcheck/main_test.go`
- Modify: `Makefile`
- Modify: `.github/workflows/test.yml`
- Modify: `test/arch/arch_test.go`

**Interfaces:**
- Consumes: `architecture-policy.yaml`、`arch-thresholds.baseline`、`arch-exceptions.baseline`
- Produces: `make arch`；`cmd/archcheck --max-exceptions N --thresholds-baseline path`

- [ ] **Step 1: 写基线比较测试**

`cmd/archcheck/main_test.go`:
```go
package main

import "testing"

func TestNewFindings(t *testing.T) {
	base := []string{"a.go:510 max_file_lines", "b.go:620 max_file_lines"}
	cur := []string{"a.go:510 max_file_lines"}
	if got := newFindings(cur, base); len(got) != 0 {
		t.Fatalf("shrink should pass, got %v", got)
	}
	cur = append(cur, "c.go:700 max_file_lines")
	if got := newFindings(cur, base); len(got) != 1 {
		t.Fatalf("new finding must fail, got %v", got)
	}
}
```

- [ ] **Step 2: 跑测试确认失败**

Run: `go test ./cmd/archcheck/ -run TestNewFindings -v`
Expected: FAIL（`newFindings` 未定义）。

- [ ] **Step 3: 实现**

`cmd/archcheck/main.go`:
```go
package main

import (
	"bufio"
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/yi-nology/goagent-kernel/archcheck"
)

func newFindings(cur, base []string) []string {
	set := map[string]bool{}
	for _, b := range base { set[b] = true }
	var out []string
	for _, c := range cur {
		if !set[c] { out = append(out, c) }
	}
	return out
}

func readLines(path string) []string {
	f, err := os.Open(path)
	if err != nil { return nil }
	defer f.Close()
	var out []string
	s := bufio.NewScanner(f)
	for s.Scan() { if t := strings.TrimSpace(s.Text()); t != "" && !strings.HasPrefix(t, "#") { out = append(out, t) } }
	return out
}

func main() {
policy := flag.String("policy", "architecture-policy.yaml", "")
	maxEx := flag.Int("max-exceptions", -1, "")
	thr := flag.String("thresholds-baseline", "arch-thresholds.baseline", "")
	writeBase := flag.Bool("write-baseline", false, "")
	flag.Parse()

	pol, err := archcheck.Load(*policy)
	if err != nil { fmt.Fprintln(os.Stderr, "policy 加载失败:", err); os.Exit(2) }
	res, err := archcheck.Run(".", pol)
	if err != nil { fmt.Fprintln(os.Stderr, "门禁给不出结论:", err); os.Exit(2) }

	for _, v := range res.Violations {
		fmt.Printf("%s:%d [%s] %s\n", v.Path, v.Line, v.Rule, v.Text)
	}
	for _, f := range res.ThresholdFindings { fmt.Println("THRESHOLD", f) }
	fmt.Printf("blocking=%d warnings=%d excused=%d exceptions=%d\n",
		res.Blocking, res.Warnings, res.Excused, len(res.Exceptions))

	if *writeBase {
		old := readLines(*thr)
		oldSet := map[string]bool{}
		for _, s := range old {
			oldSet[s] = true
		}
		for _, f := range res.ThresholdFindings {
			if len(old) > 0 && !oldSet[f] {
				fmt.Fprintln(os.Stderr, "新阈值发现不在旧基线内，拒绝写基线（只允许收缩）:", f)
				os.Exit(1)
			}
		}
		body := strings.Join(res.ThresholdFindings, "\n")
		if body != "" {
			body += "\n"
		}
		os.WriteFile(*thr, []byte(body), 0o644)
		os.WriteFile("arch-exceptions.baseline", []byte(fmt.Sprintf("%d\n", len(res.Exceptions))), 0o644)
		return
	}

	fail := false
	if *maxEx >= 0 && len(res.Exceptions) > *maxEx {
		fmt.Fprintf(os.Stderr, "例外数 %d 超过基线 %d\n", len(res.Exceptions), *maxEx); fail = true
	}
	if *thr != "" {
		if nf := newFindings(res.ThresholdFindings, readLines(*thr)); len(nf) > 0 {
			fmt.Fprintln(os.Stderr, "出现基线外的新阈值发现：")
			for _, f := range nf { fmt.Fprintln(os.Stderr, f) }
			fail = true
		}
	}
	if res.HasBlocking() || fail { os.Exit(1) }
}
```

- [ ] **Step 4: 跑测试**

Run: `go test ./cmd/archcheck/ -v`
Expected: PASS。

- [ ] **Step 5: 接 Makefile 与 CI**

`Makefile`:
```make
arch:
	go run ./cmd/archcheck --max-exceptions $(shell cat arch-exceptions.baseline 2>/dev/null || echo -1) \
		--thresholds-baseline arch-thresholds.baseline
	go test ./test/arch/ -count=1 -run TestPackageDocs

arch-baseline:
	go run ./cmd/archcheck --write-baseline
```
（`archgen -baseline` 需实现为：跑 archcheck，写 `arch-thresholds.baseline` 与当前例外数，且新值必须是旧值的子集。）

CI：`test.yml` 的 arch job 改为 `make arch`；保留 gofmt/vet/staticcheck 原 job。

- [ ] **Step 6: 退役 test/arch 的 layering**

删除 `deps`/`TestMain`/`layering`/`TestLayering`/`TestBootstrapNotImported`，保留 `TestPackageDocs`。若 `TestMain` 仅服务于 layering 则一并删除。

- [ ] **Step 7: 验收注入**

Run:
```bash
# 临时在 internal/codesearch 加一行 import bootstrap，确认 make arch 红；还原
make arch
```
Expected: 红→还原后绿。

- [ ] **Step 8: 提交**

```bash
git add cmd/archcheck Makefile .github/workflows/test.yml test/arch/arch_test.go arch-thresholds.baseline arch-exceptions.baseline
git commit -m "feat(arch): archcheck 门禁——policy 执行 + 阈值/例外棘轮"
```

---

### Task 3: 覆盖率棘轮

**Files:**
- Create: `cmd/covergate/main.go`
- Create: `cmd/covergate/main_test.go`
- Create: `coverage-baseline.json`
- Modify: `Makefile`、`.github/workflows/test.yml`

**Interfaces:**
- Consumes: `coverage.out`、`coverage-baseline.json`
- Produces: `make cover`、`make cover-baseline`

- [ ] **Step 1: 写比较测试**

`cmd/covergate/main_test.go`:
```go
package main

import "testing"

func TestCompare(t *testing.T) {
	base := Baseline{Global: 0.40, Packages: map[string]float64{"internal/a": 0.70}}
	cur := Baseline{Global: 0.41, Packages: map[string]float64{"internal/a": 0.69}}
	if bad := compare(base, cur); len(bad) == 0 {
		t.Fatal("package drop must fail")
	}
	cur = Baseline{Global: 0.41, Packages: map[string]float64{"internal/a": 0.71, "internal/new": 0.65}}
	if bad := compare(base, cur); len(bad) == 0 {
		t.Fatal("new package <70% must fail")
	}
}
```

- [ ] **Step 2: 实现**

`cmd/covergate/main.go`:
```go
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"sort"

	"golang.org/x/tools/cover"
)

type Baseline struct {
	Global   float64            `json:"global"`
	Packages map[string]float64 `json:"packages"`
}

const tolerance = 0.005
const newPackageFloor = 0.70

func compare(base, cur Baseline) []string {
	var bad []string
	if cur.Global+tolerance < base.Global { bad = append(bad, fmt.Sprintf("global %.3f -> %.3f", base.Global, cur.Global)) }
	for p, b := range base.Packages {
		if c, ok := cur.Packages[p]; ok && c+tolerance < b { bad = append(bad, fmt.Sprintf("%s %.3f -> %.3f", p, b, c)) }
	}
	for p, c := range cur.Packages {
		if _, ok := base.Packages[p]; !ok && c < newPackageFloor { bad = append(bad, fmt.Sprintf("new %s %.3f < %.2f", p, c, newPackageFloor)) }
	}
	sort.Strings(bad)
	return bad
}

func main() {
	prof := flag.String("profile", "coverage.out", "")
	basePath := flag.String("baseline", "coverage-baseline.json", "")
	write := flag.Bool("write", false, "")
	flag.Parse()
	profs, err := cover.ParseProfiles(*prof)
	if err != nil { fmt.Fprintln(os.Stderr, err); os.Exit(2) }
	cur := Baseline{Packages: map[string]float64{}}
	var total, covered int64
	for _, p := range profs {
		var pkgTotal, pkgCov int64
		for _, b := range p.Blocks { pkgTotal += int64(b.NumStmt); if b.Count > 0 { pkgCov += int64(b.NumStmt) } }
		total += pkgTotal; covered += pkgCov
		if pkgTotal > 0 { cur.Packages[p.Name] = float64(pkgCov) / float64(pkgTotal) }
	}
	if total > 0 { cur.Global = float64(covered) / float64(total) }
	raw, _ := json.MarshalIndent(cur, "", "  ")
	if *write { os.WriteFile(*basePath, raw, 0o644); return }
	var base Baseline
	b, err := os.ReadFile(*basePath); if err != nil { fmt.Fprintln(os.Stderr, err); os.Exit(2) }
	json.Unmarshal(b, &base)
	if bad := compare(base, cur); len(bad) > 0 { for _, s := range bad { fmt.Fprintln(os.Stderr, s) }; os.Exit(1) }
	fmt.Printf("coverage ok: global=%.3f packages=%d\n", cur.Global, len(cur.Packages))
}
```

- [ ] **Step 3: 生成基线并接 Makefile/CI**

```bash
go test ./internal/... -coverprofile=coverage.out -count=1
go run ./cmd/covergate -write
make cover
```
`make cover` 跑测试并 `go run ./cmd/covergate`；CI 新增 coverage job（不带 -race）。

- [ ] **Step 4: 提交**

```bash
git add cmd/covergate coverage-baseline.json Makefile .github/workflows/test.yml
git commit -m "feat(cover): 覆盖率棘轮——全局地板 + 新包 70%"
```

---

### Task 4: llms.txt / ARCHITECTURE 生成

**Files:**
- Create: `cmd/docgen/main.go`
- Create: `cmd/docgen/main_test.go`
- Create: `llms.txt`
- Modify: `ARCHITECTURE.md`（加 marker）
- Modify: `Makefile`、`.github/workflows/test.yml`

- [ ] **Step 1: 写 marker 替换测试**

`cmd/docgen/main_test.go`:
```go
package main

import (
	"strings"
	"testing"
)

func TestReplaceBlock(t *testing.T) {
	in := "a\n<!-- BEGIN POLICY -->\nOLD\n<!-- END POLICY -->\nz\n"
	out := replaceBlock(in, "<!-- BEGIN POLICY -->", "<!-- END POLICY -->", "NEW")
	if !strings.Contains(out, "NEW") || strings.Contains(out, "OLD") { t.Fatal(out) }
}
```

- [ ] **Step 2: 实现 docgen**

`cmd/docgen/main.go` 读 `architecture-policy.yaml`（`archcheck.Load`）、`go.mod`、`Makefile`，生成：
- `llms.txt`：module/包清单（id + roots + notes）、门禁命令、MCP 工具数（`grep -c 'mcp.AddTool'` 或读取 `internal/channel/mcpserver` 的注册表）、文档入口。
- `ARCHITECTURE.md` 的 marker 区间：`| 层 | 包 | 允许依赖 |` 表，按 module 排序。

- [ ] **Step 3: 加 marker 并生成**

在 `ARCHITECTURE.md` 的「依赖规则」表前后加 `<!-- BEGIN POLICY -->` / `<!-- END POLICY -->`；删掉手写表内容（保留说明段落）。

```bash
go run ./cmd/docgen
make docs && git diff --exit-code
```

- [ ] **Step 4: 提交**

```bash
git add cmd/docgen llms.txt ARCHITECTURE.md Makefile .github/workflows/test.yml
git commit -m "docs(arch): llms.txt 与分层表由 policy 生成——CI docsync"
```

---

### Task 5: A 轨收口

- [ ] **Step 1: 加 `make verify`**

```make
verify: vet lint test arch cover eval
```
（`fmt-check` 若不存在，用 `gofmt -l` 的既有 lint 逻辑。）

- [ ] **Step 2: 全量跑并记录**

```bash
make verify
```
Expected: 全绿；记录 `exceptions=N`、`thresholds=M`、`coverage=G`。

- [ ] **Step 3: 提交**

```bash
git add Makefile
git commit -m "chore(build): make verify 单一本地入口"
```

---

### Task 6: B1a — impactview / reportview 拆分

**Files:**
- Modify: `internal/impactview/html.go` (1554), `scriptgraph.go` (774), `build.go` (602)
- Modify: `internal/reportview/report.go` (672)
- Create: `internal/impactview/html_*.go`、`internal/reportview/report_*.go`（按实际职责命名）

- [ ] **Step 1: 读文件、列函数清单**

Run: `grep -n '^func ' internal/impactview/html.go internal/impactview/scriptgraph.go internal/impactview/build.go internal/reportview/report.go`

- [ ] **Step 2: 按职责分组移动**

规则：
- HTML 入口/模板数据与 HTML 片段渲染分离；
- 脚本图 JSON 生成与图布局分离；
- build 管线按阶段（collect / analyze / render）分离；
- reportview 按 mermaid / markdown / 表格段落分离。
只移动包内函数/类型；不改导出名、不改 SQL/模板文本。新文件每个 <400 行。

- [ ] **Step 3: 测试**

```bash
gofmt -w internal/impactview internal/reportview
go test ./internal/impactview/... ./internal/reportview/... -count=1
```

- [ ] **Step 4: 收缩阈值基线并提交**

```bash
go run ./cmd/archcheck --write-baseline   # 只允许收缩
git add internal/impactview internal/reportview arch-thresholds.baseline
git commit -m "refactor(impactview,reportview): 400 行拆分（B1a）"
```

---

### Task 7: B1b — storage / config 拆分

**Files:**
- Modify: `internal/storage/store_task.go` (715), `printalias.go` (479), `store.go` (450), `prints.go` (425)
- Modify: `internal/config/config.go` (729)
- Create: `internal/storage/store_task_*.go`、`internal/config/config_*.go`

- [ ] **Step 1: 读函数清单**

Run: `grep -n '^func ' internal/storage/store_task.go internal/storage/printalias.go internal/storage/store.go internal/storage/prints.go internal/config/config.go`

- [ ] **Step 2: 按聚合拆**

- storage：任务生命周期 / 查询 / 预算与 usage / print 读写；SQL 与事务边界原样搬移。
- config：按配置域拆（server/llm/review/platform/...），`Load()` 保持单入口。
- 不改变任何 SQL 字符串与 `withWrite` 事务边界。

- [ ] **Step 3: 测试**

```bash
gofmt -w internal/storage internal/config
go test ./internal/storage/... ./internal/config/... -count=1
```

- [ ] **Step 4: 收缩基线并提交**

```bash
go run ./cmd/archcheck --write-baseline
git add internal/storage internal/config arch-thresholds.baseline
git commit -m "refactor(storage,config): 400 行拆分（B1b）"
```

---

### Task 8: B1c — workflow / dispatch 拆分

**Files:**
- Modify: `internal/workflow/requirement/r3tools.go` (703), `workflow.go` (562)
- Modify: `internal/dispatch/merger.go` (456)
- Create: `internal/workflow/requirement/r3tools_*.go`、`internal/workflow/requirement/workflow_*.go`、`internal/dispatch/merger_*.go`

- [ ] **Step 1: 读函数清单并分组**
- [ ] **Step 2: 按工具族/阶段拆；提示词文本与工具签名不变**
- [ ] **Step 3: 测试**

```bash
gofmt -w internal/workflow/requirement internal/dispatch
go test ./internal/workflow/... ./internal/dispatch/... -count=1
```

- [ ] **Step 4: 收缩基线并提交**

```bash
go run ./cmd/archcheck --write-baseline
git add internal/workflow/requirement internal/dispatch arch-thresholds.baseline
git commit -m "refactor(workflow,dispatch): 400 行拆分（B1c）"
```

---

### Task 9: B1d — runner / poster / service 拆分

**Files:**
- Modify: `internal/runner/writing.go` (625), `runner.go` (472), `inject.go` (458), `runner_lifecycle.go` (418), `runner_report.go` (407)
- Modify: `internal/poster/poster.go` (593)
- Modify: `internal/service/service_taskapi.go` (560), `service.go` (405)
- Create: 对应 `_*.go` 文件

- [ ] **Step 1: 读函数清单并分组**
- [ ] **Step 2: 按生命周期/报告/注入/写作/评论拆；保持 `Runner` 字段与导出方法不变**
- [ ] **Step 3: 测试**

```bash
gofmt -w internal/runner internal/poster internal/service
go test ./internal/runner/... ./internal/poster/... ./internal/service/... -count=1
```

- [ ] **Step 4: 收缩基线并提交**

```bash
go run ./cmd/archcheck --write-baseline
git add internal/runner internal/poster internal/service arch-thresholds.baseline
git commit -m "refactor(runner,poster,service): 400 行拆分（B1d）"
```

---

### Task 10: B1e — bootstrap / qa / obs / semsearch / qconsole / plugin / cmd

**Files:**
- Modify: `internal/bootstrap/assemble.go` (495), `models.go` (475), `subsys.go` (411)
- Modify: `internal/qa/qa_tools.go` (475), `qa.go` (420)
- Modify: `internal/obs/obs.go` (460)
- Modify: `internal/semsearch/semsearch.go` (444), `chunks.go` (419)
- Modify: `internal/qconsole/qconsole.go` (411)
- Modify: `internal/plugin/types.go` (404), `internal/plugin/adapter_builtin/builtin.go` (405)
- Modify: `cmd/argus-copilot/main.go` (439)

- [ ] **Step 1: 读函数清单并分组**
- [ ] **Step 2: 按装配面/模型/子系统、QA 工具/会话、指标域、索引/分块、client、workspace、编写**
- [ ] **Step 3: 测试**

```bash
gofmt -w internal cmd/argus-copilot
go test ./internal/bootstrap/... ./internal/qa/... ./internal/obs/... ./internal/semsearch/... ./internal/qconsole/... ./internal/plugin/... ./cmd/argus-copilot/... -count=1
```

- [ ] **Step 4: 收缩基线并提交**

```bash
go run ./cmd/archcheck --write-baseline
git add internal cmd/argus-copilot arch-thresholds.baseline
git commit -m "refactor(bootstrap,qa,obs,semsearch,plugin): 400 行拆分（B1e）"
```

---

### Task 11: B2 — 巨型类型拆分

- [ ] **Step 1: 跑报告**

```bash
go run ./cmd/archcheck 2>&1 | grep 'max_methods_per_type\|THRESHOLD'
```

- [ ] **Step 2: 逐个类型拆**

对每个 >25 实质方法的类型：按聚合根/查询/生命周期/序列化分组，能独立成小类型就独立；保持导出 API 兼容；转发壳不计入。

- [ ] **Step 3: 测试 + 收缩基线 + 提交**

```bash
make arch && go test ./internal/... -count=1
go run ./cmd/archcheck --write-baseline
git add <本任务拆分的包路径> arch-thresholds.baseline
git commit -m "refactor: 巨型类型拆分（B2）"
```

> 注意：此步禁止 `git add -A`/`git add -u`；必须按本任务实际改动的包路径精确提交。

---

### Task 12: B3/B4 — 裸 goroutine + execx

**Files:**
- Modify: `internal/service/service_eventfanout.go:48`
- Modify: `cmd/argus-copilot/main.go:56`
- Create: `internal/execx/execx.go`、`execx_test.go`
- Modify: `internal/service/service_repomap.go:261,266,293`
- Modify: `cmd/argus-copilot/review.go:243`
- Modify: `architecture-policy.yaml`

- [ ] **Step 1: 裸 goroutine 收编**

`service_eventfanout.go` 的 `go func() { ... }()` → `async.GoSafe(ctx, r.Log, func(ctx context.Context) { ... })`（以实际签名为准）。cmd 同理或保留并注释（cmd 不进门禁）。

- [ ] **Step 2: execx 测试先行**

```go
func TestRunCapturesOutput(t *testing.T) {
	out, _, err := Run(context.Background(), "printf", []string{"hi"}, WithTimeout(time.Second))
	if err != nil || string(out) != "hi" { t.Fatalf("%q %v", out, err) }
}
func TestRunTimeout(t *testing.T) {
	_, _, err := Run(context.Background(), "sleep", []string{"2"}, WithTimeout(10*time.Millisecond))
	if err == nil { t.Fatal("want timeout") }
}
```

- [ ] **Step 3: 实现 execx**

按 spec §4.4：`Run(ctx, name, args, opts...)`，默认无 shell；`WithDir/WithEnv/WithTimeout/WithMaxOutput`；错误类型 `execx.Error`。输出上限用 `bytes.Buffer` + `io.LimitReader` 或 `cmd.Stdout` 包装。

- [ ] **Step 4: 迁移 4 处 exec 调用**

`service_repomap.go` 三处改为 `execx.Run(ctx, "git", args, execx.WithDir(dir), execx.WithTimeout(...))`；`cmd/argus-copilot/review.go` 同理。

- [ ] **Step 5: 翻转 policy**

`argus-exec-single-entry` 的 exceptions 删除。**`execution_paths` 保持关闭**：kernel 这条门禁判的是对裸入口（如 `ptc.Run`）的调用，不判 `os/exec`；把 `execx.Run` 登记成裸入口会语义反转。单一出口由 `argus-exec-single-entry` 保证。

- [ ] **Step 6: 测试 + 提交**

```bash
gofmt -w internal cmd
go test ./internal/service/... ./internal/execx/... ./cmd/argus-copilot/... -count=1
make arch
git add internal/execx internal/service/service_repomap.go cmd/argus-copilot/review.go architecture-policy.yaml
git commit -m "refactor(exec): execx 单一执行出口 + 裸 goroutine 归零"
```

---

### Task 13: C0 — kernel v0.38.0

- [ ] **Step 1: 升级**

```bash
go get github.com/yi-nology/goagent-kernel@v0.38.0
go mod tidy
```

- [ ] **Step 2: 全量验证**

```bash
make verify
```
Expected: 全绿；若传递依赖 breaking，单独 commit 修适配。

- [ ] **Step 3: 提交**

```bash
git add go.mod go.sum
git commit -m "chore(deps): goagent-kernel v0.35.0 -> v0.38.0"
```

---

### Task 14: C1 — netguard 出站守卫

**Files:**
- Create: `internal/outbound/outbound.go`、`outbound_test.go`
- Modify: `internal/plugin/adapter_remote/a2a.go`
- Modify: `internal/notify/octopus/octopus.go`
- Modify: `internal/service/audit_forward.go`
- Modify: `internal/qconsole/qconsole.go`
- Modify: `internal/config/config.go`（新增 `OutboundMode`/`OutboundAllow`）

- [ ] **Step 1: 写 SSRF 用例（先红）**

覆盖 spec §5.1.1 的表：半可信 `127.0.0.1`/`10.0.0.1`/`169.254.169.254` 拒绝；可信私网放行；重定向到内网拒绝；`card.URL` 指向内网拒绝；`warn` 模式放行但记录。

- [ ] **Step 2: 实现 outbound**

包装 `netguard`：
- `ProfileTrusted` = `Allow{Private:true, Loopback:true}` + 元数据/链路本地硬拒绝；
- `ProfileSemiTrusted` = 零值 `Allow` + `ARGUS_OUTBOUND_ALLOW` 白名单；
- `Client(profile, base)` 用 `netguard.NewClient`；`CheckRedirect` 用 `netguard.CheckChainWith` 逐跳复验。
- `Mode`：`off|warn|enforce`，默认 `warn`。

- [ ] **Step 3: 接线四处**

A2A：`Endpoint` 与 `card.URL` 都过 `ProfileSemiTrusted`；notify/audit/qconsole 用 `ProfileTrusted`。若 `warn` 模式，违规只记日志/指标并放行。

- [ ] **Step 4: 测试 + 提交**

```bash
go test ./internal/outbound/... ./internal/plugin/adapter_remote/... ./internal/notify/... ./internal/service/... ./internal/qconsole/... -count=1
git add internal/outbound internal/plugin/adapter_remote internal/notify internal/service/audit_forward.go internal/qconsole internal/config
git commit -m "feat(security): netguard 出站守卫——A2A card.URL 逐跳复验"
```

---

### Task 15: C1 — sideeffect 至多一次

**Files:**
- Create: `internal/storage/side_effects.go`
- Create: `internal/effect/effect.go`、`effect_test.go`
- Modify: `internal/storage/migrations.go`
- Modify: `internal/poster/poster.go`
- Modify: `internal/service/fix.go`（或 fix PR 创建点）

- [ ] **Step 1: 建表 + Store**

`side_effects(effect_key PK, resource, status, lease_until, payload_hash, created_at, updated_at)`；三方言迁移；实现 kernel `sideeffect.Store`。

- [ ] **Step 2: 写并发/重放测试（先红）**

同 key 并发两次只 Apply 一次；租约过期可重试；applied 重放不重复；store 故障 fail-soft + 指标。

- [ ] **Step 3: 接入评论与 fix PR**

`effect.Runner.Do(ctx, key, resource, prepare, apply)`；key 按 spec §5.1.2。保留 `summary_comment_id` 现有语义；store 故障记录 `argus.sideeffect.store_unavailable` 并继续。

- [ ] **Step 4: 测试 + 提交**

```bash
go test ./internal/effect/... ./internal/storage/... ./internal/poster/... ./internal/service/... -count=1
git add internal/storage internal/effect internal/poster internal/service/fix.go
git commit -m "feat(effect): sideeffect 至多一次——评论/PR 幂等键"
```

---

### Task 16: C1 — reconcile 纯判定

**Files:**
- Modify: `internal/storage/store_task.go`（`ResetRunningToPending` 边界）
- Modify: `internal/storage/lease.go`（`TryAcquire` 死主判定）
- Create: `internal/storage/reconcile_test.go`（或补现有测试）

- [ ] **Step 1: 写边界测试**

`OrphanAtBoot` 边界（相等/grace）、`JudgeLease` 三态（他人有效/他人过期/自己）、`Stalled` 阈值相等。

- [ ] **Step 2: 替换时间比较**

用 `reconcile.OrphanAtBoot/JudgeLease/Stalled` 替换手写比较；SQL 条件更新与事务边界不变。

- [ ] **Step 3: 测试 + 提交**

```bash
go test ./internal/storage/... -count=1
git add internal/storage
git commit -m "refactor(storage): reconcile 纯判定——孤儿/死主边界单源"
```

---

### Task 17: C1 — finalize 终态漏斗

**Files:**
- Create: `internal/runner/finalize.go`、`finalize_test.go`
- Modify: `internal/runner/writing.go`、`runner_lifecycle.go`

- [ ] **Step 1: 写 7 调用点表格测试（先红）**

Cause 表：success/stale/failed/panic/timeout/shutdown + fenced 分流；断言状态、公告、指标、reason。

- [ ] **Step 2: 实现 `finishTask`**

按 spec §5.1.4：先按 cause 选 status，再 `FinishTaskFenced`，fenced 时跳过公告与指标；终态写用 `context.WithoutCancel`。探针 kernel `finalize.Route/Detached`：若需 kernel contract/steer/drain 类型则不 import，按模式实现。

- [ ] **Step 3: 迁移 7 个调用点**

writing ×3、lifecycle ×2、stale ×1、finalizeAndAnnounce ×1 全部走 `finishTask`；保持日志文本与指标标签不变。

- [ ] **Step 4: 测试 + 提交**

```bash
go test ./internal/runner/... -count=1
make eval
git add internal/runner
git commit -m "refactor(runner): finalize 终态漏斗——7 调用点单源"
```

---

### Task 18: C2 探针批一

候选：`guardset`、`retryplan`、`deadline`、`controlplane`。

- [ ] **Step 1: 逐项跑三探针**（边界/净收益/测试）
- [ ] **Step 2: 通过者实现，不通过者写 issue 草案**
- [ ] **Step 3: 更新 spec §5.3 处置表**

---

### Task 19: C2 探针批二

候选：`cost`、`tenant`、`metrics`、`ticket`、`delegate`、`handoff`、`trajectory`、`eval`、`skilldisclose`、`hookengine`、`sessionref`、`pending`、`delta`。

- [ ] **Step 1: 逐项跑三探针**
- [ ] **Step 2: 通过者实现，不通过者写 issue 草案**
- [ ] **Step 3: 更新 spec §5.3**

---

### Task 20: 收口 — 全量门禁 + issue 台账 + 记忆

- [ ] **Step 1: 全量**

```bash
make verify
make eval
```

- [ ] **Step 2: 提 issue（按 spec §8.1）**

```bash
gh issue create --repo yi-nology/argus --title "覆盖率提升至 kernel 60/70 口径" --body "..."
gh issue create --repo yi-nology/argus --title "acpx 执行沙箱（C4）" --body "..."
gh issue create --repo yi-nology/argus --title "tenant_gate：多租户授权上下文" --body "..."
gh issue create --repo yi-nology/argus --title "根 main.go 纳入依赖方向门禁" --body "..."
gh issue create --repo yi-nology/argus --title "internal/dispatch 与 kernel dispatch 重名" --body "..."
gh issue create --repo yi-nology/argus --title "printf-log callback 从 warn 升 error" --body "..."
```
C2 探针失败项逐项追加；上游缺口确认后 `--repo yi-nology/goagent-kernel`。

- [ ] **Step 3: 更新记忆**

把 `argus-kernel-method-refactor-20261115.md` 更新为实施终态（只改主题文件，MEMORY.md 已超限不再加长）。

- [ ] **Step 4: 最终提交**

```bash
git add docs/superpowers docs/verify architecture-policy.yaml coverage-baseline.json arch-thresholds.baseline arch-exceptions.baseline llms.txt ARCHITECTURE.md
git commit -m "chore(refactor): 三轨收口——门禁绿/覆盖率棘轮/issue 台账"
```