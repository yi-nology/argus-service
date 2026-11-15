// r3tools_codegraph.go —— Go/多语言符号工具四件套（codegraph）：定义定位与
// 跨文件影响面（get_symbol/get_references/get_callers/get_poly_symbol）。

package requirement

import (
	"context"
	"fmt"
	"strings"

	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/components/tool/utils"

	"github.com/yi-nology/argus/internal/langcap"
)

// r3CodeGraphTools Go 符号工具四件套（v3.15.0 P0-1 codegraph）：跨文件影响面/
// 定义定位。idx 为 nil（非 Go 变更/功能关闭/无工作副本）时返回 nil（不进表）。
func (w *Workflow) r3CodeGraphTools() (getSymbol, getRefs, getCallers, getPoly tool.BaseTool, err error) {
	if w.CodeGraph == nil {
		return nil, nil, nil, nil, nil
	}
	getSymbol, err = utils.InferTool("get_symbol",
		"按名称查询 Go 符号（函数/方法/类型/变量）的定义位置与签名，用于核实 diff 中看不到的实现细节。返回 file:line + 签名列表。",
		func(ctx context.Context, in *symIn) (*symOut, error) {
			defs := w.CodeGraph.Definitions(in.Name)
			if len(defs) == 0 {
				return &symOut{Result: "未找到符号 " + in.Name}, nil
			}
			var b strings.Builder
			for _, d := range defs {
				name := d.Name
				if d.Recv != "" {
					name = d.Recv + "." + d.Name
				}
				fmt.Fprintf(&b, "%s:%d  %s %s\n", d.File, d.Line, name, d.Signature)
			}
			return &symOut{Result: DataFence("get_symbol 结果（数据）", w.stashOverflowAt(b.String(), detailOverflowMax))}, nil
		})
	if err != nil {
		return nil, nil, nil, nil, err
	}
	getRefs, err = utils.InferTool("get_references",
		"查询 Go 标识符在仓库中的出现位置（file:line 列表），用于评估跨文件影响面（谁调用了被改的函数/谁用了被改的类型）。精确大小写匹配。",
		func(ctx context.Context, in *symIn) (*symOut, error) {
			refs := w.CodeGraph.References(in.Name, 50)
			if len(refs) == 0 {
				return &symOut{Result: "未找到出现位置（可能仅定义处或不在索引范围）"}, nil
			}
			var b strings.Builder
			for _, r := range refs {
				fmt.Fprintf(&b, "%s:%d\n", r.File, r.Line)
			}
			return &symOut{Result: DataFence("get_references 结果（数据）", w.stashOverflowAt(b.String(), detailOverflowMax))}, nil
		})
	if err != nil {
		return nil, nil, nil, nil, err
	}
	getCallers, err = utils.InferTool("get_callers",
		"查询 Go 函数的调用形态出现位置（pkg.F( / F( 词法匹配，file:line 列表）——评估改动影响面优先用本工具；词法级索引，引用前须用 get_file 核实。",
		func(ctx context.Context, in *symIn) (*symOut, error) {
			refs := w.CodeGraph.GoCallers(in.Name, 50)
			if len(refs) == 0 {
				return &symOut{Result: "未找到调用形态出现位置（可能未被调用或不在索引范围）"}, nil
			}
			var b strings.Builder
			for _, r := range refs {
				fmt.Fprintf(&b, "%s:%d\n", r.File, r.Line)
			}
			return &symOut{Result: DataFence("get_callers 结果（词法级，数据）", w.stashOverflowAt(b.String(), detailOverflowMax))}, nil
		})
	if err != nil {
		return nil, nil, nil, nil, err
	}
	getPoly, err = utils.InferTool("get_poly_symbol",
		"按名称查询 "+langcap.JoinLangs(langcap.PolySymbolExts)+" 函数的定义位置与参数（词法级索引），用于核实 diff 中看不到的非 Go 实现。",
		func(ctx context.Context, in *symIn) (*symOut, error) {
			defs := w.CodeGraph.PolyDefinitions(in.Name)
			if len(defs) == 0 {
				return &symOut{Result: "未找到符号 " + in.Name}, nil
			}
			var b strings.Builder
			for _, d := range defs {
				if d.Doc != "" {
					fmt.Fprintf(&b, "%s:%d  def %s(%s)  # %s\n", d.File, d.Line, d.Name, d.Args, d.Doc)
				} else {
					fmt.Fprintf(&b, "%s:%d  def %s(%s)\n", d.File, d.Line, d.Name, d.Args)
				}
			}
			return &symOut{Result: DataFence("get_poly_symbol 结果（词法级，数据）", w.stashOverflowAt(b.String(), detailOverflowMax))}, nil
		})
	if err != nil {
		return nil, nil, nil, nil, err
	}
	return getSymbol, getRefs, getCallers, getPoly, nil
}
