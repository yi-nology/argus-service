// scriptgraph_scan.go —— 词法面采集与单文件解析：遍历/索引/各语言 parse*。

package impactview

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/yi-nology/argus/internal/langcap"
)

// scanScripts 遍历收集词法面文件（langcap.GraphScanExts）并解析。预算到点即停。
func scanScripts(dir string, deadline time.Time) *scriptGraph {
	paths := scanScriptPaths(dir)
	if len(paths) == 0 {
		return nil
	}
	slices.Sort(paths)
	fileSet, suffixIdx, crateSrc := buildScanIndexes(paths)
	sg := &scriptGraph{byRel: map[string]*scriptFile{}}
	for _, rel := range paths {
		if time.Now().After(deadline) {
			break
		}
		b, err := os.ReadFile(filepath.Join(dir, rel))
		if err != nil || len(b) > maxScriptReadBytes {
			continue
		}
		sg.byRel[rel] = parseScriptFile(rel, string(b), fileSet, suffixIdx, crateSrc)
	}
	if len(sg.byRel) == 0 {
		return nil
	}
	return sg
}

// scanScriptPaths 递归收集词法面文件相对路径（排除 .git/vendor/node_modules/
// .zcode，封顶 MaxScriptFiles；外层错误恒弃，walkFn 逐项容错——失败即降级面，
// 不阻断构建（P2-10 口径））。
func scanScriptPaths(dir string) []string {
	var paths []string
	_ = filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			switch d.Name() {
			case ".git", "vendor", "node_modules", ".zcode":
				return filepath.SkipDir
			}
			return nil
		}
		if langcap.HasExt(langcap.GraphScanExts, filepath.Ext(path)) {
			if len(paths) >= MaxScriptFiles {
				return nil
			}
			if rel, rerr := filepath.Rel(dir, path); rerr == nil {
				paths = append(paths, rel)
			}
		}
		return nil
	})
	return paths
}

// buildScanIndexes 解析用索引三件套：fileSet（仓内已知文件）、suffixIdx（段
// 边界后缀 → 完整 rel，JVM/Python 绝对模块共用）、crateSrc（Rust crate 根）。
func buildScanIndexes(paths []string) (map[string]bool, map[string]string, map[string]bool) {
	fileSet := make(map[string]bool, len(paths))
	for _, p := range paths {
		fileSet[p] = true
	}
	// 段边界后缀索引（v5.6.5）：包路径相对源根而非仓库根（Maven src/main/java、
	// Python src 布局同病）——键=任意段边界起的路径后缀，值=完整 rel（先到先得，
	// paths 已排序保证确定性）。JVM import 与 Python 绝对模块共用。
	suffixIdx := map[string]string{}
	for _, p := range paths {
		segs := strings.Split(p, "/")
		for i := range segs {
			key := strings.Join(segs[i:], "/")
			if _, ok := suffixIdx[key]; !ok {
				suffixIdx[key] = p
			}
		}
	}
	// Rust crate 根：**/src/{lib,main}.rs 所在的 src 目录（use crate:: 解析锚点；
	// workspace 多 crate 各自映射，取最长前缀归属）
	crateSrc := map[string]bool{}
	for _, p := range paths {
		d, b := filepath.Dir(p), filepath.Base(p)
		if (b == "lib.rs" || b == "main.rs") && strings.HasSuffix(d, "/src") {
			crateSrc[d] = true
		}
	}
	return fileSet, suffixIdx, crateSrc
}

// parseScriptFile 按扩展名分派单文件词法解析（未识别扩展返回空词法面文件，
// 与原样入图保持一致）。
func parseScriptFile(rel, text string, fileSet map[string]bool, suffixIdx map[string]string, crateSrc map[string]bool) *scriptFile {
	sf := &scriptFile{rel: rel}
	switch ext := filepath.Ext(rel); {
	case ext == ".sh" || ext == ".bash":
		parseShellScript(sf, rel, text)
	case ext == ".py" || ext == ".pyw":
		parsePyScript(sf, rel, text, fileSet, suffixIdx)
	case ext == ".js" || ext == ".mjs" || ext == ".cjs" || ext == ".ts" || ext == ".jsx" || ext == ".tsx":
		parseJSScript(sf, rel, text, fileSet)
	case ext == ".rs":
		parseRustScript(sf, rel, text, crateSrc, fileSet)
	case ext == ".java" || ext == ".kt":
		parseJVMScript(sf, text, suffixIdx)
	case ext == ".c" || ext == ".h" || ext == ".cpp" || ext == ".hpp" || ext == ".cc" || ext == ".cxx":
		parseCScript(sf, rel, text, fileSet)
	case ext == ".sql":
		parseSQLScript(sf, text)
	}
	return sf
}

// parseShellScript shell：source 装载 + 函数定义 + 全量词元。
func parseShellScript(sf *scriptFile, rel, text string) {
	base := filepath.Dir(rel)
	for _, m := range reShellSrc.FindAllStringSubmatch(text, -1) {
		if p := resolveSourcePath(base, m[1]); p != "" {
			sf.srcs = appendUnique(sf.srcs, p)
		}
	}
	for _, m := range reFuncDef.FindAllStringSubmatch(text, MaxDefsPerFile) {
		name := m[1]
		if name == "" {
			name = m[2]
		}
		if name != "" {
			sf.defs = appendUnique(sf.defs, name)
		}
	}
	sf.tokens = map[string]bool{}
	for _, tk := range reToken.FindAllString(text, -1) {
		sf.tokens[tk] = true
	}
}

// parsePyScript Python：import 面（v5.6.3，相对/绝对模块 → 仓内文件解析，
// 宁缺勿滥）+ def 进函数级 caller 检测 + 全量词元。
func parsePyScript(sf *scriptFile, rel, text string, fileSet map[string]bool, suffixIdx map[string]string) {
	for _, m := range rePyImport.FindAllStringSubmatch(text, -1) {
		mods := []string{m[1]}
		if m[2] != "" {
			mods = strings.Split(m[2], ",")
		}
		for _, mod := range mods {
			if p := resolvePyModule(filepath.Dir(rel), strings.TrimSpace(mod), fileSet, suffixIdx); p != "" {
				sf.srcs = appendUnique(sf.srcs, p)
			}
		}
	}
	for _, m := range rePyFunc.FindAllStringSubmatch(text, MaxDefsPerFile) {
		if m[1] != "" {
			sf.defs = appendUnique(sf.defs, m[1])
		}
	}
	sf.tokens = map[string]bool{}
	for _, tk := range reToken.FindAllString(text, -1) {
		sf.tokens[tk] = true
	}
}

// parseJSScript JS/TS：只做文件级相对 import（裸名=node_modules 域不入图）；
// 函数级刻意不做（箭头函数/重导出形态太多，误报面失控）。
func parseJSScript(sf *scriptFile, rel, text string, fileSet map[string]bool) {
	for _, m := range reJSImport.FindAllStringSubmatch(text, -1) {
		spec := m[1]
		for _, g := range m[2:] {
			if spec == "" {
				spec = g
			}
		}
		if p := resolveJSRel(rel, spec, fileSet); p != "" {
			sf.srcs = appendUnique(sf.srcs, p)
		}
	}
}

// parseRustScript Rust：mod 声明 + use 路径（crate:: 归属锚点）+ fn 定义 + 词元。
func parseRustScript(sf *scriptFile, rel, text string, crateSrc map[string]bool, fileSet map[string]bool) {
	for _, m := range reRustMod.FindAllStringSubmatch(text, -1) {
		if p := resolveRustMod(rel, m[1], fileSet); p != "" {
			sf.srcs = appendUnique(sf.srcs, p)
		}
	}
	for _, m := range reRustUse.FindAllStringSubmatch(text, -1) {
		if p := resolveRustUse(rel, m[1], crateSrc, fileSet); p != "" {
			sf.srcs = appendUnique(sf.srcs, p)
		}
	}
	for _, m := range reRustFunc.FindAllStringSubmatch(text, MaxDefsPerFile) {
		if m[1] != "" {
			sf.defs = appendUnique(sf.defs, m[1])
		}
	}
	sf.tokens = map[string]bool{}
	for _, tk := range reToken.FindAllString(text, -1) {
		sf.tokens[tk] = true
	}
}

// parseJVMScript JVM 系：只做文件级跨包 import（方法定义形态太多不做函数级；
// 同包同目录的隐式耦合不画——会密成毛线团）。
func parseJVMScript(sf *scriptFile, text string, suffixIdx map[string]string) {
	for _, m := range reJavaImport.FindAllStringSubmatch(text, -1) {
		if p := resolveJvmImport(m[1], suffixIdx); p != "" {
			sf.srcs = appendUnique(sf.srcs, p)
		}
	}
}

// parseCScript C/C++：双引号 #include（本仓相对头文件）解析。
func parseCScript(sf *scriptFile, rel, text string, fileSet map[string]bool) {
	for _, m := range reCInclude.FindAllStringSubmatch(text, -1) {
		if p := resolveCInclude(rel, m[1], fileSet); p != "" {
			sf.srcs = appendUnique(sf.srcs, p)
		}
	}
}

// parseSQLScript SQL：写表/读表抽取（读表剔除自身写表）。
func parseSQLScript(sf *scriptFile, text string) {
	for _, m := range reSQLWrite.FindAllStringSubmatch(text, -1) {
		sf.writesTbl = appendUnique(sf.writesTbl, strings.ToLower(m[1]))
	}
	for _, m := range reSQLRead.FindAllStringSubmatch(text, -1) {
		t := strings.ToLower(m[1])
		if !containsStr(sf.writesTbl, t) {
			sf.readsTbl = appendUnique(sf.readsTbl, t)
		}
	}
}
