package docgen

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeFile(t *testing.T, root, rel, content string) {
	t.Helper()
	p := filepath.Join(root, rel)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestCheckDocIndexMissingEntry(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, "docs/README.md", "# 索引\n")
	writeFile(t, root, "docs/guide/a.md", "# A\n")
	err := checkDocIndex(root)
	if !errors.Is(err, errDocIssue) {
		t.Fatalf("err = %v, want errDocIssue", err)
	}
	if !strings.Contains(err.Error(), "docs/guide/a.md") {
		t.Fatalf("err 未点名缺项: %v", err)
	}
}

func TestCheckDocIndexOK(t *testing.T) {
	root := t.TempDir()
	// 索引通常用 docs/README.md 视角的相对路径（guide/a.md），
	// 也接受仓库相对路径（docs/guide/a.md）。
	writeFile(t, root, "docs/README.md", "- `guide/a.md`\n- docs/ops/b.md\n")
	writeFile(t, root, "docs/guide/a.md", "# A\n")
	writeFile(t, root, "docs/ops/b.md", "# B\n")
	if err := checkDocIndex(root); err != nil {
		t.Fatalf("err = %v, want nil", err)
	}
}

func TestCheckDocLinksDetectsBroken(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, "README.md", "[x](docs/guide/missing.md)\n")
	if err := checkDocLinks(root); !errors.Is(err, errDocIssue) {
		t.Fatalf("err = %v, want errDocIssue", err)
	}
}

func TestCheckDocLinksSkipsCodeFenceAndExternal(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, "README.md",
		"```\n[x](docs/guide/missing.md)\n```\n"+
			"[web](https://example.com/x.md) [anchor](#top)\n"+
			"[ok](docs/guide/a.md)\n")
	writeFile(t, root, "docs/guide/a.md", "# A\n")
	if err := checkDocLinks(root); err != nil {
		t.Fatalf("err = %v, want nil", err)
	}
}

func TestCheckDocLinksArchiveExempt(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, "docs/archive/old.md", "[x](gone.md)\n")
	if err := checkDocLinks(root); err != nil {
		t.Fatalf("archive 应豁免: %v", err)
	}
}