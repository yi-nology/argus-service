// report_silenced_test.go —— 仓级回复控制静默计数尾注（v8.4.1）：
// floor/cap 过滤后的条数必须在三套模板（standard/analyst/minimal）都可见，
// 与 ARGUS_REPORT_META 开关无关——「静默」不等于「没审」。
package reportview

import (
	"strings"
	"testing"

	"github.com/yi-nology/argus/internal/reporttmpl"
)

func TestSilencedLineAcrossTemplates(t *testing.T) {
	base := Report{
		TaskID: "t-silence", Version: "v8.4.1",
		VerdictText:   "💬 有审查意见，请参考",
		Rule:          "R5.4",
		SilencedFloor: 2,
		SilencedCap:   1,
		SilenceFloor:  "medium",
	}
	for _, tpl := range []string{"", reporttmpl.Standard, reporttmpl.Analyst, reporttmpl.Minimal} {
		rep := base
		rep.Template = tpl
		rep.Meta = false // 元信息关闭也必须保留静默交代
		out := Render(rep)
		want := "🔇 按仓库设置静默 3 条（2 条低于 medium 级别，1 条超出条数上限）"
		if !strings.Contains(out, want) {
			t.Fatalf("模板 %q 缺静默计数行 %q\n---\n%s", tpl, want, out)
		}
	}
}

func TestSilencedLineOnlyFloor(t *testing.T) {
	out := Render(Report{
		TaskID: "t-floor", VerdictText: "✅ 通过",
		SilencedFloor: 4, SilenceFloor: "high",
	})
	if !strings.Contains(out, "静默 4 条（4 条低于 high 级别）") {
		t.Fatalf("仅 floor 场景文案不对:\n%s", out)
	}
	if strings.Contains(out, "超出条数上限") {
		t.Fatalf("无 cap 不应出现上限文案:\n%s", out)
	}
}

func TestSilencedLineOnlyCap(t *testing.T) {
	out := Render(Report{
		TaskID: "t-cap", VerdictText: "✅ 通过",
		SilencedCap: 7,
	})
	if !strings.Contains(out, "静默 7 条（7 条超出条数上限）") {
		t.Fatalf("仅 cap 场景文案不对:\n%s", out)
	}
}

func TestSilencedLineAbsentWhenZero(t *testing.T) {
	for _, tpl := range []string{"", reporttmpl.Standard, reporttmpl.Analyst, reporttmpl.Minimal} {
		out := Render(Report{TaskID: "t-clean", Version: "v8.4.1", Template: tpl, VerdictText: "✅ 通过"})
		if strings.Contains(out, "🔇") {
			t.Fatalf("零静默不应渲染计数行（模板 %q）:\n%s", tpl, out)
		}
	}
}
