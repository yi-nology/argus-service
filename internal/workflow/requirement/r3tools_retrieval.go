// r3tools_retrieval.go —— 检索与取回工具：search_code 受控正则全文检索 +
// get_detail 截断输出分页取回。

package requirement

import (
	"github.com/cloudwego/eino/components/tool"
)

// r3SearchTool search_code 全文检索工具（v3.84.0）：工作副本上的受控正则搜索，
// 覆盖符号索引到不了的词法线索（字符串/配置/非 Go 代码）。未注入工作副本路径
// （功能关闭/无工作副本池/无纳入文件）时返回 nil（不进表）。
func (w *Workflow) r3SearchTool() tool.BaseTool {
	return r3SearchTool(w)
}

// r3DetailTool get_detail（大输出两级化；v5.0.0 存档面切磁盘——取回面 v6.15.0 收编
// goagent-kernel/spill.ReadPage：净化+会话隔离三重守卫单源上游，白名单正则退役；
// 分页取回——单页缺省 8KB/上限 16KB，NextOffset 续页，替代旧版全文直读
// （200KB 直塞上下文是预算隐患））。
func (w *Workflow) r3DetailTool() (tool.BaseTool, error) {
	return r3DetailTool(w)
}
