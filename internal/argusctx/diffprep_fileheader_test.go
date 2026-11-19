// diffprep_fileheader_test.go —— #112：diff 文本文件归属头（TDD）。
// 根因：f.Diff 是平台给的单文件 hunk 文本（GitLab diffs API 纯 hunk，
// 无 diff --git / --- / +++ 头），addText 直接 \n 拼接多文件/单文件都无
// 归属标注 → LLM 只能从内容猜路径（package validator + 主文件名 →
// validator/validator.go 幻觉，findings 张冠李戴，行内评论 dropped）。
// 修法：addText 拼「=== path ===」显式头（含变更性质标注），头计入 token 预算。
package argusctx

import (
	"context"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/yi-nology/argus/internal/plugin"
	"github.com/yi-nology/go-git-platform/model"
)

func hunkDiff(path string, body string) *model.MergeDiff {
	return &model.MergeDiff{Files: []*model.ChangedFile{{
		NewPath: path, Diff: body, IsNew: true, Additions: 3,
	}}}
}

// 单文件纯 hunk（GitLab diffs API 形态）：头必带路径，LLM 无需猜。
func TestPrepareFileHeaderSingleFile(t *testing.T) {
	dc := Prepare(context.Background(), hunkDiff("scanner.go", "@@ -0,0 +1,3 @@\n+package validator\n+func Query() {}\n+x"), 0)
	if !strings.HasPrefix(dc.Text, "=== scanner.go") {
		t.Fatalf("diff 文本应以文件头起首: %.60q", dc.Text)
	}
	if !strings.Contains(dc.Text, "scanner.go") || strings.Contains(dc.Text, "validator/validator.go") {
		t.Fatalf("归属头缺失或错位: %.80q", dc.Text)
	}
}

// 多文件：每个文件块都有自己的头，头之间不串。
func TestPrepareFileHeaderPerFile(t *testing.T) {
	md := &model.MergeDiff{Files: []*model.ChangedFile{
		{NewPath: "a.go", Diff: "@@ hunk-a", Additions: 1},
		{NewPath: "b.go", Diff: "@@ hunk-b", Additions: 1},
	}}
	dc := Prepare(context.Background(), md, 0)
	ia, ib := strings.Index(dc.Text, "=== a.go"), strings.Index(dc.Text, "=== b.go")
	if ia < 0 || ib < 0 || ia > ib {
		t.Fatalf("双文件头缺失或乱序: %.100q", dc.Text)
	}
	if strings.Count(dc.Text, "=== ") < 2 {
		t.Fatalf("每个文件块都应带独立头: %.100q", dc.Text)
	}
}

// 新增/删除/修改性质标注进头（LLM 语境补强）。
func TestPrepareFileHeaderMarksNewFile(t *testing.T) {
	dc := Prepare(context.Background(), hunkDiff("scanner.go", "@@ hunk"), 0)
	if !strings.Contains(dc.Text, "新增") {
		t.Fatalf("新增文件头应含性质标注: %.60q", dc.Text)
	}
}

// 头计入 token 预算：小预算下头占字符后 hunk 截断语义仍成立。
func TestPrepareFileHeaderBudgetCounted(t *testing.T) {
	md := &model.MergeDiff{Files: []*model.ChangedFile{
		{NewPath: "a.go", Diff: strings.Repeat("x", 500), Additions: 500},
	}}
	dc := Prepare(context.Background(), md, 200)
	if !strings.HasPrefix(dc.Text, "=== a.go") {
		t.Fatalf("截断场景头仍应起首: %.60q", dc.Text)
	}
}

// diffKindLabel 全分支覆盖（ratchet 回基线）：删除/重命名/修改/新增。
func TestDiffKindLabelBranches(t *testing.T) {
	cases := []struct {
		e    plugin.DiffFile
		want string
	}{
		{plugin.DiffFile{IsNew: true}, "新增"},
		{plugin.DiffFile{IsDeleted: true}, "删除"},
		{plugin.DiffFile{IsRenamed: true}, "重命名"},
		{plugin.DiffFile{}, "修改"},
		{plugin.DiffFile{IsNew: true, IsDeleted: true, IsRenamed: true}, "新增"}, // 优先级
	}
	for _, c := range cases {
		if got := diffKindLabel(c.e); got != c.want {
			t.Errorf("diffKindLabel(%+v) = %q, want %q", c.e, got, c.want)
		}
	}
}

// headerLen 与 diffHeader 同源自洽（预算计数点）。
func TestHeaderLenConsistent(t *testing.T) {
	e := plugin.DiffFile{Path: "x.go", IsNew: true}
	if headerLen(e) != utf8.RuneCountInString(diffHeader(e)) {
		t.Fatal("headerLen 必须与 diffHeader rune 计数一致")
	}
}
