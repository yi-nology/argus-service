// scriptgraph_resolve.go —— 词法引用解析：shell/py/js/rust/jvm/c include → 仓内文件。

package impactview

import (
	"path/filepath"
	"strings"
)

// resolveSourcePath 归一化 source 行参数：先剥"引号 + 脚本目录惯用前缀"
// （`"$(dirname "$0")/` 族，含空格无法按字段切），再取首个参数剥外层引号，
// 按当前文件目录相对解析。仍残留变量展开或越出仓库根/绝对路径一律放弃
// （宁缺勿滥：错边比无边毒）。
func resolveSourcePath(base, rawLine string) string {
	line := strings.TrimSpace(rawLine)
	for _, frag := range []string{
		`"$(dirname "$0")/`, `$(dirname "$0")/`, `"$(dirname "$0")"`, `$(dirname "$0")`,
		`$(cd "$(dirname "$0")" && pwd)/`, `${SCRIPT_DIR}/`, "${SCRIPT_DIR}/",
		`$SCRIPT_DIR/`, `${BASH_SOURCE%/*}/`,
	} {
		if strings.HasPrefix(line, frag) {
			line = strings.TrimPrefix(strings.TrimPrefix(line, frag), "/")
			break
		}
	}
	fields := strings.Fields(line)
	if len(fields) == 0 {
		return ""
	}
	p := strings.Trim(fields[0], `"`)
	p = strings.Trim(p, `'`)
	if p == "" || strings.HasPrefix(p, "/") || strings.ContainsAny(p, "$({}") {
		return ""
	}
	clean := filepath.Clean(filepath.Join(base, p))
	// 越过仓库根的相对引用（../../x）不入图：目标在本仓外，无从归属
	if strings.HasPrefix(clean, "..") {
		return ""
	}
	return filepath.ToSlash(clean)
}

// resolvePyModule Python import → 仓内文件。相对点前缀按层级上溯（`.`=当前
// 目录、`..`=父目录）；绝对模块从仓库根解析。仅命中仓内已知文件（x/y.py 或
// x/y/__init__.py）才返回路径——第三方/动态 import 不入图（宁缺勿滥）。
func resolvePyModule(base, mod string, files map[string]bool, suffixIdx map[string]string) string {
	if mod == "" {
		return ""
	}
	rel := mod
	up := 0
	for strings.HasPrefix(rel, ".") {
		rel = rel[1:]
		up++
	}
	p := strings.ReplaceAll(rel, ".", "/")
	var root string
	switch {
	case up > 0:
		// `from . import`（空尾）指包自身目录 __init__；`from .x import` 当前目录
		root = base
		for i := 1; i < up; i++ {
			root = filepath.Dir(root)
		}
		if rel == "" {
			py := pyFileFor(filepath.ToSlash(filepath.Clean(root)), files)
			if py == "" {
				return ""
			}
			// `from . import` 装载的是包 __init__：指向目录自身即够（边语义=装载）
			return py
		}
	default:
		// 绝对模块（无点前缀）：包路径相对源根（src 布局），段边界后缀索引命中
		// 即归属；未命中（第三方/动态）不入图。候选=模块路径本身（包 __init__
		// 形态已在索引键里）+ .py 后缀（模块文件形态）
		if got, ok := suffixIdx[p]; ok {
			return got
		}
		if got, ok := suffixIdx[p+".py"]; ok {
			return got
		}
		return ""
	}
	return pyFileFor(filepath.ToSlash(filepath.Clean(filepath.Join(root, p))), files)
}

func pyFileFor(c string, files map[string]bool) string {
	if strings.HasPrefix(c, "..") {
		return ""
	}
	for _, cand := range []string{c + ".py", filepath.Join(c, "__init__.py")} {
		cand = filepath.ToSlash(filepath.Clean(cand))
		if files[cand] {
			return cand
		}
	}
	return ""
}

// resolveJSRel JS/TS import spec → 仓内文件。仅相对引用（./ ../）；候选覆盖
// 显式扩展名与 index 文件；裸名（node_modules 域）一律放弃。
func resolveJSRel(relFile, spec string, files map[string]bool) string {
	if spec == "" || (!strings.HasPrefix(spec, "./") && !strings.HasPrefix(spec, "../")) {
		return ""
	}
	c := filepath.ToSlash(filepath.Clean(filepath.Join(filepath.Dir(relFile), spec)))
	if strings.HasPrefix(c, "..") {
		return ""
	}
	for _, cand := range []string{c, c + ".js", c + ".mjs", c + ".cjs", c + ".ts", c + ".tsx", c + ".jsx",
		filepath.Join(c, "index.js"), filepath.Join(c, "index.mjs"), filepath.Join(c, "index.ts"), filepath.Join(c, "index.tsx")} {
		if files[cand] {
			return cand
		}
	}
	return ""
}

// resolveRustMod `mod x;` → x.rs 或 x/mod.rs（相对当前文件目录）。
func resolveRustMod(relFile, name string, files map[string]bool) string {
	if name == "" {
		return ""
	}
	c := filepath.ToSlash(filepath.Clean(filepath.Join(filepath.Dir(relFile), name)))
	for _, cand := range []string{c + ".rs", filepath.Join(c, "mod.rs")} {
		if files[cand] {
			return cand
		}
	}
	return ""
}

// resolveRustUse use 路径 → 仓内文件。crate:: 从归属 crate 的 src 目录解析；
// super::/self:: 相对当前文件；外部 crate 名（首段非关键字）不入图。
// 候选链：全路径与去尾段各试 .rs / mod.rs（尾段可能是类型而非模块）。
func resolveRustUse(relFile, usePath string, crateSrc map[string]bool, files map[string]bool) string {
	segs := strings.Split(usePath, "::")
	if len(segs) == 0 || segs[0] == "" {
		return ""
	}
	var root string
	rest := segs
	switch segs[0] {
	case "crate":
		root = crateRootFor(relFile, crateSrc)
		rest = segs[1:]
	case "super":
		root = filepath.Dir(filepath.Dir(relFile))
		rest = segs[1:]
	case "self":
		root = filepath.Dir(relFile)
		rest = segs[1:]
	default:
		return "" // 外部 crate（serde::…）或裸 crate 名引用，无从归属
	}
	if len(rest) == 0 || root == "" {
		return ""
	}
	tryCands := func(parts []string) string {
		c := filepath.ToSlash(filepath.Clean(filepath.Join(append([]string{root}, parts...)...)))
		if strings.HasPrefix(c, "..") {
			return ""
		}
		for _, cand := range []string{c + ".rs", filepath.Join(c, "mod.rs")} {
			if files[cand] {
				return cand
			}
		}
		return ""
	}
	if got := tryCands(rest); got != "" {
		return got
	}
	if len(rest) > 1 {
		return tryCands(rest[:len(rest)-1])
	}
	return ""
}

// crateRootFor 归属 crate 的 src 目录（最长前缀匹配；无命中退化当前目录）。
func crateRootFor(relFile string, crateSrc map[string]bool) string {
	d := filepath.Dir(relFile)
	best := ""
	for src := range crateSrc {
		if d == src || strings.HasPrefix(d, src+"/") {
			if len(src) > len(best) {
				best = src
			}
		}
	}
	if best != "" {
		return best
	}
	return d
}

// resolveJvmImport Java/Kotlin import（com.foo.Bar）→ com/foo/Bar.java|.kt。
// 通配 .* 不入图。
func resolveJvmImport(imp string, suffixIdx map[string]string) string {
	if imp == "" || strings.HasSuffix(imp, ".*") {
		return ""
	}
	c := filepath.ToSlash(strings.ReplaceAll(imp, ".", "/"))
	if strings.HasPrefix(c, "..") {
		return ""
	}
	if got, ok := suffixIdx[c+".java"]; ok {
		return got
	}
	if got, ok := suffixIdx[c+".kt"]; ok {
		return got
	}
	return ""
}

// resolveCInclude #include "x.h" → 相对当前文件目录解析（引号形态=本仓头，
// 含 gcc 惯用的 ./ ../ 前缀）；越出仓库根不入图。
func resolveCInclude(relFile, inc string, files map[string]bool) string {
	if inc == "" || strings.HasPrefix(inc, "/") {
		return ""
	}
	c := filepath.ToSlash(filepath.Clean(filepath.Join(filepath.Dir(relFile), inc)))
	if strings.HasPrefix(c, "..") {
		return ""
	}
	if files[c] {
		return c
	}
	return ""
}

func containsStr(hay []string, needle string) bool {
	for _, h := range hay {
		if h == needle {
			return true
		}
	}
	return false
}

func appendUnique(xs []string, x string) []string {
	if containsStr(xs, x) {
		return xs
	}
	return append(xs, x)
}
