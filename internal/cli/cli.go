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