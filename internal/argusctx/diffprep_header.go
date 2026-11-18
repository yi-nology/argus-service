// diffprep_header.go —— diff 文本文件归属头（#112）：diffKindLabel/diffHeader。
// 独立成文件的职责理由：diff 文本「形态装配」（头/围栏）与「预算截断」是
// 两个变化原因；头形态随 LLM 消费面演进，截断算法随性能口径演进。

package argusctx

import (
	"fmt"
	"unicode/utf8"

	"github.com/yi-nology/argus/internal/plugin"
)

// diffHeader 归属头（#112）：变更性质标注 + 路径，置于 hunk 之前。
func diffHeader(e plugin.DiffFile) string {
	header := fmt.Sprintf("=== %s（%s） ===\n", e.Path, diffKindLabel(e))
	return header
}

// headerLen 头的 rune 长度（预算计入用；与 diffHeader 同源的单一计数点）。
func headerLen(e plugin.DiffFile) int {
	return utf8.RuneCountInString(diffHeader(e))
}

// diffKindLabel 变更性质标注（#112 头的一部分）：新增/删除/重命名/修改。
func diffKindLabel(e plugin.DiffFile) string {
	switch {
	case e.IsNew:
		return "新增"
	case e.IsDeleted:
		return "删除"
	case e.IsRenamed:
		return "重命名"
	default:
		return "修改"
	}
}
