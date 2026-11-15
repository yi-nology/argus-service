// html_template.go —— HTML 文档骨架与 tpl* 子模板：文档头/页头/工具栏/视口/数据块/图例。

package impactview

import (
	"fmt"
	"html"
	"strings"
)

// impactLegend/impactLegendEdges 影响面页图例（v3.183.0 原文，v6.1.4 参数化
// 以支持地图页分语境）。
func impactLegend() string {
	return `<span class="lgd lgd-chg"></span>改动文件/符号（红=直接改动，黄=改动函数有调用方）<span class="lgd lgd-pkg"></span>包<span class="lgd lgd-tst"></span>测试<span class="lgd lgd-tbl"></span>SQL 表<span class="lgd lgd-file"></span>受影响文件（颜色越红=关联越多=风险越高）`
}

func impactLegendEdges() string {
	return `import=波及依赖包 · caller=波及调用方 · source=波及装载方 · lineage=表血缘 · test_of=需跑测试 · coupled=历史共变`
}

// repoLegend/repoLegendEdges 仓库地图页图例（v6.1.4）：无「改动/波及」语境，
// 文件节点=仓库自有文件（Detail=导出符号清单）。
func repoLegend() string {
	return `<span class="lgd lgd-pkg"></span>包（目录）<span class="lgd lgd-file"></span>文件（Detail=导出符号）`
}

func repoLegendEdges() string {
	return `import=跨包依赖 · contains=包含（目录→文件，仅小仓展开） · source=脚本装载 · lineage=表血缘`
}

// htmlTemplate 单文件 HTML（内联样式与脚本，零外链）。title/headLabel 支撑
// 影响面与仓库地图双页头（v5.6.2）。
func htmlTemplate(title, headLabel, riskBadgeStr string, s *Snapshot, taskID, dataJSON, svg string, w, h int, findingsJSON, cmpSummary, historyJSON, legendNodes, legendEdges, foldedSymsJSON string) string {
	var notes strings.Builder
	for _, n := range s.Notes {
		notes.WriteString("<li>" + html.EscapeString(n) + "</li>")
	}
	// v5.8.0 findings 数据块(空则整块省略)
	fScript := ""
	if findingsJSON != "" {
		fScript = `<script type="application/json" id="impact-findings">` + findingsJSON + `</script>`
	}
	hScript := ""
	if historyJSON != "" {
		hScript = `<script type="application/json" id="impact-history">` + historyJSON + `</script>`
	}
	fsScript := ""
	if foldedSymsJSON != "" {
		fsScript = `<script type="application/json" id="impact-folded-syms">` + foldedSymsJSON + `</script>`
	}
	var riskList strings.Builder
	for _, r := range s.Risk.Reasons {
		riskList.WriteString("<li>" + html.EscapeString(r) + "</li>")
	}
	// 拆分（批 D 波1）：按模板逻辑区块切 tpl* 子模板逐段拼接，输出与原单体
	// 模板逐字节等价；参数过多的区块各自收拢为私有函数入参。
	return tplDocHead(title, riskBadgeStr) +
		tplPageHeader(title, riskBadgeStr, headLabel, s) +
		tplToolbar() +
		tplDetails(cmpSummary, riskList.String(), legendNodes, legendEdges, notes.String(), s) +
		tplViewport(svg, w, h) +
		tplDataJSON(dataJSON) +
		fScript + hScript + fsScript +
		tplScriptBoot() +
		tplScriptChips() +
		tplScriptFilter() +
		tplScriptPanel() +
		tplScriptPanelFile() +
		tplScriptMisc() +
		tplScriptExpand() +
		tplScriptMinimap()
}

// tplDocHead 文档头：DOCTYPE + head + 内联样式（title/风险徽标插值）。
func tplDocHead(title, riskBadgeStr string) string {
	return `<!DOCTYPE html>
<html lang="zh"><head><meta charset="utf-8">
<title>` + title + ` · 风险 ` + riskBadgeStr + `</title>
<style>
body{font:14px/1.6 -apple-system,"PingFang SC","Microsoft YaHei",sans-serif;margin:0;color:#0f172a;background:#f8fafc}
header{padding:12px 20px;background:#fff;border-bottom:1px solid #e2e8f0;display:flex;gap:16px;align-items:center;flex-wrap:wrap}
header h1{font-size:16px;margin:0}
.badge{padding:2px 10px;border-radius:999px;font-weight:600}
.badge.high{background:#fecaca;color:#7f1d1d}.badge.medium{background:#fef3c7;color:#78350f}.badge.low{background:#dcfce7;color:#14532d}
.meta{color:#64748b;font-size:12px}
#toolbar{padding:8px 20px;background:#fff;border-bottom:1px solid #e2e8f0;display:flex;gap:8px;align-items:center}
#toolbar input{flex:0 1 280px;padding:4px 10px;border:1px solid #cbd5e1;border-radius:6px}
button{padding:4px 12px;border:1px solid #cbd5e1;background:#fff;border-radius:6px;cursor:pointer}
button:hover{background:#f1f5f9}
#viewport{overflow:hidden;height:calc(100vh - 118px);cursor:grab;position:relative;background:#f8fafc}
#viewport.dragging{cursor:grabbing}
svg{min-width:100%}
.node rect{stroke-width:1.5}
.node{cursor:pointer}
.node text{font-size:12px;fill:#0f172a;pointer-events:none}
.node.dim{opacity:.15}
.edge{fill:none;stroke:#94a3b8;stroke-width:1.2;marker-end:url(#arrow)}
.edge.dim{opacity:.12}
.edgeg text.elabel{opacity:0;transition:opacity .15s;pointer-events:none}
.edgeg:hover text.elabel{opacity:1}
.edgeg:hover path.edge{stroke:#475569;stroke-width:2}
.elabel{font-size:10px;fill:#94a3b8}
/* v6.10.5 边类型过滤：边按 rel 属性打标，JS 按选中类型显隐 */
.edge.hidden{display:none}
aside{position:fixed;right:0;top:0;bottom:0;width:320px;background:#fff;border-left:1px solid #e2e8f0;padding:16px;display:none;overflow:auto}
aside h3{margin:0 0 8px;font-size:14px}
aside .kv{margin:2px 0;font-size:12px;color:#334155;word-break:break-all}
aside .kv b{color:#64748b;font-weight:500;margin-right:6px}
details{margin:8px 20px}
summary{cursor:pointer;color:#334155}
#kinds{display:inline-flex;gap:4px;margin:0 6px}
.kchip{padding:2px 10px;border:1px solid #cbd5e1;border-radius:999px;background:#fff;font-size:12px;cursor:pointer;user-select:none}
.kchip.on{background:#1e293b;color:#fff;border-color:#1e293b}
/* v6.10.5 边类型过滤 chips */
#edgekinds{display:inline-flex;gap:4px;margin:0 6px}
.ekchip{padding:2px 8px;border:1px solid #cbd5e1;border-radius:999px;background:#fff;font-size:11px;cursor:pointer;user-select:none}
.ekchip.on{background:#8b5cf6;color:#fff;border-color:#8b5cf6}
.node.focus rect{stroke:#2563eb;stroke-width:2.5}
.lgd{display:inline-block;width:10px;height:10px;border-radius:2px;margin:0 4px 0 8px;vertical-align:-1px}
.lgd-chg{background:#fecaca;border:1px solid #dc2626}.lgd-sym{background:#fef9c3;border:1px solid #ca8a04}.lgd-pkg{background:#dbeafe;border:1px solid #2563eb}.lgd-tst{background:#dcfce7;border:1px solid #16a34a}.lgd-tbl{background:#ede9fe;border:1px solid #7c3aed}.lgd-file{background:#e2e8f0;border:1px solid #64748b}
.cplrow{cursor:pointer;padding:2px 0;font-size:12px;color:#334155}
.cplrow:hover{color:#2563eb;text-decoration:underline}
/* v6.10.4 全屏模式：body.fullscreen 隐藏 header/toolbar，viewport 占满全屏 */
body.fullscreen header,body.fullscreen #toolbar{display:none}
body.fullscreen #viewport{height:100vh}
/* v6.9.4 暗色模式：prefers-color-scheme 媒体查询自动切换 */
@media(prefers-color-scheme:dark){
 body{color:#e2e8f0;background:#0f172a}
 header{background:#1e293b;border-color:#334155}
 #toolbar{background:#1e293b;border-color:#334155}
 #viewport{background:#0f172a}
 button{background:#1e293b;color:#e2e8f0;border-color:#334155}
 button:hover{background:#334155}
 #toolbar input{background:#0f172a;color:#e2e8f0;border-color:#334155}
 aside{background:#1e293b;border-color:#334155}
 aside .kv{color:#cbd5e1}
 aside .kv b{color:#94a3b8}
 summary{color:#94a3b8}
 details{color:#cbd5e1}
 .kchip{background:#1e293b;color:#e2e8f0;border-color:#334155}
 .kchip.on{background:#3b82f6;color:#fff;border-color:#3b82f6}
 .cplrow{color:#cbd5e1}
 .cplrow:hover{color:#60a5fa}
 .node text{fill:#e2e8f0}
 .edge{stroke:#64748b}
 .edgeg:hover path.edge{stroke:#94a3b8}
 .elabel{fill:#64748b}
 .meta{color:#94a3b8}
 .badge.high{background:#7f1d1d;color:#fecaca}
 .badge.medium{background:#78350f;color:#fef3c7}
 .badge.low{background:#14532d;color:#dcfce7}
}
/* v6.9.9 方向 Z：打印样式——隐藏交互元素，调整颜色供导出 PDF */
@media print{
 #toolbar,aside,#closepanel{display:none!important}
 header{border-bottom:none}
 #viewport{height:auto;overflow:visible}
 svg{transform:none!important;max-width:100%;height:auto}
 .node.dim{opacity:.3}
 details{break-inside:avoid}
}
/* v6.10.5 边类型过滤：隐藏非选中关系的边 */
.edge.hidden{display:none}
/* v6.11.0 小地图：右下角缩略图 + 视口指示器 */
#minimap{position:fixed;right:16px;bottom:16px;width:180px;height:120px;border:1px solid #cbd5e1;border-radius:6px;background:rgba(255,255,255,.9);z-index:10;overflow:hidden}
#minimap svg{width:100%;height:100%}
#minimap rect.vp{fill:rgba(59,130,246,.15);stroke:#3b82f6;stroke-width:1}
/* v6.11.0 节点拖拽中：禁用视口拖拽 */
#viewport.nodedrag{cursor:move}
</style></head><body>
`
}

// tplPageHeader 页头：标题、风险徽标、统计与页标识。
func tplPageHeader(title, riskBadgeStr, headLabel string, s *Snapshot) string {
	return `<header>
<h1>` + title + `</h1>
<span class="badge ` + s.Risk.Level + `">风险 ` + riskBadgeStr + ` · ` + fmt.Sprint(s.Risk.Score) + ` 分</span>
<span class="meta">` + html.EscapeString(fmt.Sprintf("%d 改动 · %d 节点 · %d 边 · 调用位 %d · 共变 %d", len(s.Changed), len(s.Nodes), len(s.Edges), s.Stats.CallSites, len(s.Coupling))) + `</span>
<span class="meta">` + headLabel + `</span>
</header>
`
}

// tplToolbar 工具栏：搜索框与缩放/导出/分享按钮。
func tplToolbar() string {
	return `<div id="toolbar">
<input id="q" placeholder="搜索文件 / 包 / 符号…">
<span id="kinds"></span>
<span id="edgekinds"></span>
<button id="zin">放大</button><button id="zout">缩小</button><button id="fit">复位</button>
<button id="export">导出 SVG</button>
<button id="exportpng" title="导出 PNG 图片">导出 PNG</button>
<button id="shareview" title="复制当前视图状态链接">分享视图</button>
<button id="fullscreen" title="全屏模式">全屏</button>
<button id="closepanel" style="display:none">收起详情</button>
</div>
`
}

// tplDetails 折叠详情区：演化对比摘要、风险依据、图例、共变、函数级 diff、降级注明。
func tplDetails(cmpSummary, riskList, legendNodes, legendEdges, notes string, s *Snapshot) string {
	return cmpSummary + `<details><summary>风险依据（` + fmt.Sprint(len(s.Risk.Reasons)) + ` 条）</summary><ul>` + riskList + `</ul></details>
<details><summary>🗺 图例与交互</summary><div style="font-size:12px;color:#334155;line-height:2">
节点：` + legendNodes + `<br>
边：` + legendEdges + `<br>
徽标：文件节点右上角红点数字=该文件审查意见数（点节点看意见摘要+历史档案）。
历史：点选文件节点的「历史档案」= 跨 MR 意见史与人工抑制计数（误报倾向参考）。交互：点节点=聚焦一跳邻居+详情侧栏；搜索/类型 chips 过滤；悬停边看关系；「导出 SVG」离线存档。改动符号函数级 diff 见「函数级图 diff」区块，卡片可点击联动聚焦。
</div></details>
` + couplingDetails(s) + `
` + funcDiffDetails(s) + `<details><summary>降级注明（` + fmt.Sprint(len(s.Notes)) + ` 条）</summary><ul>` + notes + `</ul></details>
`
}

// tplViewport 主视口：SVG 画布、小地图容器与详情侧栏占位。
func tplViewport(svg string, w, h int) string {
	return `<div id="viewport"><svg id="g" width="` + fmt.Sprint(w) + `" height="` + fmt.Sprint(h) + `" viewBox="0 0 ` + fmt.Sprint(w) + ` ` + fmt.Sprint(h) + `">
<defs><marker id="arrow" viewBox="0 0 10 10" refX="9" refY="5" markerWidth="7" markerHeight="7" orient="auto"><path d="M0,0L10,5L0,10z" fill="#94a3b8"/></marker></defs>
` + svg + `</svg></div>
<div id="minimap"><svg id="mm" viewBox="0 0 ` + fmt.Sprint(w) + ` ` + fmt.Sprint(h) + `"></svg></div>
<aside id="panel"></aside>
`
}

// tplDataJSON 内嵌快照数据块（script 内闭合防线由调用方保证）。
func tplDataJSON(dataJSON string) string {
	return `<script type="application/json" id="impact-data">` + dataJSON + `</script>
`
}
