// Package cli 是单二进制 argus 的子命令注册表。
//
// 服务启动仍留在仓根 main.go（进程生命周期与组合根）；本包只登记工具与门禁
// 子命令。子命令名沿用 v7.3 之前的 cmd/<name> 目录名，避免肌肉记忆与脚本漂移。
package cli

import (
	"fmt"
	"io"
	"sort"

	"github.com/yi-nology/argus/internal/cli/archcheck"
	"github.com/yi-nology/argus/internal/cli/archgen"
	"github.com/yi-nology/argus/internal/cli/covergate"
	"github.com/yi-nology/argus/internal/cli/dbcopy"
	"github.com/yi-nology/argus/internal/cli/docgen"
	"github.com/yi-nology/argus/internal/cli/exportdata"
	"github.com/yi-nology/argus/internal/cli/modcheck"
	"github.com/yi-nology/argus/internal/cli/ocrshim"
	"github.com/yi-nology/argus/internal/cli/qconsole"
)

// Command 是一个子命令登记项。Run 返回进程退出码（0 成功 / 1 发现 / 2 用法或无法判定）。
type Command struct {
	Name    string
	Summary string
	Run     func([]string) int
}

// All 返回全部已登记子命令（按 Name 排序）。
func All() []Command {
	cmds := []Command{
		{Name: "archcheck", Summary: "架构门禁：policy + kernel archcheck + 阈值/例外棘轮", Run: archcheck.Run},
		{Name: "archgen", Summary: "生成 architecture-policy.yaml 的 modules 段", Run: archgen.Run},
		{Name: "covergate", Summary: "覆盖率棘轮门禁", Run: covergate.Run},
		{Name: "dbcopy", Summary: "sqlite→postgres 一次性全量迁移", Run: dbcopy.Run},
		{Name: "docgen", Summary: "生成/校验 llms.txt 与 ARCHITECTURE.md 生成区", Run: docgen.Run},
		{Name: "exportdata", Summary: "学习环训练数据导出 JSONL", Run: exportdata.Run},
		{Name: "modcheck", Summary: "模块面门禁：replace 白名单 + 禁 go.work + go mod verify", Run: modcheck.Run},
		{Name: "ocrshim", Summary: "OCR MCP sidecar（streamable HTTP）", Run: ocrshim.Run},
		{Name: "qconsole", Summary: "审查质量控制台静态页/文本", Run: qconsole.Run},
	}
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