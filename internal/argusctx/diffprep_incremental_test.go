package argusctx

import (
	"context"
	"strings"
	"testing"

	"github.com/yi-nology/argus/internal/plugin"
	"github.com/yi-nology/go-git-platform/model"
)

// --- PrepareIncremental 测试 ---

func TestPrepareIncremental_NoLastDiff_FullReview(t *testing.T) {
	// lastDiffText 为空 → 等价于全量审查
	md := &model.MergeDiff{Files: []*model.ChangedFile{
		diffFile("a.go", "diff --git a/a.go b/a.go\n+++ b/a.go\n+line1\n", 1),
		diffFile("b.go", "diff --git a/b.go b/b.go\n+++ b/b.go\n+line2\n", 1),
	}}
	dc := PrepareIncremental(context.Background(), md, "", 100000)
	if dc.NoNewChanges {
		t.Fatal("首次审查不应标记 NoNewChanges")
	}
	if !strings.Contains(dc.Text, "+line1") || !strings.Contains(dc.Text, "+line2") {
		t.Fatal("首次审查应包含全部文件 diff")
	}
	for _, f := range dc.Files {
		if f.AlreadyReviewed {
			t.Fatalf("首次审查不应有 AlreadyReviewed 文件: %s", f.Path)
		}
	}
}

func TestPrepareIncremental_IdenticalDiff_NoNewChanges(t *testing.T) {
	diffA := "diff --git a/a.go b/a.go\n+++ b/a.go\n+line1\n"
	diffB := "diff --git a/b.go b/b.go\n+++ b/b.go\n+line2\n"
	lastDiff := diffA + "\n" + diffB

	md := &model.MergeDiff{Files: []*model.ChangedFile{
		diffFile("a.go", diffA, 1),
		diffFile("b.go", diffB, 1),
	}}
	dc := PrepareIncremental(context.Background(), md, lastDiff, 100000)
	if !dc.NoNewChanges {
		t.Fatal("diff 完全一致应标记 NoNewChanges")
	}
	if dc.Text != "" {
		t.Fatalf("NoNewChanges 时 Text 应为空，got: %q", dc.Text)
	}
	for _, f := range dc.Files {
		if !f.AlreadyReviewed {
			t.Fatalf("文件 %s 应标记 AlreadyReviewed", f.Path)
		}
	}
}

func TestPrepareIncremental_PartialChange(t *testing.T) {
	// diff 超过 minIncrementalChars(100) 以避免回退全量审查
	diffA := "diff --git a/a.go b/a.go\n+++ b/a.go\n@@ -1,3 +1,3 @@\n package main\n-old line that is long enough to exceed one hundred characters for the incremental test\n+replaced with a new line\n"
	diffB := "diff --git a/b.go b/b.go\n+++ b/b.go\n@@ -1,3 +1,3 @@\n package util\n-func old() { return }\n+func New() { return }\n"
	lastDiff := diffA + "\n" + diffB

	// a.go 有变化，b.go 不变
	newDiffA := "diff --git a/a.go b/a.go\n+++ b/a.go\n@@ -1,3 +1,3 @@\n package main\n-old line that changed again in this push\n+final replacement line\n"
	md := &model.MergeDiff{Files: []*model.ChangedFile{
		diffFile("a.go", newDiffA, 1),
		diffFile("b.go", diffB, 1),
	}}
	dc := PrepareIncremental(context.Background(), md, lastDiff, 100000)
	if dc.NoNewChanges {
		t.Fatal("有文件变化不应标记 NoNewChanges")
	}
	if !strings.Contains(dc.Text, "final replacement") {
		t.Fatal("变化文件的 diff 应进入 Text")
	}
	if strings.Contains(dc.Text, "func New()") {
		t.Fatal("未变化文件的 diff 不应进入 Text")
	}
	filesByName := map[string]plugin.DiffFile{}
	for _, f := range dc.Files {
		filesByName[f.Path] = f
	}
	if filesByName["a.go"].AlreadyReviewed {
		t.Fatal("a.go 有变化不应 AlreadyReviewed")
	}
	if !filesByName["b.go"].AlreadyReviewed {
		t.Fatal("b.go 未变化应 AlreadyReviewed")
	}
}

func TestPrepareIncremental_NewFile(t *testing.T) {
	diffA := "diff --git a/a.go b/a.go\n+++ b/a.go\n@@ -1,3 +1,3 @@\n package main\n-func old() { return }\n+func main() { fmt.Println() }\n"
	lastDiff := diffA

	// 新增 c.go（diff 超过 minIncrementalChars=100）
	diffC := "diff --git a/c.go b/c.go\n+++ b/c.go\n@@ -0,0 +1,3 @@\n+package util\n+func NewHelper() error {\n+    return nil\n+}\n"
	md := &model.MergeDiff{Files: []*model.ChangedFile{
		diffFile("a.go", diffA, 1),
		diffFile("c.go", diffC, 1),
	}}
	dc := PrepareIncremental(context.Background(), md, lastDiff, 100000)
	if dc.NoNewChanges {
		t.Fatal("有新文件不应 NoNewChanges")
	}
	if !strings.Contains(dc.Text, "NewHelper") {
		t.Fatal("新文件 diff 应进入 Text")
	}
	filesByName := map[string]plugin.DiffFile{}
	for _, f := range dc.Files {
		filesByName[f.Path] = f
	}
	if !filesByName["a.go"].AlreadyReviewed {
		t.Fatal("a.go 未变化应 AlreadyReviewed")
	}
	if filesByName["c.go"].AlreadyReviewed {
		t.Fatal("c.go 是新文件不应 AlreadyReviewed")
	}
}

func TestPrepareIncremental_DeletedFile(t *testing.T) {
	diffA := "diff --git a/a.go b/a.go\n+++ b/a.go\n@@ -1,3 +1,3 @@\n package main\n-func old() { return }\n+func main() { fmt.Println() }\n"
	diffB := "diff --git a/b.go b/b.go\n+++ b/b.go\n@@ -1,3 +1,3 @@\n package util\n-func helper() error {\n+func Helper() error {\n    return nil\n}\n"
	lastDiff := diffA + "\n" + diffB

	// b.go 被删除（deleted diff 超过 minIncrementalChars）
	md := &model.MergeDiff{Files: []*model.ChangedFile{
		diffFile("a.go", diffA, 1),
		{OldPath: "b.go", NewPath: "", IsDeleted: true, Diff: "diff --git a/b.go b/b.go\ndeleted file mode 100644\n--- a/b.go\n+++ /dev/null\n@@ -1,5 +0,0 @@\n-package util\n-func helper() error {\n-    return nil\n-}\n"},
	}}
	dc := PrepareIncremental(context.Background(), md, lastDiff, 100000)
	if dc.NoNewChanges {
		t.Fatal("有文件删除不应 NoNewChanges")
	}
	filesByName := map[string]plugin.DiffFile{}
	for _, f := range dc.Files {
		filesByName[f.Path] = f
	}
	if !filesByName["a.go"].AlreadyReviewed {
		t.Fatal("a.go 未变化应 AlreadyReviewed")
	}
	if filesByName["b.go"].AlreadyReviewed {
		t.Fatal("b.go 被删除不应 AlreadyReviewed")
	}
}

func TestPrepareIncremental_SmallDiffFallback(t *testing.T) {
	// 增量 diff 太短（< 100 字符）→ 回退全量审查
	lastDiff := "diff --git a/a.go b/a.go\n+++ b/a.go\n+old\n"
	newDiff := "diff --git a/a.go b/a.go\n+++ b/a.go\n+x\n" // ~40 chars

	md := &model.MergeDiff{Files: []*model.ChangedFile{
		diffFile("a.go", newDiff, 1),
	}}
	dc := PrepareIncremental(context.Background(), md, lastDiff, 100000)
	// 回退全量后，NoNewChanges 应为 false，Text 应有内容
	if dc.NoNewChanges {
		t.Fatal("回退全量不应 NoNewChanges")
	}
	if dc.Text == "" {
		t.Fatal("回退全量后 Text 不应为空")
	}
	// 回退全量时 AlreadyReviewed 应全为 false
	for _, f := range dc.Files {
		if f.AlreadyReviewed {
			t.Fatalf("回退全量后不应有 AlreadyReviewed: %s", f.Path)
		}
	}
}

func TestPrepareIncremental_NoiseFiltered(t *testing.T) {
	// 噪声文件即使有变化也不进文本
	lastGoSum := "diff --git a/go.sum b/go.sum\n+++ b/go.sum\n+old\n"
	lastDiff := lastGoSum

	newGoSum := "diff --git a/go.sum b/go.sum\n+++ b/go.sum\n+new\n"
	md := &model.MergeDiff{Files: []*model.ChangedFile{
		diffFile("go.sum", newGoSum, 1),
	}}
	dc := PrepareIncremental(context.Background(), md, lastDiff, 100000)
	if strings.Contains(dc.Text, "go.sum") {
		t.Fatal("噪声文件不应进 Text")
	}
	// 噪声文件不标记 AlreadyReviewed（走 noise 分支）
	for _, f := range dc.Files {
		if f.Path == "go.sum" && f.AlreadyReviewed {
			t.Fatal("噪声文件不应标记 AlreadyReviewed")
		}
	}
}

func TestPrepareIncremental_RawDiffBackfill(t *testing.T) {
	// ChangedFile.Diff 为空，内容在 RawDiff → 自动回填
	lastDiff := "diff --git a/a.go b/a.go\n+++ b/a.go\n+old line\n"
	raw := "diff --git a/a.go b/a.go\n+++ b/a.go\n@@ +1 @@\n+new line\n"

	md := &model.MergeDiff{
		Files:   []*model.ChangedFile{{NewPath: "a.go", Additions: 1}},
		RawDiff: raw,
	}
	dc := PrepareIncremental(context.Background(), md, lastDiff, 100000)
	if dc.NoNewChanges {
		t.Fatal("RawDiff 回填后有变化不应 NoNewChanges")
	}
	if !strings.Contains(dc.Text, "+new line") {
		t.Fatal("RawDiff 回填内容应进 Text")
	}
}
