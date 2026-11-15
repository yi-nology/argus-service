// Package main 从 architecture-policy.yaml 生成两份 AI/人类可读入口：
//
//   - llms.txt：机器事实 + 包清单 + 门禁命令 + 文档入口；
//   - ARCHITECTURE.md 的 <!-- BEGIN POLICY --> / <!-- END POLICY --> 区间：
//     分层依赖表。
//
// 这是 goagent-kernel site/gen.mjs 方法的等价物：文档与门禁互为镜像。
// CI docsync 跑 `make docs && git diff --exit-code`，手改生成区即红。
package main

import (
	"errors"
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
	b.WriteString("- `make verify` — 以上五项的本地单一入口\n\n")
	b.WriteString("## 文档\n\n")
	b.WriteString("- `README.md`\n- `ARCHITECTURE.md`\n- `design.md`\n")
	b.WriteString("- `docs/superpowers/specs/2026-11-15-kernel-method-refactor-design.md`\n\n")
	b.WriteString("## 包清单\n\n")
	b.WriteString(renderTable(pol.Modules))
	b.WriteString("\n## 边界\n\n")
	b.WriteString("- 根 `main.go` 不在 dependency_direction 扫描面（kernel `packageDirs` 跳过仓根）；组合根单向性由 `argus-bootstrap-single-entry` 规则保证。\n")
	b.WriteString("- 领域包（`dispatch`/`learning`/`dream`/`qa`/`impactview`）是宿主知识，不追求 kernel 化。\n")
	b.WriteString("- kernel 只经公开包消费；`goagent-kernel/internal/*` 由 `argus-no-harness-internal` 规则禁止。\n")
	return b.String()
}

func main() {
	pol, err := archcheck.Load("architecture-policy.yaml")
	if err != nil {
		fmt.Fprintln(os.Stderr, "policy 加载失败:", err)
		os.Exit(2)
	}
	module, goversion := readGoMod("go.mod")
	tools := countMCPTools("internal/channel/mcpserver")

	if err := os.WriteFile("llms.txt", []byte(renderLLMS(pol, module, goversion, tools)), 0o644); err != nil {
		fmt.Fprintln(os.Stderr, "写 llms.txt 失败:", err)
		os.Exit(2)
	}

	arch, err := os.ReadFile("ARCHITECTURE.md")
	if err != nil {
		fmt.Fprintln(os.Stderr, "读 ARCHITECTURE.md 失败:", err)
		os.Exit(2)
	}
	updated, err := replaceBlock(string(arch), beginMarker, endMarker, renderTable(pol.Modules))
	if err != nil {
		fmt.Fprintln(os.Stderr, "ARCHITECTURE.md 生成区失败:", err)
		os.Exit(2)
	}
	if err := os.WriteFile("ARCHITECTURE.md", []byte(updated), 0o644); err != nil {
		fmt.Fprintln(os.Stderr, "写 ARCHITECTURE.md 失败:", err)
		os.Exit(2)
	}
}
