// build.go —— 影响面管线编排：L0 文件 → L1 import 反向闭包 → L2 符号调用方
// → L3 测试面 → P2 共变，逐层独立降级（失败记 Notes 继续上层），输出确定化
// 快照。Build 是唯一入口，runner 接线只做类型搬运。

package impactview

import (
	"context"
	"time"

	"github.com/oagentkit/textx/codeintel"
	"github.com/yi-nology/argus/internal/gitdeepen"
	"github.com/yi-nology/argus/internal/semsearch"
)

// Build 输入（通用原语，不认识 plugin 类型；接线在 runner）。
type Input struct {
	Dir          string              // 工作副本根（空 = 无工作区，仅 L0 降级面）
	ChangedFiles []string            // 纳入审查的文件（仓库相对路径，/ 分隔）
	HunkRanges   map[string][][2]int // 文件 → 改动行区间（L2 触达符号依据）
	DiffText     string              // 清洗后 diff 文本（签名变更近似判定）
	Index        *codeintel.Index    // 工作副本符号索引（nil = 跳过 L2；runner 复用 codegraph 已建索引）
	Graph        *semsearch.Index    // 消歧符号图（v6.0.0，nil = 走既有 codeintel 词法路径；runner 复用 codegraph 已建图）
	MaxCommits   int                 // P2 共变扫描深度（0 = DefaultCommits；负 = 关闭）
	BaseRef      string              // 函数级 diff 的 base 参照（如 origin/main；空 = 跳过该层）
	InsecureTLS  bool                // funcdiff deepen fetch 的 TLS 跳过校验（与平台克隆同环境）
	Deepen       gitdeepen.Auth      // funcdiff deepen fetch 的凭证与期望 clone 源（零值 = 无鉴权面）
	Timeout      time.Duration       // 构建总预算（0 = 30s；超时逐层降级不阻断）
}

// Build 构建影响面快照（永不返回 error——影响面是增强产物，失败面由 Notes
// 与降级快照表达）。
func Build(ctx context.Context, in Input) *Snapshot {
	start := time.Now()
	if in.Timeout <= 0 {
		in.Timeout = 30 * time.Second
	}
	deadline := start.Add(in.Timeout)
	s := &Snapshot{
		Version:     SnapshotVersion,
		GeneratedAt: start.UTC(),
		Changed:     dedupSorted(in.ChangedFiles),
		Notes:       []string{},
	}
	if len(s.Changed) == 0 {
		s.Notes = append(s.Notes, "无纳入审查的变更文件，影响面为空")
		return s
	}

	// ---- L0 文件层：变更节点（恒可用） ----
	for _, f := range s.Changed {
		s.Nodes = append(s.Nodes, Node{
			ID: "file:" + f, Kind: KindFile, Label: shortLabel(f), Changed: true, Detail: f,
		})
	}

	// ---- L1 包层 + L3 测试面：工作副本 import 图 ----
	ws := scanWorkspaceLayer(s, in)
	affected := buildAffectedPackages(s, ws, s.Changed)

	// ---- L2 符号层：触达符号 → 精化调用位（v6.0.0 图优先、逐符号回退） ----
	syms := collectSymbols(in.Index, in.Graph, in.HunkRanges)
	s.Stats.Symbols = len(syms)
	callTotal := appendSymbolNodes(s, syms)
	noteCallerSource(s, syms, in.Graph)

	// ---- P2 共变：git log 历史共现（预算内才做） ----
	buildCouplingLayer(ctx, s, in, deadline)

	// ---- v3.185.0 函数/方法级图 diff：base 切面对比（预算内才做） ----
	buildFuncDiffLayers(ctx, s, in, deadline)

	// ---- v5.6.0 多语言图层：非 Go 仓词法图（source/函数调用/表血缘；预算内） ----
	// Go 仓不走此层：import 闭包+AST 是更强的真相源，词法图只会添误报。
	scriptAffected, scriptCalls := 0, 0
	if in.Dir != "" && (ws == nil || len(ws.modules) == 0) && ctx.Err() == nil && time.Now().Before(deadline) {
		scriptAffected, scriptCalls = buildScriptLayer(ctx, in.Dir, s, deadline)
	}

	// ---- 风险分（确定性） ----
	scoreBuildRisk(s, in, affected, syms, callTotal, scriptAffected, scriptCalls)

	finalize(s)
	s.Stats.TookMS = time.Since(start).Milliseconds()
	return s
}

// scanWorkspaceLayer L1/L3 输入面：扫描工作副本 import 图并回填文件/包统计
// 与降级注记（无工作副本时只记降级注，ws 为 nil）。
func scanWorkspaceLayer(s *Snapshot, in Input) *goMods {
	var ws *goMods
	if in.Dir != "" {
		var trunc bool
		ws, trunc = scanWorkspace(in.Dir)
		for _, n := range ws.fileCount {
			s.Stats.GoFiles += n
		}
		for _, n := range ws.testFiles {
			s.Stats.GoFiles += n
		}
		s.Stats.Packages = len(ws.dirs)
		if len(ws.modules) == 0 {
			s.Notes = append(s.Notes, "未识别 go.mod（非 Go 仓库），包级影响面降级为文件层")
		}
		if trunc {
			s.Stats.Truncated = true
			s.Notes = append(s.Notes, "工作副本 Go 文件超扫描上限，import 图可能不完整")
		}
	} else {
		s.Notes = append(s.Notes, "无工作副本，包级/符号级影响面降级为文件层")
	}
	return ws
}

// buildCouplingLayer P2 历史共变层：预算内跑 git log 共现扫描并挂共变节点/边。
func buildCouplingLayer(ctx context.Context, s *Snapshot, in Input, deadline time.Time) {
	commits := in.MaxCommits
	if commits == 0 {
		commits = DefaultCommits
	}
	if commits > 0 && in.Dir != "" && ctx.Err() == nil && time.Now().Before(deadline) {
		couples, n, errNote := collectCoupling(ctx, in.Dir, s.Changed, minInt(commits, MaxCommits), in.Timeout)
		if errNote != "" {
			s.Notes = append(s.Notes, errNote)
		}
		s.Coupling = couples
		s.Stats.Commits = n
		appendCouplingNodes(s, couples)
	} else if commits > 0 && in.Dir == "" {
		s.Notes = append(s.Notes, "无工作副本，历史共变扫描跳过")
	}
}

// buildFuncDiffLayers 函数/方法级图 diff 层：Go 版在前、脚本版在后（共用
// FuncDiffs 出口，稳定序），预算内才做；v5.6.4 起 shell/py 走词法版。
func buildFuncDiffLayers(ctx context.Context, s *Snapshot, in Input, deadline time.Time) {
	if in.BaseRef != "" && in.Dir != "" && hasGoChanged(s.Changed) && ctx.Err() == nil && time.Now().Before(deadline) {
		fds, fnNotes := diffFunctions(ctx, in.Dir, in.BaseRef, s.Changed, in.HunkRanges, in.InsecureTLS, in.Deepen)
		s.FuncDiffs = fds
		s.Notes = append(s.Notes, fnNotes...)
		s.Stats.FuncDiffs = len(fds)
	} else if in.BaseRef == "" && in.Dir != "" && hasGoChanged(s.Changed) {
		s.Notes = append(s.Notes, "未提供 base 参照，函数级 diff 跳过")
	}
	if in.BaseRef != "" && in.Dir != "" && hasScriptChanged(s.Changed) && ctx.Err() == nil && time.Now().Before(deadline) {
		sfds, snNotes := diffScriptFunctions(ctx, in.Dir, in.BaseRef, s.Changed, in.HunkRanges)
		s.FuncDiffs = append(s.FuncDiffs, sfds...)
		s.Notes = append(s.Notes, snNotes...)
		s.Stats.FuncDiffs = len(s.FuncDiffs)
	}
}

// scoreBuildRisk 风险分（确定性）：汇总各层产出 → riskInput → scoreRisk，
// 并回填 CallSites 统计。
func scoreBuildRisk(s *Snapshot, in Input, affected []PkgImpact, syms []symHits, callTotal, scriptAffected, scriptCalls int) {
	exported := 0
	for _, h := range syms {
		if h.Exported {
			exported++
		}
	}
	strongCoupling := 0
	for _, c := range s.Coupling {
		if c.Count >= MinCoupleCount {
			strongCoupling++
		}
	}
	s.Stats.CallSites = callTotal + scriptCalls
	s.Risk = scoreRisk(riskInput{
		AffectedPackages:  countForeignPackages(affected, s.Changed),
		CallSites:         callTotal + scriptCalls,
		ScriptAffected:    scriptAffected,
		ExportedSymbols:   exported,
		SignatureChanged:  detectSignatureChange(in.DiffText),
		TestsMissing:      testsMissing(affected),
		EntrypointTouched: touchesEntrypoint(s.Changed, syms),
		StrongCoupling:    strongCoupling,
		DepsChanged:       depsChanged(s.Changed),
		TestsOnlyChanged:  testsOnlyChanged(s.Changed),
	})
}
