// merger_round.go —— 轮内归并：severity 归一化、三通道去重、acc 合并与
// 跨轮指纹入库。

package dispatch

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/oagentkit/textx/reportutil"

	"github.com/yi-nology/argus/internal/domain"
	"github.com/yi-nology/argus/internal/plugin"
)

// linesCompatible 指纹同但位置不相容（同文件两处不同行、且相差 >2）时，
// 轮内按指纹合并会把第二处问题吞掉——视为不同条目。任一方无行号（0）按相容处理。
func linesCompatible(a, b int) bool {
	if a <= 0 || b <= 0 {
		return true
	}
	return abs(a-b) <= 2
}

// mergeAcc 轮内归并 accumulator：同一问题多插件/多措辞命中时共享一个 acc。
type mergeAcc struct {
	finding domain.Finding
	fp      string // 该条目的指纹（linesCompatible 分裂后各行有独立 acc）
	bys     map[string]bool
	byOrder []string // 来源插件按合并发生序（首见者=评语作者，学习环记账键依赖此序）
}

// mergePending 待入库条目及其入库结果（跨轮历史/抑制判定在此回填）。
type mergePending struct {
	f   domain.Finding
	res domain.PrintUpsertResult
}

// mergeInto 把后到的同类发现并入既有条目：severity 冲突取高、行号 ±2 内采纳新行号、
// by 追加来源插件；意见文本不同且非互相包含时拼接（不同专家措辞各保留）。
func (a *mergeAcc) mergeInto(f domain.Finding) {
	a.finding.Severity = plugin.HigherSeverity(a.finding.Severity, f.Severity)
	if abs(a.finding.Line-f.Line) <= 2 && f.Line > 0 {
		a.finding.Line = f.Line
	}
	// best-of-N 采样复现计数随条目透传（多插件命中同题时保留首个非空，
	// 数据在结构化字段——入 Comment 会污染指纹/模式键，v3.10.0）
	if a.finding.Samples == "" {
		a.finding.Samples = f.Samples
	}
	if !a.bys[f.By] { // 首次出现的插件才进 byOrder（与 bys 去重语义一致）
		a.byOrder = append(a.byOrder, f.By)
	}
	a.bys[f.By] = true
	if !strings.Contains(a.finding.Comment, f.Comment) && !strings.Contains(f.Comment, a.finding.Comment) {
		a.finding.Comment += "\n\n（" + f.By + " 同位置补充）" + f.Comment
	}
}

// mergeInRound 轮内归并：三通道探测（指纹 / file+规范化评语 / file+line），
// 命中既有条目即并入，否则新建。返回按首见序排列的 acc 清单，同时把插件
// 失败/降级注记写进 out.Degraded。
func (m *Merger) mergeInRound(outcomes []Outcome, out *plugin.MergedFindings) []*mergeAcc {
	byFP := map[string]*mergeAcc{} // fp → 首个 acc（轮内指纹去重探测）
	byNC := map[string]*mergeAcc{} // "file|规范化comment" → 首个 acc（v3.7.0：fp v2 含行号后，
	//   同评语 ±2 行的容差合并由此通道承接——行号相容才并，保持 v1 容差语义）
	byLoc := map[string]*mergeAcc{} // "file|line" → 该位置首个 acc（跨措辞合并用）
	var order []*mergeAcc

	for _, o := range outcomes {
		if note := o.Notes(); note != "" {
			out.Degraded = append(out.Degraded, note)
		}
		if o.Result == nil {
			continue
		}
		// 插件自报的审查降级（如 builtin ReAct 失败落回单轮）透出为 Degraded——
		// 此前同一 "by xxx" 标签掩盖两种审查强度，报告消费者无法区分（v3.8.0）。
		for _, line := range strings.Split(o.Result.Notes, "\n") {
			if strings.Contains(line, plugin.DegradationMarker) {
				out.Degraded = append(out.Degraded, o.Name+"："+strings.TrimSpace(line))
			}
		}
		for _, f := range o.Result.Findings {
			sev, ok := plugin.NormalizeSeverity(f.Severity)
			if !ok {
				m.log.Warn("argus.merger.severity_unknown",
					"plugin", o.Name, "raw", f.Severity, "mapped", sev)
				// 词表外严重度整批落 low 是"外部插件词表失配"的信号——此前只有
				// 日志，报告零痕迹（v3.8.0 补 Degraded 注记）
				out.Degraded = append(out.Degraded,
					fmt.Sprintf("%s 报告了词表外严重度 %q（已按 %s 处理）", o.Name, f.Severity, sev))
			}
			f.Severity = sev
			f.By = o.Name
			plugin.ObsFindings(sev, o.Name, 1)

			// 规范指纹写定于此（原始 comment/行号，先于任何标注与 R3.5 修订），
			// 之后只读贯穿到渲染/反馈层——身份唯一化（v3.8.0，勿在下游重算）。
			f.FP = plugin.Fingerprint(f.File, f.Comment, f.Line)
			fp := f.FP
			if a, dup := byFP[fp]; dup && linesCompatible(a.finding.Line, f.Line) {
				// 轮内去重：severity 冲突取高；行号 ±2 内采纳新行号（匹配辅助）；
				// by 追加来源插件。
				// 行号相容闸门（收尾批修复）：指纹不含行号，同文件两处不同位置的
				// 同模板意见（OCR/规则型插件常见）此前被误并为一条、第二条永久
				// 丢失——行号不相容时保留为独立条目。
				a.mergeInto(f)
				continue
			}
			// 同文件 + 规范化评语相同 + 行号相容 → 同一问题（v1 容差语义的承接）
			ncKey := f.File + "|" + reportutil.NormalizeComment(f.Comment)
			if a, dup := byNC[ncKey]; dup && linesCompatible(a.finding.Line, f.Line) {
				a.mergeInto(f)
				continue
			}
			a2 := &mergeAcc{finding: f, fp: fp, bys: map[string]bool{f.By: true}, byOrder: []string{f.By}}
			// 位置合并兜底：不同专家对同一 file:line 用不同措辞描述时（指纹必不同），
			// 按位置归并为一条——「不同专家发现同一问题只报一次」的确定性兜底；
			// 不同行号不并（保守，两个真正不同的问题常落在相邻行）。
			// 注：曾评估加"语义相似才并"的闸门，与 v3.5.x 拍板的归并语义
			//（merger_locmerge_test 固化：同位置不同措辞视为同一问题）冲突，不采用。
			// 措辞各保留在合并文本里，人工可见双方视角。
			if f.Line > 0 {
				loc := f.File + "|" + strconv.Itoa(f.Line)
				if a, seen := byLoc[loc]; seen {
					a.mergeInto(f)
					continue
				}
				byLoc[loc] = a2
			}
			if _, seen := byFP[fp]; !seen {
				byFP[fp] = a2
			}
			if _, seen := byNC[ncKey]; !seen {
				byNC[ncKey] = a2
			}
			order = append(order, a2)
		}
	}
	return order
}

// persistPrints 先归一化 By 并批量入库：整批共用一次 writeMu（SQLite 多意见 PR
// 显著降锁竞争），返回逐条 Prior/suppressed 待消费清单。
func (m *Merger) persistPrints(ctx context.Context, pr plugin.PRContext, taskID string, at time.Time,
	order []*mergeAcc) []mergePending {
	pendings := make([]mergePending, 0, len(order))
	batch := make([]domain.PrintUpsert, 0, len(order))
	for _, a := range order {
		f := a.finding
		if len(a.bys) > 1 { // 多插件报了同一问题：by 按合并发生序拼接展示
			// v3.8.0：不再 sort.Strings——展示序决定记账键（PatternPosted/matchLowPattern
			// 取 By 首段=评语作者）。字母序会让 a-max 持续消费 z-review 写的评语的
			// 学习历史（误报模式跨插件串门）。
			f.By = strings.Join(a.byOrder, ",")
		}
		batch = append(batch, domain.PrintUpsert{FP: a.fp, Finding: f})
		pendings = append(pendings, mergePending{f: f})
	}
	if m.store != nil && len(batch) > 0 {
		results := m.store.UpsertPrints(ctx,
			pr.Platform, pr.Owner, pr.Repo, pr.Number, taskID, at, batch)
		for i := range pendings {
			if i < len(results) {
				pendings[i].res = results[i]
			}
		}
	}
	return pendings
}
