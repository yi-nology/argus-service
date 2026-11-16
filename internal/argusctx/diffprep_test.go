package argusctx

import (
	"strings"
	"testing"

	"github.com/yi-nology/go-git-platform/model"
)

func diffFile(path, text string, adds int) *model.ChangedFile {
	return &model.ChangedFile{OldPath: path, NewPath: path, Diff: text, Additions: adds}
}

func TestPrepareCleaning(t *testing.T) {
	md := &model.MergeDiff{
		Files: []*model.ChangedFile{
			diffFile("go.sum", "diff --git a/go.sum b/go.sum\n+lots", 100),
			diffFile("vendor/x/y.go", "+vendor code", 5),
			diffFile("proto/gen.pb.go", "+generated", 5),
			diffFile("main.go", "+package main\n+func main() {}", 2),
			diffFile("logo.png", "", 0),
		},
	}
	md.Files[4].IsBinary = true

	dc := Prepare(md, 100000)
	paths := map[string]struct {
		Included bool
		IsBinary bool
	}{}
	for _, f := range dc.Files {
		paths[f.Path] = struct {
			Included bool
			IsBinary bool
		}{f.Included, f.IsBinary}
	}

	if paths["go.sum"].Included {
		t.Fatal("lockfile 不应进入 diff 文本")
	}
	if paths["vendor/x/y.go"].Included {
		t.Fatal("vendor 不应进入 diff 文本")
	}
	if paths["proto/gen.pb.go"].Included {
		t.Fatal("生成物不应进入 diff 文本")
	}
	if paths["logo.png"].Included || !paths["logo.png"].IsBinary {
		t.Fatal("二进制不进入文本但保留 IsBinary 元信息")
	}
	if !paths["main.go"].Included {
		t.Fatal("正常文件应进入 diff 文本")
	}
	if !strings.Contains(dc.Text, "package main") {
		t.Fatal("diff 文本缺正常文件内容")
	}
	if strings.Contains(dc.Text, "vendor code") {
		t.Fatal("diff 文本混入 vendor 内容")
	}
	if dc.Truncated {
		t.Fatal("未超限不应标记截断")
	}
	// 元信息保留全部文件
	if len(dc.Files) != 5 {
		t.Fatalf("Files 元信息应含全部 5 个文件，got %d", len(dc.Files))
	}
}

func TestPrepareTotalTruncation(t *testing.T) {
	var big strings.Builder
	for range 1000 {
		big.WriteString("+line of code that matters\n")
	}
	md := &model.MergeDiff{Files: []*model.ChangedFile{
		diffFile("a.go", big.String(), 1000),
		diffFile("b.go", big.String(), 1000),
	}}
	dc := Prepare(md, 500)
	if !dc.Truncated {
		t.Fatal("超限应标记 Truncated")
	}
	// 截断说明（被排除文件清单）会附加在文本尾部，放宽为"主体 500 + 说明 < 900"
	if len(dc.Text) > 1400 {
		t.Fatalf("总量截断未生效: %d", len(dc.Text))
	}
	if !strings.Contains(dc.Text, "截断说明") || !strings.Contains(dc.Text, "- b.go") {
		t.Fatal("被预算排除的文件应在截断说明中留痕")
	}
}

func TestPrepareSingleFileTruncation(t *testing.T) {
	var big strings.Builder
	for range SingleFileMaxChars + 100 {
		big.WriteString("x")
	}
	md := &model.MergeDiff{Files: []*model.ChangedFile{diffFile("a.go", big.String(), 1)}}
	dc := Prepare(md, 0) // 0 = 不限总量
	if len(dc.Text) >= SingleFileMaxChars+100 {
		t.Fatal("单文件截断未生效")
	}
	if !strings.Contains(dc.Text, "截断") {
		t.Fatal("缺截断标记")
	}
}

// v3.0.1 真实试点回归：gitea 后端 ChangedFile.Diff 为空、内容在 RawDiff → 按文件切块回填。
func TestPrepareFillFromRawDiff(t *testing.T) {
	raw := "diff --git a/login.go b/login.go\nnew file mode 100644\n--- /dev/null\n+++ b/login.go\n@@ -0,0 +1,3 @@\n+package auth\n+\n+func login() {}\n" +
		"diff --git a/go.sum b/go.sum\n--- /dev/null\n+++ b/go.sum\n@@ -0,0 +1,1 @@\n+huge lockfile\n"
	md := &model.MergeDiff{
		Files: []*model.ChangedFile{
			{NewPath: "login.go", Additions: 3},
			{NewPath: "go.sum", Additions: 1},
		},
		RawDiff: raw,
	}
	dc := Prepare(md, 100000)
	got := map[string]struct {
		Included bool
		IsBinary bool
	}{}
	for _, f := range dc.Files {
		got[f.Path] = struct {
			Included bool
			IsBinary bool
		}{f.Included, f.IsBinary}
	}
	if !got["login.go"].Included {
		t.Fatal("login.go 应被回填并进入文本")
	}
	if !strings.Contains(dc.Text, "func login()") {
		t.Fatalf("diff 文本缺 RawDiff 切块内容:\n%s", dc.Text)
	}
	if got["go.sum"].Included {
		t.Fatal("go.sum 是噪声，切块后仍应被剔除")
	}
}
