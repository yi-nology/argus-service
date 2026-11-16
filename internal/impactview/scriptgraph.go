// scriptgraph.go —— 非 Go 仓的词法影响图（v5.6.0 多语言图谱引擎首批）。
//
// Go 侧有 go.mod import 闭包与 AST 符号面；shell/SQL 仓过去只剩文件层孤岛
// （deploy-sql 实弹：mermaid 里 4 个红盒之间零条边）。本层用与 codeintel 同
// 纪律的词法启发补三类真边：
//   - source：shell 的 `source x.sh` / `. x.sh` 装载关系（含沿装载链传递）；
//   - caller：shell 函数定义 → 他文件调用位（复用 L2 的 RelCaller 词表）；
//   - lineage：SQL 表写入（CREATE/INSERT/UPDATE/DELETE/ALTER）→ 读方（FROM/JOIN），
//     经 table 枢纽节点两跳表达（改动文件→表→消费方）。
//
// v5.6.3 起扩 py/js/ts import 边、v5.6.5 起扩 rust mod/use、jvm import、
// c-family include（函数级只做 shell/py/rust，js/jvm 刻意不做——误报面失控，
// 见各 case 注释）。参与扩展名单源 langcap.GraphScanExts（v6.6.0）。
//
// 刻意不引 AST/新依赖：误报由封顶+Notes 表达，价值在"让受影响面可见"而非完备。

package impactview

import (
	"context"
	"regexp"
	"strconv"
	"time"
)

// 关系词表扩展与节点种类（与 types.go 同一契约面，只加不改）。
const (
	RelSource   = "source"   // shell 装载：改动 → 装载它的文件（含传递）
	RelLineage  = "lineage"  // SQL 表血缘：改动文件→表（写）、表→消费方（读）
	RelContains = "contains" // v6.1.4 包含边：目录（包）→ 其内的 Go 文件（仅小仓文件级展开）
	KindTable   = "table"    // SQL 表枢纽节点
	// v5.7.4 函数级变更一等节点（仅 HTML 渲染层合成,快照与 mermaid 不含）：
	RelFuncOf    = "func_of"  // 归属边：改动文件 → 其函数级变更节点
	KindFuncDiff = "funcdiff" // 函数级变更节点（Detail 前缀=状态词表）
)

// 封顶：非 Go 仓常是巨型脚本仓，扫描与快照两头都要闸。
const (
	MaxScriptFiles     = 3000   // 词法扫描文件上限
	maxScriptReadBytes = 256000 // 单文件读取上限
	MaxScriptTargets   = 400    // 受影响面入快照上限
	MaxDefsPerFile     = 80     // 单 shell 文件入图函数上限
)

type scriptFile struct {
	rel       string
	srcs      []string        // 解析成功的装载目标（shell source / py·js import，相对路径）
	defs      []string        // 定义的函数名（shell fn()/py def；JS 不做函数级）
	tokens    map[string]bool // 词元集（调用检测用）
	writesTbl []string        // SQL 写表（CREATE/INSERT/UPDATE/DELETE/ALTER）
	readsTbl  []string        // SQL 读表（FROM/JOIN，不含自身写表）
}

type scriptGraph struct {
	byRel     map[string]*scriptFile
	sourcedBy map[string][]string // target → 装载它的文件
	callersOf map[string][]string // func name → 调用它的文件（不含定义文件）
	readsBy   map[string][]string // table → 触碰（读/写）它的文件（不含写入方自身）
}

var (
	reShellSrc = regexp.MustCompile(`(?m)^[ \t]*(?:source|\.)[ \t]+([^\n#]+)`)
	reFuncDef  = regexp.MustCompile(`(?m)^[ \t]*(?:function[ \t]+)?([a-zA-Z_][a-zA-Z0-9_]*)[ \t]*\(\s*\)[ \t\r\n]*\{|^[ \t]*function[ \t]+([a-zA-Z_][a-zA-Z0-9_]*)[ \t\r\n]*\{`)
	rePyFunc   = regexp.MustCompile(`(?m)^[ \t]*def[ \t]+([a-zA-Z_][a-zA-Z0-9_]*)[ \t]*\(`)
	// import x.y[, z] / from x.y import …（from 的模块可为相对点前缀）
	rePyImport = regexp.MustCompile(`(?m)^[ \t]*(?:from[ \t]+(\.+[\w.]*|[\w.]+)[ \t]+import|import[ \t]+([\w.]+(?:[ \t]*,[ \t]*[\w.]+)*))`)
	// require("x") / import … from "x" / export … from "x"（CJS+ESM：from 在
	// 路径之后，与 Python 语法相反——首版写反实弹即漏）
	reJSImport = regexp.MustCompile(`(?:require\(\s*["']([^"']+)["']\s*\)|import[^;\n]*?from[ \t]+["']([^"']+)["']|export[^;\n]*?from[ \t]+["']([^"']+)["'])`)
	reToken    = regexp.MustCompile(`[a-zA-Z_][a-zA-Z0-9_]*`)
	// Rust：mod 声明（带分号=文件模块；inline `mod x {` 不产生文件不入图）/
	// use 路径（crate::/super::/self::，外部 crate 不入图）/ fn 定义
	reRustMod  = regexp.MustCompile(`(?m)^[ \t]*(?:pub(?:\([\w ]+\))?[ \t]+)?mod[ \t]+([a-zA-Z_]\w*)[ \t]*;`)
	reRustUse  = regexp.MustCompile(`(?m)^[ \t]*(?:pub(?:\([\w ]+\))?[ \t]+)?use[ \t]+([a-zA-Z_][\w]*::[\w:{}, ]*?)(?:[ \t]+as[ \t]+\w+)?[ \t]*;`)
	reRustFunc = regexp.MustCompile(`(?m)^[ \t]*(?:pub(?:\([\w ]+\))?[ \t]+)?(?:async[ \t]+)?fn[ \t]+([a-zA-Z_]\w*)`)
	// Java/Kotlin：import（通配 .* 不入图；静态导入同解析）
	reJavaImport = regexp.MustCompile(`(?m)^[ \t]*import[ \t]+(?:static[ \t]+)?([\w.]+)[ \t]*;`)
	// C/C++：#include "x.h"（双引号=本仓相对路径；<系统头> 不入图）
	reCInclude = regexp.MustCompile(`(?m)^[ \t]*#[ \t]*include[ \t]*"([^"]+)"`)
	// 引号/反引号/方括号起始的表名一律放过（字符类含 ` 不能进反引号原文，改双引号转义）
	reSQLWrite = regexp.MustCompile("(?i)\\b(?:CREATE\\s+TABLE(?:\\s+IF\\s+NOT\\s+EXISTS)?|CREATE\\s+(?:OR\\s+REPLACE\\s+)?VIEW|INSERT\\s+INTO|UPDATE|DELETE\\s+FROM|ALTER\\s+TABLE|MERGE\\s+INTO)\\s+[\"`\\[]?([a-zA-Z_][\\w$]*(?:\\.[a-zA-Z_][\\w$]*)?)")
	reSQLRead  = regexp.MustCompile("(?i)\\b(?:FROM|JOIN)\\s+[\"`\\[]?([a-zA-Z_][\\w$]*(?:\\.[a-zA-Z_][\\w$]*)?)")
)

// scriptLayerSink 词法层写入器：节点/边去重 + 波及/调用位/边数计数 + 截断
// 标记收拢（原函数内闭包局部变量的等价迁移，写入顺序不变）。
type scriptLayerSink struct {
	s         *Snapshot
	nodeSeen  map[string]bool
	edgeSeen  map[Edge]bool
	affected  int
	callSites int
	edges     int
	truncated bool
}

// newScriptLayerSink 以快照现有节点为去重基线构造写入器。
func newScriptLayerSink(s *Snapshot) *scriptLayerSink {
	k := &scriptLayerSink{s: s, nodeSeen: map[string]bool{}, edgeSeen: map[Edge]bool{}}
	for _, n := range s.Nodes {
		k.nodeSeen[n.ID] = true
	}
	return k
}

// addNode 幂等加节点（已有同 ID 节点跳过）。
func (k *scriptLayerSink) addNode(n Node) {
	if !k.nodeSeen[n.ID] {
		k.nodeSeen[n.ID] = true
		k.s.Nodes = append(k.s.Nodes, n)
	}
}

// addEdge 幂等加边；返回是否首次写入（计数依据）。
func (k *scriptLayerSink) addEdge(e Edge) bool {
	if k.edgeSeen[e] {
		return false
	}
	k.edgeSeen[e] = true
	k.s.Edges = append(k.s.Edges, e)
	return true
}

// buildScriptLayer 扫描并回填快照的脚本/血缘层；返回（波及文件边数, 调用位数）。
// 非 Go 仓（go.mod 缺失）时由 Build 调用；预算内执行，永不报错，无边即零副作用。
func buildScriptLayer(ctx context.Context, dir string, s *Snapshot, deadline time.Time) (int, int) {
	sg := scanScripts(dir, deadline)
	if sg == nil || len(sg.byRel) == 0 {
		return 0, 0
	}
	indexReverse(sg)

	k := newScriptLayerSink(s)
	for _, c := range s.Changed {
		if ctx.Err() != nil || time.Now().After(deadline) {
			k.truncated = true
			break
		}
		sf, ok := sg.byRel[c]
		if !ok {
			continue
		}
		applyScriptCallers(k, sg, c, sf)
		applySourceClosure(k, sg, c)
		applySQLLineage(k, sg, c, sf)
	}

	if k.edges == 0 {
		return 0, 0
	}
	note := "词法图补充：source/import 装载、函数调用、SQL 血缘边 " + strconv.Itoa(k.edges) + " 条"
	if k.truncated {
		s.Stats.Truncated = true
		note += "（超封顶已截断）"
	}
	s.Notes = append(s.Notes, note)
	return k.affected, k.callSites
}

// applyScriptCallers shell 函数 → 调用方（sym 枢纽，与 L2 同形态）。
func applyScriptCallers(k *scriptLayerSink, sg *scriptGraph, c string, sf *scriptFile) {
	for _, fn := range sf.defs {
		callers := sg.callersOf[fn]
		if len(callers) == 0 {
			continue
		}
		symID := "sym:" + c + ":" + fn
		k.addNode(Node{ID: symID, Kind: KindSymbol, Label: fn + "()", Changed: true, Detail: c})
		for i, g := range callers {
			if i >= MaxCallersPerSym {
				k.truncated = true
				break
			}
			k.addNode(Node{ID: "file:" + g, Kind: KindFile, Label: shortLabel(g), Detail: g})
			if k.addEdge(Edge{From: symID, To: "file:" + g, Rel: RelCaller}) {
				k.callSites++
				k.edges++
			}
		}
	}
}

// applySourceClosure source 反向闭包（装载我的文件，以及沿装载链继续上游）。
func applySourceClosure(k *scriptLayerSink, sg *scriptGraph, c string) {
	cFile := "file:" + c
	seen := map[string]bool{c: true}
	queue := []string{c}
	for depth := 0; depth <= MaxImportDepth && len(queue) > 0; depth++ {
		var next []string
		for _, t := range queue {
			for _, up := range sg.sourcedBy[t] {
				if seen[up] {
					continue
				}
				seen[up] = true
				if k.affected >= MaxScriptTargets {
					k.truncated = true
					continue
				}
				k.addNode(Node{ID: "file:" + up, Kind: KindFile, Label: shortLabel(up), Detail: up})
				if k.addEdge(Edge{From: cFile, To: "file:" + up, Rel: RelSource}) {
					k.affected++
					k.edges++
				}
				next = append(next, up)
			}
		}
		queue = next
	}
}

// applySQLLineage SQL 表血缘：写表 → table 枢纽 → 触碰方。
func applySQLLineage(k *scriptLayerSink, sg *scriptGraph, c string, sf *scriptFile) {
	cFile := "file:" + c
	for _, t := range sf.writesTbl {
		tid := "table:" + t
		k.addNode(Node{ID: tid, Kind: KindTable, Label: t, Detail: "table"})
		if k.addEdge(Edge{From: cFile, To: tid, Rel: RelLineage}) {
			k.edges++
		}
		for i, g := range sg.readsBy[t] {
			if i >= MaxCallersPerSym {
				k.truncated = true
				break
			}
			k.addNode(Node{ID: "file:" + g, Kind: KindFile, Label: shortLabel(g), Detail: g})
			if k.addEdge(Edge{From: tid, To: "file:" + g, Rel: RelLineage}) {
				k.affected++
				k.edges++
			}
		}
	}
}

// indexReverse 单遍建反向索引（避免 defs×files 的平方扫描）。
func indexReverse(sg *scriptGraph) {
	sg.sourcedBy = map[string][]string{}
	usersOf := map[string][]string{} // token(仅已知函数名) → 使用文件
	sg.readsBy = map[string][]string{}
	defNames := map[string]bool{}
	tblNames := map[string]bool{}
	for _, sf := range sg.byRel {
		for _, fn := range sf.defs {
			defNames[fn] = true
		}
		for _, t := range sf.writesTbl {
			tblNames[t] = true
		}
		for _, t := range sf.readsTbl {
			tblNames[t] = true
		}
	}
	for f, sf := range sg.byRel {
		for _, t := range sf.srcs {
			if _, ok := sg.byRel[t]; ok && t != f {
				sg.sourcedBy[t] = append(sg.sourcedBy[t], f)
			}
		}
		for tk := range sf.tokens {
			if defNames[tk] {
				usersOf[tk] = append(usersOf[tk], f)
			}
		}
		for _, t := range sf.writesTbl {
			sg.readsBy[t] = append(sg.readsBy[t], f)
		}
		for _, t := range sf.readsTbl {
			sg.readsBy[t] = append(sg.readsBy[t], f)
		}
	}
	sg.callersOf = map[string][]string{}
	for f, sf := range sg.byRel {
		for _, fn := range sf.defs {
			if len(fn) < 3 || fn == "main" {
				continue // 太短的名字误报面失控；main 人人都有
			}
			for _, u := range usersOf[fn] {
				if u == f {
					continue
				}
				sg.callersOf[fn] = append(sg.callersOf[fn], u)
			}
		}
	}
	// readsBy 剔除定义文件自身（写方读自己的表不是"消费方"）
	for t, fs := range sg.readsBy {
		keep := fs[:0]
		writers := map[string]bool{}
		for _, f := range fs {
			if containsStr(sg.byRel[f].writesTbl, t) {
				writers[f] = true
			}
		}
		for _, f := range fs {
			if !writers[f] {
				keep = append(keep, f)
			}
		}
		sg.readsBy[t] = keep
	}
}
