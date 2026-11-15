// report_verdict.go —— R5 结论文案映射与围栏安全长度（markdown 渲染共用）。

package reportview

import "fmt"

// VerdictText 把 R5 结论映射为模板结论文案（workflow 调用，保持单一出处）。
// 词表必须与 workflow 的 rule 赋值一一对应——R5.0（v3.8.0 修复）：此前无该分支，
// 守门降级 COMMENT 后 default 落"✅ 通过"，人读文案与真值表直接矛盾。
func VerdictText(rule string, missingPartial int) string {
	switch {
	case rule == "R5.0":
		return "⚠️ 未能完成审查（无插件实际产出），本结论不可作为合并依据"
	case rule == "R5.1":
		return fmt.Sprintf("⛔ 需求未完成（%d 项未达标）", missingPartial)
	case rule == "R5.2":
		return "⛔ 存在高危问题，请修复后再提交"
	case rule == "R5.3":
		return "⚠️ 需人工确认"
	case rule == "R5.4":
		return "💬 有审查意见，请参考"
	default:
		return "✅ 通过"
	}
}

// codeFenceLen 围栏安全长度（v3.91.2）：内容中最长反引号串 +1（下限 3）——
// CommonMark 下内容再含 ``` 也不会提前闭合围栏。
func codeFenceLen(s string) int {
	n, cur := 3, 0
	for _, r := range s {
		if r == '`' {
			cur++
			if cur >= n {
				n = cur + 1
			}
		} else {
			cur = 0
		}
	}
	return n
}
