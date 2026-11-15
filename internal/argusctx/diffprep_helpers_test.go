package argusctx

import (
	"strings"
	"testing"

	"github.com/yi-nology/go-git-platform/provider"
)

func TestFillFromRawDiff(t *testing.T) {
	raw := "diff --git a/a.go b/a.go\n+++ b/a.go\n@@ +1 @@\n+line1\ndiff --git b/b.go b/b.go\n+++ b/b.go\n@@ +1 @@\n+line2\n"
	files := []*provider.ChangedFile{{NewPath: "a.go"}, {NewPath: "b.go"}}
	out := fillFromRawDiff(files, raw)
	if len(out) != 2 {
		t.Fatalf("应回填 2 个文件，got %d", len(out))
	}
	// 存储值不带 "diff --git " 前缀（与 ChangedFile.Diff 形态一致，读写两端契约）
	if strings.HasPrefix(out[0].Diff, "diff --git ") || !strings.Contains(out[0].Diff, "+line1") {
		t.Fatalf("a.go 回填错误: %q", out[0].Diff)
	}
	if !strings.Contains(out[1].Diff, "+line2") {
		t.Fatalf("b.go 回填错误: %q", out[1].Diff)
	}
	// 不改动调用方切片（浅拷贝语义）
	if files[0].Diff != "" {
		t.Fatalf("不得改写调用方原文件: %q", files[0].Diff)
	}
}

func TestFillFromRawDiff_Empty(t *testing.T) {
	files := []*provider.ChangedFile{{NewPath: "a.go"}}
	if out := fillFromRawDiff(files, ""); out[0].Diff != "" {
		t.Fatalf("空 RawDiff 不应回填, got %q", out[0].Diff)
	}
}

// 深挖修复回归：目录噪声规则按路径段匹配，"cargo-build/"、"ui-dist/" 不再被
// "build/"、"dist/" 的无边界子串匹配误伤。
func TestNoiseSegmentMatch(t *testing.T) {
	noiseCases := map[string]bool{
		"a/build/b.go":      true,  // 目录段命中
		"vendor/x/y.go":     true,  // 顶层 vendor
		"cmd/a/vendor/x.go": true,  // 嵌套 vendor
		"ui-dist/x.go":      false, // 子串误伤修复
		"cargo-build/x.go":  false, // 子串误伤修复
		"myvendor/x.go":     false, // 子串误伤修复
		"go.sum":            true,  // 文件名精确匹配
		"x.pb.go":           true,  // 后缀匹配
		"distillary/d/a.go": false, // 目录段不等同前缀
	}
	for p, want := range noiseCases {
		if got := noise(p); got != want {
			t.Errorf("noise(%q) = %v, want %v", p, got, want)
		}
	}
}

// 深挖修复回归：噪声/二进制文件不进截断说明（它们是按规则排除，不是预算截断）。
func TestTruncationNoteExcludesNoise(t *testing.T) {
	var big strings.Builder
	for range 1000 {
		big.WriteString("+code\n")
	}
	md := &provider.MergeDiff{Files: []*provider.ChangedFile{
		diffFile("a.go", big.String(), 1000),
		diffFile("b.go", big.String(), 1000),
		diffFile("go.sum", big.String(), 1000),
	}}
	dc := Prepare(md, 500)
	if !dc.Truncated {
		t.Fatal("应标记 Truncated")
	}
	if strings.Contains(dc.Text, "- go.sum") {
		t.Fatal("噪声文件不应出现在截断说明里")
	}
	if !strings.Contains(dc.Text, "- b.go") {
		t.Fatalf("预算排除的 b.go 应在截断说明中，文本尾:\n%s", dc.Text[len(dc.Text)-400:])
	}
}
