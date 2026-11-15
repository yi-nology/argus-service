// html.go —— 自包含交互 HTML 视图（v3.183.0 P1）：快照 → 单文件 HTML。
// 零 CDN 依赖（215 内网部署无外网也完整可用）：Go 侧做分层布局产 SVG，
// 内嵌 JSON + 少量原生 JS 供给缩放/拖拽/搜索/点选详情。经 GET /tasks/:id/impact
// 下发（text/html），也可离线存档。

package impactview

import (
	"encoding/json"
	"fmt"
	"html"
	"strconv"
	"strings"

	"github.com/yi-nology/argus/internal/domain"
	"github.com/yi-nology/argus/internal/storage"
)

// ViewOpts 渲染可选面（v5.8.1 收口：findings/演化对比,避免签名继续膨胀）。
type ViewOpts struct {
	Findings []domain.Finding
	Compare  *ImpactCompare // 同 MR 上一轮快照的集合差(nil=无对比面)
	// History 文件意见历史档案(v5.8.2 惯犯文件热力;key=file 相对路径)
	History map[string]storage.FindingFileHistory
	// RepoMode 仓库地图页模式(v6.1.4):图例措辞切换(「受影响文件/波及」是
	// 影响面语境;地图页节点=包/文件,边=依赖关系词)。
	RepoMode bool
}

// RenderHTMLFull 全量渲染面(v5.8.2):findings+演化对比+历史档案（taskID 仅作
// 页头标识可空；快照空节点返回空串）。任务视角唯一 HTML 入口——v6.2.x 架构
// 卫生批 B 销掉 RenderHTML/RenderHTMLWithFindings/RenderHTMLWithCompare
// 三个生产零消费平行壳（测试直走本入口，语义覆盖不变；历史先例 v5.4.2
// repoAllowed 死代码删、v3.9.2 Stop 教训）。
func RenderHTMLFull(s *Snapshot, taskID string, opts ViewOpts) string {
	return renderImpactHTML(s, taskID, "Argus 影响面视图", taskMeta(taskID), opts)
}

// RenderRepoHTML 仓库级代码地图页（v5.6.2）：同一渲染面，页头换标题与
// 仓库标识；空图返回空串。
func RenderRepoHTML(s *Snapshot, repoLabel string) string {
	return renderImpactHTML(s, "", "Argus 仓库代码地图", html.EscapeString(repoLabel), ViewOpts{RepoMode: true})
}

func renderImpactHTML(s *Snapshot, taskID, title, headLabel string, opts ViewOpts) string {
	if s == nil || len(s.Nodes) == 0 {
		return ""
	}
	// v5.7.4 函数级变更一等节点：视图副本上合成 FuncDiff 节点/归属边——
	// JSON（JS 联动数据源）与布局/绘制三处同源一致；mermaid/报告面用原
	// snapshot,不受影响（评论图已有独立 FuncDiffMermaid 块,不重复膨胀）。
	vs := withFuncDiffNodes(s)
	data, _ := json.Marshal(vs)
	dataStr := strings.ReplaceAll(string(data), "</", `<\/`) // script 内闭合防线
	vn, vi, ve, foldedSyms := viewGraph(vs)
	pos, w, h := layoutSVG(vn, ve)
	legendNodes, legendEdges := impactLegend(), impactLegendEdges()
	if opts.RepoMode {
		legendNodes, legendEdges = repoLegend(), repoLegendEdges()
	}
	// v6.10.6 方向 AQ：演化对比高亮——AddedNodes 绿框 / RemovedNodes 灰虚框。
	// 数据来自 opts.Compare（CompareSnapshots 集合差），图上直接可见。
	addedSet := map[string]bool{}
	removedSet := map[string]bool{}
	if opts.Compare != nil {
		for _, n := range opts.Compare.AddedNodes {
			addedSet[n.ID] = true
		}
		for _, n := range opts.Compare.RemovedNodes {
			removedSet[n.ID] = true
		}
	}
	return htmlTemplate(title, headLabel, html.EscapeString(riskBadge(s.Risk.Level)), s, taskID,
		string(dataStr), svgBody(pos, vn, vi, ve, findingsByFile(opts.Findings), addedSet, removedSet), w, h,
		findingsJSON(opts.Findings), compareSummary(opts.Compare), historyJSON(opts.History),
		legendNodes, legendEdges, foldedSymsJSON(foldedSyms))
}

// maxFuncDiffNodes 主图合成的函数级变更节点上限（防大 diff 刷屏；完整清单
// 仍在侧栏与 fdcard 区块）。
const maxFuncDiffNodes = 8

// withFuncDiffNodes 视图层合成：每个 FuncDiff（file 在改动节点集内）生成
// funcdiff 节点 + file→fd 归属边（新增/删除/变更状态进 Detail 前缀,配色
// 与侧栏徽标同词表）。返回副本,不改入参（mermaid/报告面共用原 snapshot）。
func withFuncDiffNodes(s *Snapshot) *Snapshot {
	if s == nil || len(s.FuncDiffs) == 0 {
		return s
	}
	changed := map[string]bool{}
	for _, n := range s.Nodes {
		if n.Changed && n.Kind == KindFile {
			changed[n.ID] = true
		}
	}
	vs := *s
	added := 0
	for _, fd := range s.FuncDiffs {
		if added >= maxFuncDiffNodes {
			break
		}
		fid := "file:" + fd.File
		if !changed[fid] {
			continue // 所在文件不在改动集（截断/非直接改动）,不合成
		}
		detail := fd.Status
		if fd.SigSame {
			detail += "|sig_same"
		}
		vs.Nodes = append(vs.Nodes, Node{
			ID: "fd:" + fd.File + ":" + fd.Name, Kind: KindFuncDiff,
			Label: fd.Name + "()", Detail: detail,
		})
		vs.Edges = append(vs.Edges, Edge{From: fid, To: "fd:" + fd.File + ":" + fd.Name, Rel: RelFuncOf})
		added++
	}
	if added == 0 {
		return s
	}
	return &vs
}

// taskMeta 页头任务标识（空省略）。
func taskMeta(taskID string) string {
	if taskID == "" {
		return ""
	}
	return "任务 " + html.EscapeString(taskID)
}

// findingsByFile 文件→意见数(徽标渲染输入)。
func findingsByFile(findings []domain.Finding) map[string]int {
	m := map[string]int{}
	for _, f := range findings {
		if f.File != "" {
			m["file:"+f.File]++
		}
	}
	return m
}

// findingsJSON 渲染面瘦身子集(file/line/severity/fp/comment 摘要/by)。
func findingsJSON(findings []domain.Finding) string {
	if len(findings) == 0 {
		return ""
	}
	type slim struct {
		File     string `json:"file"`
		Line     int    `json:"line"`
		Severity string `json:"severity"`
		FP       string `json:"fp,omitempty"`
		By       string `json:"by,omitempty"`
		Comment  string `json:"comment"`
	}
	out := make([]slim, 0, len(findings))
	for _, f := range findings {
		c := []rune(f.Comment)
		if len(c) > 160 {
			c = append(c[:160], []rune("…")...)
		}
		out = append(out, slim{File: f.File, Line: f.Line, Severity: f.Severity,
			FP: f.FP, By: f.By, Comment: string(c)})
	}
	b, _ := json.Marshal(out)
	return strings.ReplaceAll(string(b), "</", `<\/`)
}

// historyJSON 文件意见历史档案的瘦身影射(file→total/suppressed,仅保留 total≥3 的惯犯)。
func historyJSON(h map[string]storage.FindingFileHistory) string {
	if len(h) == 0 {
		return ""
	}
	out := map[string]storage.FindingFileHistory{}
	for f, v := range h {
		if v.Total >= 3 {
			out[f] = v
		}
	}
	if len(out) == 0 {
		return ""
	}
	b, _ := json.Marshal(out)
	return strings.ReplaceAll(string(b), "</", `<\/`)
}

// couplingDetails 历史共变面板（v5.7.3）：Top 共变对可点击联动聚焦主图——
// import 图外的隐性耦合（同 commit 反复共现）是评审最容易漏的关联面。
func couplingDetails(s *Snapshot) string {
	if s == nil || len(s.Coupling) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString(`<details><summary>🔗 历史共变（` + fmt.Sprint(len(s.Coupling)) + ` 对，点击聚焦图中节点）</summary>`)
	for _, c := range s.Coupling {
		if c.Count < MinCoupleCount {
			continue
		}
		b.WriteString(`<div class="cplrow" onclick="window.focusFileNode &amp;&amp; window.focusFileNode(` +
			strconv.Quote("file:"+c.File) + `)">` +
			html.EscapeString(shortLabel(c.File)) + ` ↔ ` + html.EscapeString(shortLabel(c.Other)) +
			` <b>×` + fmt.Sprint(c.Count) + `</b>（同 commit 共现）</div>`)
	}
	b.WriteString(`</details>`)
	return b.String()
}

// funcDiffDetails 函数级图 diff 区块（before/after 双列卡片 + 调用边增减
// chips；无数据整块省略）。状态徽标：🟢 新增 / 🔴 删除 / 🟡 变更。
func funcDiffDetails(s *Snapshot) string {
	if s == nil || len(s.FuncDiffs) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString(`<details open><summary>函数级图 diff（` + fmt.Sprint(len(s.FuncDiffs)) + ` · base 对比，🟢 新增 / 🔴 删除 / 🟡 变更）</summary>`)
	b.WriteString(`<style>
.fdcard{border:1px solid #e2e8f0;border-radius:8px;margin:8px 0;padding:10px;background:#fff}
.fdhead{display:flex;gap:8px;align-items:center;font-size:13px}
.fdst{padding:1px 8px;border-radius:999px;font-size:12px;font-weight:600}
.st-add{background:#dcfce7;color:#14532d}.st-rem{background:#fecaca;color:#7f1d1d}.st-mod{background:#fef9c3;color:#713f12}
.fdcols{display:flex;gap:8px;margin-top:8px}
.fdcol{flex:1;min-width:0}
.fdcol h4{margin:0 0 4px;font-size:11px;color:#64748b;font-weight:600}
.fdcol pre{background:#f1f5f9;padding:6px 8px;border-radius:6px;overflow:auto;font-size:12px;margin:0;white-space:pre-wrap;word-break:break-all}
.fdcalls{margin-top:6px;font-size:12px}
.chip{display:inline-block;padding:1px 8px;border-radius:999px;margin:2px 4px 0 0;font-size:12px}
.chip-add{background:#dcfce7;color:#14532d}.chip-rem{background:#fecaca;color:#7f1d1d}
</style><div>`)
	shown := s.FuncDiffs
	if len(shown) > 12 {
		shown = shown[:12]
	}
	for _, fd := range shown {
		var st, stCls string
		switch fd.Status {
		case FuncAdded:
			st, stCls = "新增", "st-add"
		case FuncRemoved:
			st, stCls = "删除", "st-rem"
		default:
			st, stCls = "变更", "st-mod"
		}
		b.WriteString(`<div class="fdcard" data-fdfile="` + html.EscapeString(fd.File) + `" style="cursor:pointer" title="点击在图中聚焦该文件"><div class="fdhead"><span class="fdst ` + stCls + `">` + st + `</span><b>` + html.EscapeString(fd.Name) + `</b><span style="color:#64748b;font-size:12px">` + html.EscapeString(fd.File) + `</span></div>`)
		before, after := "", ""
		if fd.Before != nil {
			before = fd.Before.Signature
		}
		if fd.After != nil {
			after = fd.After.Signature
		}
		if fd.Status != FuncAdded && before != "" {
			b.WriteString(`<div class="fdcols"><div class="fdcol"><h4>BEFORE</h4><pre>` + html.EscapeString(before) + `</pre></div>`)
		} else {
			b.WriteString(`<div class="fdcols"><div class="fdcol"><h4>BEFORE</h4><pre>（无）</pre></div>`)
		}
		if fd.Status != FuncRemoved && after != "" {
			b.WriteString(`<div class="fdcol"><h4>AFTER</h4><pre>` + html.EscapeString(after) + `</pre></div></div>`)
		} else {
			b.WriteString(`<div class="fdcol"><h4>AFTER</h4><pre>（已删除）</pre></div></div>`)
		}
		if len(fd.CallsAdded) > 0 || len(fd.CallsRemoved) > 0 {
			b.WriteString(`<div class="fdcalls">`)
			for _, c := range capShown(fd.CallsAdded, 6) {
				b.WriteString(`<span class="chip chip-add">+ ` + html.EscapeString(c) + `</span>`)
			}
			for _, c := range capShown(fd.CallsRemoved, 6) {
				b.WriteString(`<span class="chip chip-rem">− ` + html.EscapeString(c) + `</span>`)
			}
			b.WriteString(`</div>`)
		}
		b.WriteString(`</div>`)
	}
	if rest := len(s.FuncDiffs) - len(shown); rest > 0 {
		b.WriteString(`<div style="color:#64748b;font-size:12px">…其余 ` + fmt.Sprint(rest) + ` 条略（完整见 MCP get_impact / JSON 快照）</div>`)
	}
	b.WriteString(`</div></details>`)
	return b.String()
}
