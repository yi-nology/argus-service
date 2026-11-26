// Package docgen 从 architecture-policy.yaml 生成两份 AI/人类可读入口：
//
//   - llms.txt：机器事实 + 包清单 + 门禁命令 + 文档入口；
//   - ARCHITECTURE.md 的 <!-- BEGIN POLICY --> / <!-- END POLICY --> 区间：
//     分层依赖表。
//
// 这是 goagent-kernel site/gen.mjs 方法的等价物：文档与门禁互为镜像。
// CI docs-sync 跑 `make docs-check`（`docgen -check`）断言一致，手改生成区即红；
// `-check` 只比对不落盘，本地想刷新生成物仍用 `make docs`。
package docgen

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/yi-nology/goagent-kernel/archcheck"
)

const (
	beginMarker = "<!-- BEGIN POLICY -->"
	endMarker   = "<!-- END POLICY -->"
)

// errDrift 标记「生成物与磁盘不一致」（退出码 1），区别于读 policy/文件 IO
// 失败（退出码 2）——CI 只需要知道是漂移还是工具自身出错。
var errDrift = errors.New("生成物与磁盘不一致")

// generatedFile 是一份生成物：相对仓库根的路径 + 目标内容。
type generatedFile struct {
	path    string
	content string
}

// replaceBlock 用 body 替换 begin/end 之间的内容（保留两行 marker 本身）。
// marker 缺失或顺序颠倒时返回错误——静默不替换会让 docsync 永远绿。
func replaceBlock(src, begin, end, body string) (string, error) {
	i := strings.Index(src, begin)
	j := strings.Index(src, end)
	if i < 0 || j < 0 {
		return src, errors.New("缺少 policy 生成区 marker")
	}
	if j < i {
		return src, errors.New("policy 生成区 marker 顺序颠倒")
	}
	return src[:i+len(begin)] + "\n" + body + "\n" + src[j:], nil
}

func sortedModules(mods []archcheck.Module) []archcheck.Module {
	out := append([]archcheck.Module(nil), mods...)
	sort.Slice(out, func(i, j int) bool {
		if len(out[i].Roots) == 0 || len(out[j].Roots) == 0 {
			return out[i].ID < out[j].ID
		}
		return out[i].Roots[0] < out[j].Roots[0]
	})
	return out
}

func renderTable(mods []archcheck.Module) string {
	var b strings.Builder
	b.WriteString("| 包 | 允许的 internal / external 依赖 | 说明 |\n")
	b.WriteString("|---|---|---|\n")
	for _, m := range sortedModules(mods) {
		roots := strings.Join(m.Roots, ", ")
		reqs := strings.Join(m.Requires, ", ")
		if reqs == "" {
			reqs = "（无）"
		}
		note := strings.TrimSpace(m.Notes)
		if note == "" {
			note = "-"
		}
		fmt.Fprintf(&b, "| %s | %s | %s |\n", roots, reqs, note)
	}
	return b.String()
}

var toolRe = regexp.MustCompile(`mcp\.NewTool\("([^"]+)"`)

func countMCPTools(dir string) int {
	files, _ := filepath.Glob(filepath.Join(dir, "*.go"))
	names := map[string]bool{}
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		raw, err := os.ReadFile(f)
		if err != nil {
			continue
		}
		for _, m := range toolRe.FindAllStringSubmatch(string(raw), -1) {
			names[m[1]] = true
		}
	}
	return len(names)
}

func readGoMod(path string) (module, goversion string) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return "", ""
	}
	for _, line := range strings.Split(string(raw), "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "module ") {
			module = strings.TrimSpace(strings.TrimPrefix(line, "module "))
		}
		if strings.HasPrefix(line, "go ") {
			goversion = strings.TrimSpace(strings.TrimPrefix(line, "go "))
		}
	}
	return module, goversion
}

func renderLLMS(pol *archcheck.Policy, module, goversion string, tools int) string {
	var b strings.Builder
	b.WriteString("# Argus\n\n")
	b.WriteString("> 可插拔多 Agent 代码审查平台：编排是代码，LLM 只做单环节。\n\n")
	b.WriteString("## 机器事实\n\n")
	fmt.Fprintf(&b, "- module: `%s`\n", module)
	fmt.Fprintf(&b, "- go: `%s`\n", goversion)
	fmt.Fprintf(&b, "- internal packages: %d\n", len(pol.Modules))
	fmt.Fprintf(&b, "- MCP tools: %d\n", tools)
	b.WriteString("- architecture policy: `architecture-policy.yaml`\n\n")
	b.WriteString("## 门禁\n\n")
	b.WriteString("- `make arch` — policy + kernel archcheck + 包文档 + 阈值/例外棘轮\n")
	b.WriteString("- `make cover` — 覆盖率棘轮（全局地板 + 单包不降 + 新包 70%）\n")
	b.WriteString("- `make lint` — vet + staticcheck + gofmt\n")
	b.WriteString("- `make test` — 全量 -race\n")
	b.WriteString("- `make eval` — golden 编排语义回归\n")
	b.WriteString("- `make crossbuild` — 三平台交叉编译（linux/amd64、linux/arm64、darwin/arm64）\n")
	b.WriteString("- `make modcheck` — replace 允许清单 + go.work 禁止 + go mod verify\n")
	b.WriteString("- `make docs-check` — llms.txt/ARCHITECTURE.md 生成物一致性（不写文件）\n")
	b.WriteString("- `make verify` — 以上各项的本地单一入口\n\n")
	b.WriteString("## 文档\n\n")
	b.WriteString("- `docs/README.md`（四层索引：guide/ops/design/archive）\n")
	b.WriteString("- `docs/guide/API-COMPAT.md`（对外契约稳定性分级与破坏性变更流程）\n")
	b.WriteString("- `docs/design/design.md`（设计契约）\n")
	b.WriteString("- `docs/design/architecture-deep-dive.md`（架构演进深读）\n")
	b.WriteString("- `docs/design/roadmap.md`（演进路线）\n")
	b.WriteString("- `docs/archive/superpowers/specs/2026-11-15-kernel-method-refactor-design.md`\n\n")
	b.WriteString("## 包清单\n\n")
	b.WriteString(renderTable(pol.Modules))
	b.WriteString("\n## 边界\n\n")
	b.WriteString("- 根 `main.go` 不在 dependency_direction 扫描面（kernel `packageDirs` 跳过仓根）；组合根单向性由 `argus-bootstrap-single-entry` 规则保证。\n")
	b.WriteString("- 领域包（`dispatch`/`learning`/`dream`/`qa`/`impactview`）是宿主知识，不追求 kernel 化。\n")
	b.WriteString("- kernel 只经公开包消费；`goagent-kernel/internal/*` 由 `argus-no-harness-internal` 规则禁止。\n")
	return b.String()
}

// buildDocs 在内存中生成全部目标内容，不写盘。
func buildDocs(root string) ([]generatedFile, error) {
	pol, err := archcheck.Load(filepath.Join(root, "architecture-policy.yaml"))
	if err != nil {
		return nil, fmt.Errorf("policy 加载失败: %w", err)
	}
	module, goversion := readGoMod(filepath.Join(root, "go.mod"))
	tools := countMCPTools(filepath.Join(root, "internal/channel/mcpserver"))

	archPath := filepath.Join(root, "ARCHITECTURE.md")
	arch, err := os.ReadFile(archPath)
	if err != nil {
		return nil, fmt.Errorf("读 ARCHITECTURE.md 失败: %w", err)
	}
	updated, err := replaceBlock(string(arch), beginMarker, endMarker, renderTable(pol.Modules))
	if err != nil {
		return nil, fmt.Errorf("ARCHITECTURE.md 生成区失败: %w", err)
	}
	return []generatedFile{
		{path: "llms.txt", content: renderLLMS(pol, module, goversion, tools)},
		{path: "ARCHITECTURE.md", content: updated},
	}, nil
}

// writeDocs 把目标内容落盘（普通生成模式）。
func writeDocs(root string, files []generatedFile) error {
	for _, f := range files {
		if err := os.WriteFile(filepath.Join(root, f.path), []byte(f.content), 0o644); err != nil {
			return fmt.Errorf("写 %s 失败: %w", f.path, err)
		}
	}
	return nil
}

// checkDocs 逐字节比对目标内容与磁盘现有文件，只读不写。
// 一致返回 nil；不一致返回 errDrift（含首个差异块），调用方以 exit 1 收口。
func checkDocs(root string, files []generatedFile) error {
	var drift []string
	for _, f := range files {
		disk, err := os.ReadFile(filepath.Join(root, f.path))
		if err != nil {
			return fmt.Errorf("读 %s 失败: %w", f.path, err)
		}
		if string(disk) != f.content {
			drift = append(drift, diffSnippet(f.path, string(disk), f.content))
		}
	}
	if len(drift) > 0 {
		return fmt.Errorf("%w:\n%s", errDrift, strings.Join(drift, "\n"))
	}
	return nil
}

// diffSnippet 渲染首个不一致块的简短 diff（磁盘为 `-`，生成为 `+`）。
// 用公共前后缀把差异收窄到第一处变更附近；块体与上下文都有行数上限，
// 避免整表重排时刷出几百行。
func diffSnippet(name, disk, want string) string {
	if disk == want {
		return ""
	}
	d := strings.Split(disk, "\n")
	w := strings.Split(want, "\n")
	start := 0
	for start < len(d) && start < len(w) && d[start] == w[start] {
		start++
	}
	// 公共后缀（从差异点之后计算）让「插入一行」只显示该行，而不是整段下移。
	dEnd, wEnd := len(d), len(w)
	for dEnd > start && wEnd > start && d[dEnd-1] == w[wEnd-1] {
		dEnd--
		wEnd--
	}

	const contextLines = 2
	const maxBlockLines = 8
	from := start - contextLines
	if from < 0 {
		from = 0
	}
	removed, removedOmitted := capLines(d[start:dEnd], maxBlockLines)
	added, addedOmitted := capLines(w[start:wEnd], maxBlockLines)

	var b strings.Builder
	fmt.Fprintf(&b, "--- %s（磁盘）\n+++ %s（docgen 生成）\n", name, name)
	fmt.Fprintf(&b, "@@ 第 %d 行 @@\n", start+1)
	for _, line := range d[from:start] {
		b.WriteString(" " + line + "\n")
	}
	for _, line := range removed {
		b.WriteString("-" + line + "\n")
	}
	for _, line := range added {
		b.WriteString("+" + line + "\n")
	}
	if removedOmitted > 0 || addedOmitted > 0 {
		fmt.Fprintf(&b, "... 省略 %d 行差异\n", removedOmitted+addedOmitted)
	}
	after := dEnd + contextLines
	if after > len(d) {
		after = len(d)
	}
	for _, line := range d[dEnd:after] {
		b.WriteString(" " + line + "\n")
	}
	return strings.TrimRight(b.String(), "\n")
}

func capLines(lines []string, max int) ([]string, int) {
	if len(lines) <= max {
		return lines, 0
	}
	return lines[:max], len(lines) - max
}

func Run(args []string) int {
	fs := flag.NewFlagSet("argus docgen", flag.ContinueOnError)
	check := fs.Bool("check", false, "只校验 llms.txt/ARCHITECTURE.md 是否与生成结果一致（不写文件）")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}

	files, err := buildDocs(".")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 2
	}
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
	if err := writeDocs(".", files); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 2
	}
	return 0
}
