package main

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yi-nology/goagent-kernel/archcheck"
)

func TestReplaceBlock(t *testing.T) {
	in := "a\n<!-- BEGIN POLICY -->\nOLD\n<!-- END POLICY -->\nz\n"
	out, err := replaceBlock(in, beginMarker, endMarker, "NEW")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "NEW") || strings.Contains(out, "OLD") {
		t.Fatalf("unexpected: %q", out)
	}
	if !strings.HasPrefix(out, "a\n<!-- BEGIN POLICY -->\n") || !strings.HasSuffix(out, "<!-- END POLICY -->\nz\n") {
		t.Fatalf("markers not preserved: %q", out)
	}
}

func TestReplaceBlockMissing(t *testing.T) {
	if _, err := replaceBlock("no markers", beginMarker, endMarker, "x"); err == nil {
		t.Fatal("missing markers must fail")
	}
	if _, err := replaceBlock("<!-- END POLICY -->"+beginMarker, beginMarker, endMarker, "x"); err == nil {
		t.Fatal("reversed markers must fail")
	}
}

func TestRenderTable(t *testing.T) {
	got := renderTable([]archcheck.Module{
		{ID: "b", Roots: []string{"internal/b"}, Requires: []string{"a"}},
		{ID: "a", Roots: []string{"internal/a"}, Requires: nil, Notes: "L0"},
	})
	if !strings.Contains(got, "| internal/a | （无） | L0 |") {
		t.Fatalf("unexpected table: %s", got)
	}
	if strings.Index(got, "internal/a") > strings.Index(got, "internal/b") {
		t.Fatalf("not sorted by root: %s", got)
	}
}

func writeGenerated(t *testing.T, dir string, files []generatedFile) {
	t.Helper()
	for _, f := range files {
		if err := os.WriteFile(filepath.Join(dir, f.path), []byte(f.content), 0o644); err != nil {
			t.Fatalf("write %s: %v", f.path, err)
		}
	}
}

func TestCheckDocsConsistent(t *testing.T) {
	dir := t.TempDir()
	files := []generatedFile{
		{path: "llms.txt", content: "# Argus\n\nsame\n"},
		{path: "ARCHITECTURE.md", content: "<!-- BEGIN POLICY -->\n| a |\n<!-- END POLICY -->\n"},
	}
	writeGenerated(t, dir, files)

	if err := checkDocs(dir, files); err != nil {
		t.Fatalf("一致时不应报错: %v", err)
	}
}

func TestCheckDocsDrift(t *testing.T) {
	dir := t.TempDir()
	files := []generatedFile{
		{path: "llms.txt", content: "# Argus\n\n新行\n"},
	}
	// 磁盘上留旧内容：check 必须只比对、不写盘，并报出首个差异块。
	writeGenerated(t, dir, []generatedFile{{path: "llms.txt", content: "# Argus\n\n旧行\n"}})

	err := checkDocs(dir, files)
	if !errors.Is(err, errDrift) {
		t.Fatalf("漂移应返回 errDrift, got %v", err)
	}
	for _, want := range []string{"--- llms.txt", "+++ llms.txt", "旧行", "新行", "@@ 第 3 行 @@"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("diff 缺少 %q:\n%s", want, err)
		}
	}
	disk, readErr := os.ReadFile(filepath.Join(dir, "llms.txt"))
	if readErr != nil {
		t.Fatal(readErr)
	}
	if string(disk) != "# Argus\n\n旧行\n" {
		t.Fatalf("-check 不得写文件，磁盘内容变成了 %q", disk)
	}
}

func TestDiffSnippetInsertion(t *testing.T) {
	got := diffSnippet("llms.txt", "a\nb\nc\n", "a\nb\nnew\nc\n")
	if !strings.Contains(got, "+new") {
		t.Fatalf("插入行应显示为 +new:\n%s", got)
	}
	if strings.Contains(got, "-c") || strings.Contains(got, "+c") {
		t.Fatalf("公共后缀不应重复展示:\n%s", got)
	}
}
