// r3tools_retrieval.go —— 检索与取回工具：search_code 受控正则全文检索 +
// get_detail 截断输出分页取回。

package requirement

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/components/tool/utils"
	"github.com/yi-nology/goagent-kernel/spill"

	"github.com/yi-nology/argus/internal/codesearch"
	"github.com/yi-nology/argus/internal/plugin"
)

// r3SearchTool search_code 全文检索工具（v3.84.0）：工作副本上的受控正则搜索，
// 覆盖符号索引到不了的词法线索（字符串/配置/非 Go 代码）。未注入工作副本路径
// （功能关闭/无工作副本池/无纳入文件）时返回 nil（不进表）。
func (w *Workflow) r3SearchTool() tool.BaseTool {
	if w.CodeSearchDir == "" {
		return nil
	}
	st, err := utils.InferTool("search_code", codesearch.ToolDescription,
		func(ctx context.Context, in *codesearch.ToolIn) (*codesearch.ToolOut, error) {
			res, err := codesearch.Search(ctx, w.CodeSearchDir, in.Pattern,
				codesearch.Options{Globs: codesearch.NormalizeGlobs(in.Glob), IgnoreCase: in.IgnoreCase})
			if err != nil {
				return &codesearch.ToolOut{Result: plugin.ToolErrNote(err)}, nil
			}
			return &codesearch.ToolOut{Result: DataFence("search_code 检索结果（数据）", w.stashOverflowAt(res.Render(), detailOverflowMax))}, nil
		})
	if err != nil {
		w.Log.Warn("argus.r3.search_tool_build_failed", "error", err.Error())
		return nil
	}
	return st
}

// r3DetailTool get_detail（大输出两级化；v5.0.0 存档面切磁盘——取回面 v6.15.0 收编
// goagent-kernel/spill.ReadPage：净化+会话隔离三重守卫单源上游，白名单正则退役；
// 分页取回——单页缺省 8KB/上限 16KB，NextOffset 续页，替代旧版全文直读
// （200KB 直塞上下文是预算隐患））。
func (w *Workflow) r3DetailTool() (tool.BaseTool, error) {
	return utils.InferTool("get_detail",
		"读取此前被截断的完整工具输出（分页）。ref 来自截断提示中的 ref 编号（如 \"d1\"）；"+
			"默认读第一页，长输出用 offset/limit 续页（返回值含 next_offset 时还有后续页）。",
		func(ctx context.Context, in *getDetailIn) (*symOut, error) {
			ref := strings.TrimSpace(in.Ref)
			if ref == "" || w.SpillDir == "" {
				return &symOut{Result: "无此详情 ref（编号无效或已被后续详情挤出）"}, nil
			}
			// SpillDir 布局 = <root>/<taskID>：root/session 拆给上游 spill 契约
			//（净化/会话守卫/分页语义全在上游单源）。
			pg, err := spill.ReadPage(filepath.Dir(w.SpillDir), filepath.Base(w.SpillDir),
				ref, in.Offset, in.Limit)
			if err != nil {
				return &symOut{Result: "无此详情 ref（编号无效或已被后续详情挤出）"}, nil
			}
			out := DataFence("get_detail 完整输出（数据）", pg.Content)
			if !pg.EOF {
				out += fmt.Sprintf("\n…（分页取回：本页 %d/%d 字节，offset=%d；"+
					"继续用 get_detail ref=%q offset=%d 取下一页）",
					pg.Bytes, pg.Total, pg.Offset, pg.Name, pg.NextOffset)
			}
			return &symOut{Result: out}, nil
		})
}
