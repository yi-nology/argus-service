package argusctx

// diffprep 边界补充：RawDiff 回填的路径对齐（NewPath/OldPath/未匹配块）、
// 增量预算耗尽后的截断标记、截断清单超过上限的折叠。

import (
	"context"
	"strings"
	"testing"

	"github.com/yi-nology/go-git-platform/model"

	"github.com/yi-nology/argus/internal/plugin"
)

func TestFillFromRawDiffPathAlignment(t *testing.T) {
	raw := strings.Join([]string{
		"diff --git a/old.go b/new.go",
		"--- a/old.go",
		"+++ b/new.go",
		"@@ -1 +1 @@",
		"-old",
		"+new",
		"diff --git a/gone.go b/gone.go",
		"--- a/gone.go",
		"+++ /dev/null",
		"@@ -1 +0,0 @@",
		"-removed",
		"diff --git a/ghost.go b/ghost.go",
		"+++ b/ghost.go",
		"+ghost",
	}, "\n")

	files := []*model.ChangedFile{
		{NewPath: "new.go", OldPath: "old.go", Diff: ""},
		{NewPath: "", OldPath: "gone.go", Diff: ""},
		{NewPath: "", OldPath: "", Diff: "keep"}, // 无路径：不建索引
	}
	got := fillFromRawDiff(files, raw)
	if len(got) != len(files) {
		t.Fatalf("回填数量 = %d, want %d", len(got), len(files))
	}
	if !strings.Contains(got[0].Diff, "a/old.go") || strings.Contains(got[0].Diff, "diff --git") {
		t.Fatalf("NewPath 块未回填或未剥前缀: %q", got[0].Diff)
	}
	if !strings.Contains(got[1].Diff, "b/gone.go") {
		t.Fatalf("删除文件应按 OldPath 回填: %q", got[1].Diff)
	}
	if got[2].Diff != "keep" {
		t.Fatalf("未匹配块不应改写原值: %q", got[2].Diff)
	}
	// 复制语义：原输入不被就地修改（resolveFiles 的调用方仍可能持有旧对象）。
	if files[0].Diff != "" {
		t.Fatalf("fillFromRawDiff 不应修改入参: %q", files[0].Diff)
	}

	if empty := fillFromRawDiff(nil, raw); len(empty) != 0 {
		t.Fatalf("空文件清单应原样返回: %v", empty)
	}
}

func TestPrepareIncrementalBudgetExhaustionMarksSkipped(t *testing.T) {
	bigA := strings.Repeat("+changed-a ", 40) // > totalLimit 100
	bigB := strings.Repeat("+changed-b ", 40)
	md := &model.MergeDiff{Files: []*model.ChangedFile{
		diffFile("a.go", bigA, 1),
		diffFile("b.go", bigB, 1),
	}}
	// 上次 diff 只含无关文件：两个文件都视为有变化。
	last := "diff --git a/other.go b/other.go\n--- a/other.go\n+++ b/other.go\n@@ -1 +1 @@\n-old\n+new\n"

	dc := PrepareIncremental(context.Background(), md, last, 100)
	if !dc.Truncated {
		t.Fatal("预算耗尽应置 Truncated")
	}
	byName := map[string]plugin.DiffFile{}
	for _, f := range dc.Files {
		byName[f.Path] = f
	}
	if f := byName["b.go"]; f.Included || f.AlreadyReviewed {
		t.Fatalf("预算耗尽后的文件应 Included=false: %+v", f)
	}
	if !strings.Contains(dc.Text, "总量截断") {
		t.Fatalf("总量截断注记缺失: %q", dc.Text)
	}
}

func TestPrepareTruncationNoteFoldsOverLimit(t *testing.T) {
	files := make([]*model.ChangedFile, 0, 32)
	for i := 0; i < 32; i++ {
		name := "f" + strings.Repeat("x", i%3) + string(rune('a'+i%26)) + ".go"
		files = append(files, diffFile(name, strings.Repeat("+line ", 40), 1))
	}
	// 预算只够第一个文件（截断）→ 其余 31 个因预算被排除，清单折叠为前 30 + 计数。
	dc := Prepare(context.Background(), &model.MergeDiff{Files: files}, 100)
	if !dc.Truncated {
		t.Fatal("应置 Truncated")
	}
	if !strings.Contains(dc.Text, "其余 1 个略") {
		t.Fatalf("超过 truncationNoteMaxPaths 应折叠计数: %q", tail(dc.Text, 400))
	}
}

func tail(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[len(r)-n:])
}
