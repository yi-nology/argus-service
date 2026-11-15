// r3tools_codegraph.go —— Go/多语言符号工具四件套（codegraph）：定义定位与
// 跨文件影响面（get_symbol/get_references/get_callers/get_poly_symbol）。

package requirement

import (
	"github.com/cloudwego/eino/components/tool"
)

// r3CodeGraphTools Go 符号工具四件套（v3.15.0 P0-1 codegraph）：跨文件影响面/
// 定义定位。idx 为 nil（非 Go 变更/功能关闭/无工作副本）时返回 nil（不进表）。
func (w *Workflow) r3CodeGraphTools() (getSymbol, getRefs, getCallers, getPoly tool.BaseTool, err error) {
	return r3CodeGraphTools(w)
}
