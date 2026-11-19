package impactview

import (
	"os"
	"path/filepath"
	"testing"
)

// writeGoMod 落一个 go.mod（module 行）。
func writeGoMod(t *testing.T, root, dir, module string) {
	t.Helper()
	p := filepath.Join(root, filepath.FromSlash(dir))
	if err := os.MkdirAll(p, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(p, "go.mod"), []byte("module "+module+"\n\ngo 1.21\n"), 0o644); err != nil {
		t.Fatal(err)
	}
}

// writeGoFile 落一个只带 import 包装的 .go 文件。
func writeGoFile(t *testing.T, root, rel, pkg string, imports ...string) {
	t.Helper()
	p := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	src := "package " + pkg + "\n\nimport (\n"
	for _, imp := range imports {
		src += "\t\"" + imp + "\"\n"
	}
	src += ")\n"
	if err := os.WriteFile(p, []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
}

// TestScanWorkspaceNestedModules 子目录/多模块布局（monorepo 形态：根无
// go.mod，kylin-insights/server 与 kylin-insights/cli 各自成模块）：go.mod
// 注册表须全部发现，import 按最长前缀映射到各自模块目录——此前只认根
// go.mod，此类仓库包级影响面整体降级为文件层。
func TestScanWorkspaceNestedModules(t *testing.T) {
	root := t.TempDir()
	writeGoMod(t, root, "kylin-insights/server", "kylin-insights/server")
	writeGoMod(t, root, "kylin-insights/cli", "kylin-insights/cli")
	// server 模块内互引 + 跨模块引 cli + 引外部包（应忽略）
	writeGoFile(t, root, "kylin-insights/server/querier/model/view/a.go", "view",
		"kylin-insights/server/querier/model/view/dao", "kylin-insights/cli/cmd", "fmt")
	writeGoFile(t, root, "kylin-insights/server/querier/model/view/dao/b.go", "dao")
	// cli 模块内互引
	writeGoFile(t, root, "kylin-insights/cli/cmd/main.go", "main", "kylin-insights/cli/cmd/util")
	writeGoFile(t, root, "kylin-insights/cli/cmd/util/u.go", "util")

	ws, trunc := scanWorkspace(root)
	if trunc {
		t.Fatal("小仓库不应截断")
	}
	if len(ws.modules) != 2 {
		t.Fatalf("应发现 2 个 go.mod: %+v", ws.modules)
	}
	byPath := map[string]string{}
	for _, m := range ws.modules {
		byPath[m.path] = m.dir
	}
	if byPath["kylin-insights/server"] != "kylin-insights/server" ||
		byPath["kylin-insights/cli"] != "kylin-insights/cli" {
		t.Fatalf("模块→目录映射不符: %+v", ws.modules)
	}

	viewImports := ws.imports["kylin-insights/server/querier/model/view"]
	// mergeSortedUnique 按字典序归并：cli/cmd 在 server/... 之前
	want := []string{"kylin-insights/cli/cmd", "kylin-insights/server/querier/model/view/dao"}
	if len(viewImports) != len(want) {
		t.Fatalf("view 包 import 解析不符: %v", viewImports)
	}
	for i, w := range want {
		if viewImports[i] != w {
			t.Fatalf("view 包 import[%d] = %s, want %s", i, viewImports[i], w)
		}
	}
	if got := ws.imports["kylin-insights/cli/cmd"]; len(got) != 1 || got[0] != "kylin-insights/cli/cmd/util" {
		t.Fatalf("cli 包 import 解析不符: %v", got)
	}
}

// TestResolveImportLongestPrefix 同前缀嵌套模块（kylin-insights 与
// kylin-insights/server 并存）取最长命中，短模块不误吞长模块的包。
func TestResolveImportLongestPrefix(t *testing.T) {
	mods := []moduleRef{
		{path: "kylin-insights", dir: "."},
		{path: "kylin-insights/server", dir: "kylin-insights/server"},
	}
	for _, tc := range []struct {
		imp  string
		want string
		ok   bool
	}{
		{"kylin-insights/server/querier", "kylin-insights/server/querier", true},
		{"kylin-insights/docs", "docs", true},
		{"kylin-insights/server", "kylin-insights/server", true}, // 嵌套模块根包 → 模块目录
		{"kylin-insights", ".", true},                            // 根模块根包 → 根目录
		{"other.com/x", "", false},
	} {
		got, ok := resolveImport(tc.imp, mods)
		if ok != tc.ok || got != tc.want {
			t.Fatalf("resolveImport(%q) = %q,%v want %q,%v", tc.imp, got, ok, tc.want, tc.ok)
		}
	}
}
