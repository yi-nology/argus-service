package langcap

import (
	"reflect"
	"strings"
	"testing"
)

// TestMatrixExact 锁定语言矩阵的精确内容：任一能力面扩/缩表都必须同步
// 本测试——这是「读到就是事实」的防漂移锚点，不是重复实现。
func TestMatrixExact(t *testing.T) {
	cases := []struct {
		name string
		got  []string
		want []string
	}{
		{"GoExts", GoExts, []string{".go"}},
		{"ShellExts", ShellExts, []string{".sh", ".bash"}},
		{"PyExts", PyExts, []string{".py", ".pyw"}},
		{"JSTSExts", JSTSExts, []string{".js", ".mjs", ".cjs", ".jsx", ".ts", ".tsx"}},
		{"RustExts", RustExts, []string{".rs"}},
		{"JVMExts", JVMExts, []string{".java", ".kt"}},
		{"CExts", CExts, []string{".c", ".h", ".cpp", ".hpp", ".cc", ".cxx"}},
		{"SQLExts", SQLExts, []string{".sql"}},
		{"GraphScanExts", GraphScanExts, []string{
			".sh", ".bash", ".sql", ".py", ".pyw",
			".js", ".mjs", ".cjs", ".jsx", ".ts", ".tsx",
			".rs", ".java", ".kt", ".c", ".h", ".cpp", ".hpp", ".cc", ".cxx",
		}},
		{"FuncDiffExts", FuncDiffExts, []string{
			".sh", ".bash", ".py", ".pyw", ".rs",
			".js", ".mjs", ".cjs", ".jsx", ".ts", ".tsx",
		}},
		{"SemChunkExts", SemChunkExts, []string{
			".go", ".md", ".js", ".ts", ".jsx", ".tsx",
			".py", ".java", ".rs", ".c", ".cc", ".cpp",
			".h", ".hpp", ".rb", ".php", ".cs", ".kt",
			".swift", ".scala", ".sh", ".sql", ".yaml",
			".yml", ".toml", ".proto", ".vue",
		}},
		{"PolySymbolExts", PolySymbolExts, []string{".py", ".js", ".ts", ".tsx", ".jsx"}},
		{"PreciseEdgeLangs", PreciseEdgeLangs, []string{"Go"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if !reflect.DeepEqual(tc.got, tc.want) {
				t.Fatalf("%s = %v, want %v", tc.name, tc.got, tc.want)
			}
		})
	}
}

// TestCapabilitySurfacesDeriveFromFamilies 图层/funcdiff 面必须严格等于对应族
// 并集（保序去重）——派生结果被手改成「差不多」的列表即红。
func TestCapabilitySurfacesDeriveFromFamilies(t *testing.T) {
	if want := concat(ShellExts, SQLExts, PyExts, JSTSExts, RustExts, JVMExts, CExts); !reflect.DeepEqual(GraphScanExts, want) {
		t.Fatalf("GraphScanExts 不等于声明的族并集:\n got %v\nwant %v", GraphScanExts, want)
	}
	if want := concat(ShellExts, PyExts, RustExts, JSTSExts); !reflect.DeepEqual(FuncDiffExts, want) {
		t.Fatalf("FuncDiffExts 不等于声明的族并集:\n got %v\nwant %v", FuncDiffExts, want)
	}
}

// TestSemChunkIndependentSurface SemChunk 是独立历史面（不做族并集）：
// 包含 Go/.md、排除 .bash/.pyw/.mjs/.cjs/.cxx；PolySymbol ⊆ SemChunk。
func TestSemChunkIndependentSurface(t *testing.T) {
	for _, e := range []string{".go", ".md", ".py", ".sh", ".sql", ".yaml", ".vue", ".proto"} {
		if !HasExt(SemChunkExts, e) {
			t.Fatalf("SemChunk 应包含 %q", e)
		}
	}
	for _, e := range []string{".bash", ".pyw", ".mjs", ".cjs", ".cxx", ".mod"} {
		if HasExt(SemChunkExts, e) {
			t.Fatalf("SemChunk 不应包含 %q（独立历史面，扩面需评估 chunk 预算）", e)
		}
	}
	for _, e := range PolySymbolExts {
		if !HasExt(SemChunkExts, e) {
			t.Fatalf("PolySymbol 的 %q 不在 SemChunk 索引面", e)
		}
	}
	// 图层面刻意不含 Go（Go 走 go.mod import 闭包 + AST）。
	if HasExt(GraphScanExts, ".go") {
		t.Fatal("GraphScanExts 不应包含 .go")
	}
	if HasExt(FuncDiffExts, ".go") {
		t.Fatal("FuncDiffExts 不应包含 .go（Go 走 go/parser AST 版）")
	}
}

// TestHasExt 精确匹配、含点、大小写敏感；空集/空串不命中。
func TestHasExt(t *testing.T) {
	cases := []struct {
		name string
		set  []string
		ext  string
		want bool
	}{
		{"hit", GoExts, ".go", true},
		{"no_dot", GoExts, "go", false},
		{"upper", GoExts, ".GO", false},
		{"empty_ext", GoExts, "", false},
		{"empty_set", nil, ".go", false},
		{"other_set", ShellExts, ".py", false},
		{"duplicates", []string{".a", ".a"}, ".a", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := HasExt(tc.set, tc.ext); got != tc.want {
				t.Fatalf("HasExt(%v, %q) = %v, want %v", tc.set, tc.ext, got, tc.want)
			}
		})
	}
}

// TestLangsOf 展示名去重保序；未登记扩展名按原样透出（不静默丢）。
func TestLangsOf(t *testing.T) {
	cases := []struct {
		name string
		set  []string
		want []string
	}{
		{"same_language_dedup", PyExts, []string{"Python"}},
		{"first_seen_order", []string{".rs", ".go", ".rs", ".pyw"}, []string{"Rust", "Go", "Python"}},
		{"unknown_passthrough", []string{".zz", ".go", ".zz"}, []string{".zz", "Go"}},
		{"empty", nil, []string{}},
		{"c_family_merge", CExts, []string{"C/C++"}},
		{"jsts_two_langs", JSTSExts, []string{"JavaScript", "TypeScript"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := LangsOf(tc.set)
			if len(got) != len(tc.want) || !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("LangsOf(%v) = %v, want %v", tc.set, got, tc.want)
			}
		})
	}
}

// TestJoinLangs 描述口径拼接与 LangsOf 同源。
func TestJoinLangs(t *testing.T) {
	cases := []struct {
		name string
		set  []string
		want string
	}{
		{"nil", nil, ""},
		{"go", GoExts, "Go"},
		{"py_dedup", PyExts, "Python"},
		{"c_family", CExts, "C/C++"},
		{"jsts", JSTSExts, "JavaScript/TypeScript"},
		{
			"graph_scan_order",
			GraphScanExts,
			"shell/SQL/Python/JavaScript/TypeScript/Rust/Java/Kotlin/C/C++",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := JoinLangs(tc.set); got != tc.want {
				t.Fatalf("JoinLangs(%v) = %q, want %q", tc.set, got, tc.want)
			}
		})
	}
}

// TestExtLangRegistryConsistency 注册表与能力面互不漏：每个登记扩展名至少
// 出现在一个能力面（无死条目），每个面扩展名都能派生出展示名，且展示名
// 非空、不是裸扩展名。
func TestExtLangRegistryConsistency(t *testing.T) {
	union := concat(GraphScanExts, SemChunkExts, PolySymbolExts)
	for ext, lang := range extLang {
		if !strings.HasPrefix(ext, ".") {
			t.Fatalf("extLang 键 %q 不是带点扩展名", ext)
		}
		if !HasExt(union, ext) {
			t.Fatalf("extLang 登记了 %q 但无任何能力面使用（死条目）", ext)
		}
		if lang == "" || strings.HasPrefix(lang, ".") {
			t.Fatalf("extLang[%q] 展示名不合法: %q", ext, lang)
		}
	}
	for _, set := range [][]string{GraphScanExts, FuncDiffExts, SemChunkExts, PolySymbolExts} {
		for _, e := range set {
			if !strings.HasPrefix(e, ".") || e != strings.ToLower(e) {
				t.Fatalf("能力面扩展名不是小写带点形式: %q", e)
			}
		}
	}
}

// TestPreciseEdgeLangs 精确边语言与 Go 族展示名同源。
func TestPreciseEdgeLangs(t *testing.T) {
	if !reflect.DeepEqual(PreciseEdgeLangs, []string{"Go"}) {
		t.Fatalf("PreciseEdgeLangs = %v", PreciseEdgeLangs)
	}
	if got := LangsOf(GoExts); !reflect.DeepEqual(got, PreciseEdgeLangs) {
		t.Fatalf("精确边语言 %v 与 Go 族展示名 %v 不同源", PreciseEdgeLangs, got)
	}
}

// TestConcat 族并集保序去重。
func TestConcat(t *testing.T) {
	got := concat([]string{".a", ".b"}, []string{".b", ".c"}, nil)
	if want := []string{".a", ".b", ".c"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("concat = %v, want %v", got, want)
	}
	if got := concat(); len(got) != 0 {
		t.Fatalf("空并集 = %v", got)
	}
}
