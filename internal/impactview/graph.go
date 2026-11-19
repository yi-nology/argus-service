// graph.go —— L1 包依赖图与 L3 测试面：go/parser ImportsOnly 扫工作副本建
// 目录级 import 图，反向闭包推导受影响包。目录=包的映射经 go.mod module 路径
// 前缀换算（import path 剥前缀即仓库相对目录；/vN 后缀剥离）。无 go.mod 的
// 仓库 L1 整层降级（记 Notes，不阻断）。

package impactview

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
)

// goMods 仓库扫描产物：目录 → 包信息（import 图与测试面共用一次扫描）。
type goMods struct {
	modules   []moduleRef         // 仓库内 go.mod 清单（根 + 嵌套多模块；空 = 未识别）
	dirs      []string            // 含 Go 文件的目录（相对路径，有序）
	imports   map[string][]string // 目录 → 该包 import 的仓库内目录（去重有序）
	testFiles map[string]int      // 目录 → *_test.go 数
	fileCount map[string]int      // 目录 → 非 test Go 文件数
}

// moduleRef 仓库内一个 go.mod 的注册项（多模块/子目录布局：如 monorepo 下
// kylin-insights/server 与 kylin-insights/cli 各自成模块）。
type moduleRef struct {
	path string // go.mod 的 module 行（如 kylin-insights/server）
	dir  string // go.mod 所在目录（仓库相对、/ 分隔；根模块 = "."）
}

// scanWorkspace 扫描 dir 下全部 Go 文件（ImportsOnly，只读 import 区）。
// 上限 MaxImportFiles（触发即返回已扫部分 + false，由调用方记 Notes）。
func scanWorkspace(dir string) (*goMods, bool) {
	m := &goMods{
		imports:   map[string][]string{},
		testFiles: map[string]int{},
		fileCount: map[string]int{},
	}
	// go.mod 先于 .go 解析发现（两段式）：单遍字典序下子目录里的 .go 可能先于
	// 同层 go.mod 被解析，依赖注册表的 import 换算会漏映射。
	m.modules = discoverModules(dir)

	fset := token.NewFileSet()
	count := 0
	truncated := false
	// 目录序遍历保证确定性；单文件解析失败跳过（与 codeintel 同纪律）。
	// 外层错误恒弃：walkFn 对每项已自行容错（err→nil 跳过），WalkDir 返回值
	// 只会是 walkFn 的返回——失败即降级面（图少节点），不阻断渲染（P2-10 口径）。
	_ = filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			if d != nil && d.IsDir() {
				name := d.Name()
				if name == ".git" || name == "vendor" || name == "node_modules" || name == ".zcode" {
					return filepath.SkipDir
				}
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || count >= MaxImportFiles {
			if count >= MaxImportFiles {
				truncated = true
			}
			return nil
		}
		count++
		rel, rerr := filepath.Rel(dir, path)
		if rerr != nil {
			return nil
		}
		rel = filepath.ToSlash(rel)
		pkgDir := filepath.ToSlash(filepath.Dir(rel))
		isTest := strings.HasSuffix(rel, "_test.go")
		if isTest {
			m.testFiles[pkgDir]++
		} else {
			m.fileCount[pkgDir]++
		}
		af, perr := parser.ParseFile(fset, path, nil, parser.ImportsOnly|parser.SkipObjectResolution)
		if perr != nil {
			return nil
		}
		if !isTest || m.fileCount[pkgDir] > 0 { // 纯测试包不算独立依赖节点，import 归并主包
			m.imports[pkgDir] = mergeSortedUnique(m.imports[pkgDir], pkgImports(af.Imports, m.modules))
		}
		return nil
	})
	m.dirs = slices.Sorted(maps.Keys(m.fileCount))
	return m, truncated
}

// discoverModules 发现仓库内全部 go.mod（根 + 嵌套多模块/子目录布局）。
// vendor/.git 等目录跳过（与 Go 文件扫描同口径）；单文件解析失败跳过。
func discoverModules(dir string) []moduleRef {
	var mods []moduleRef
	_ = filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			if path != dir {
				switch d.Name() {
				case ".git", "vendor", "node_modules", ".zcode":
					return filepath.SkipDir
				}
			}
			return nil
		}
		if d.Name() != "go.mod" {
			return nil
		}
		rel, rerr := filepath.Rel(dir, path)
		if rerr != nil {
			return nil
		}
		if mod := readModulePath(path); mod != "" {
			mods = append(mods, moduleRef{path: mod, dir: filepath.ToSlash(filepath.Dir(rel))})
		}
		return nil
	})
	// 长路径优先排序：resolveImport 取最长前缀匹配，排序只保遍历确定性。
	slices.SortFunc(mods, func(a, b moduleRef) int {
		if c := strings.Compare(b.path, a.path); c != 0 {
			return c
		}
		return strings.Compare(a.dir, b.dir)
	})
	return mods
}

// readModulePath 读 go.mod 的 module 行（无文件/解析不出返回空）。
func readModulePath(gomod string) string {
	b, err := os.ReadFile(gomod)
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(string(b), "\n") {
		line = strings.TrimSpace(line)
		if rest, ok := strings.CutPrefix(line, "module "); ok {
			return strings.TrimSpace(rest)
		}
	}
	return ""
}

// resolveImport 把 import path 映射为仓库相对目录：多模块注册表里取最长
// module 前缀命中（kylin-insights/server 与 kylin-insights/cli 并存时各归
// 各的模块，互不误吞），剥前缀得包内相对段（/vN 剥离）再拼模块所在目录。
func resolveImport(p string, modules []moduleRef) (string, bool) {
	best := -1
	for i, mod := range modules {
		if p != mod.path && !strings.HasPrefix(p, mod.path+"/") {
			continue
		}
		if best < 0 || len(modules[i].path) > len(modules[best].path) {
			best = i
		}
	}
	if best < 0 {
		return "", false
	}
	mod := modules[best]
	rest := strings.TrimPrefix(strings.TrimPrefix(p, mod.path), "/")
	if rest == "" {
		return "", false // 模块根包自身无目录语义（与旧口径一致）
	}
	rest = stripVersionSuffix(rest)
	if mod.dir == "." {
		return rest, true
	}
	return mod.dir + "/" + rest, true
}

// pkgImports 提取一次 import 声明里落在本仓内的目录（多模块注册表换算）。
func pkgImports(imports []*ast.ImportSpec, modules []moduleRef) []string {
	if len(modules) == 0 {
		return nil
	}
	var out []string
	for _, imp := range imports {
		if imp.Path == nil {
			continue
		}
		p := strings.Trim(imp.Path.Value, `"`)
		if d, ok := resolveImport(p, modules); ok {
			out = append(out, d)
		}
	}
	return out
}

// stripVersionSuffix 剥 import path 尾部 /vN（Go modules 主版本后缀）。
func stripVersionSuffix(p string) string {
	if i := strings.LastIndexByte(p, '/'); i >= 0 {
		if seg := p[i+1:]; len(seg) > 1 && seg[0] == 'v' {
			if v, err := strconv.Atoi(seg[1:]); err == nil && v >= 2 {
				return p[:i]
			}
		}
	}
	return p
}

// mergeSortedUnique 有序去重合并（小集合，O(n²) 可忽略）。
func mergeSortedUnique(base, add []string) []string {
	for _, a := range add {
		found := false
		for _, b := range base {
			if a == b {
				found = true
				break
			}
		}
		if !found {
			base = append(base, a)
		}
	}
	slices.Sort(base)
	return base
}
