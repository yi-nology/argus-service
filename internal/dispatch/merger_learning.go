// merger_learning.go —— 学习环闸门与标注：HardFloor 丢弃、PatternPosted
// 记账、SoftFloor/展示标注与低置信索引。

package dispatch

import (
	"context"
	"fmt"
	"time"

	"github.com/oagentkit/textx/textutil"

	"github.com/yi-nology/argus/internal/learning"
	"github.com/yi-nology/argus/internal/plugin"
)

// applyLearning 学习环闸门与标注（顺序不变量：HardFloor 丢弃 → PatternPosted 记账
// → SoftFloor 标注 → 展示标注，错账史见 v3.9.0/v3.9.1）。
func (m *Merger) applyLearning(ctx context.Context, pr plugin.PRContext, at time.Time,
	out *plugin.MergedFindings, pendings []mergePending) {
	// 学习环：一次性拉本仓库低置信模式（HardFloor），供丢弃与标注共用；
	// 建 key 索引后循环内 O(1) 查表（原 matchLowPattern 为 O(n·m) 线扫 + 每条
	// finding 重算 PatternKey，意见多的 PR 在热路径上）。
	var lowIdx map[string]*learning.PatternState
	if m.learn.Enabled && m.patterns != nil {
		ps, err := m.patterns.ListLowConfidencePatterns(ctx, pr.Platform, pr.Owner, pr.Repo,
			m.learn.MinSamples, m.learn.SoftFloor, 0)
		if err != nil {
			m.log.Warn("argus.merger.pattern_list_failed", "error", err.Error())
		} else {
			lowIdx = patternIndex(ps, m.learn.MinSamples)
		}
	}
	for _, p := range pendings {
		f, res := p.f, p.res
		if res.Err != nil {
			m.log.Error("argus.merger.print_upsert_failed", "error", res.Err.Error())
		} else if res.Suppressed {
			out.Suppressed++
			continue
		}
		// 跨轮历史（批量入库返回的 Prior）：复现次数与 regret 作可信度信号。
		prior := res.Prior
		// 学习环记账/匹配的权威评语（v3.9.1）：跨轮既有条目以 prints 行原文为
		// 准——轮内合并拼接（"（X 同位置补充）…"）发生在 acc.finding 上，而条目
		// 指纹=首见评语的指纹，内存文本会偏离历史模式行的键；新条目用本轮文本
		//（UpsertPrint 首建即存该文本，regret 回查同源，两键天然一致）。
		patternComment := f.Comment
		if prior != nil && prior.Comment != "" {
			patternComment = prior.Comment
		}
		probe := f
		probe.Comment = patternComment
		// PatternKey 整轮只算一次（规范化+哈希是热路径），Hard/Soft 两闸共用。
		var pkey string
		if m.learn.Enabled {
			pkey = learning.PatternKey(learning.FirstBy(f.By), patternComment)
		}

		// 学习环硬闸门：样本充足且置信度低于 HardFloor 的 low 条目直接丢弃。
		// medium/high 不丢——误杀代价不对称（design D4）。
		if m.learn.Enabled && f.Severity == plugin.SeverityLow {
			if hit := lookupLow(lowIdx, pkey, m.learn.HardFloor); hit != nil {
				out.Degraded = append(out.Degraded, fmt.Sprintf(
					"学习环抑制：%s/%s（模式置信度 %.2f）", hit.Plugin, textutil.TruncEllipsis(hit.PatternText, 40), hit.Confidence))
				// 记 posted 以外：被闸门吃掉不记 posted，避免抬高置信度
				continue
			}
		}
		// PatternPosted：意见进入报告后记账（失败只告警）。必须先于 SoftFloor/
		// 展示两类标注——PatternKey 由评语派生（pattern.go），标注文本进键会把
		// posted 记进"原文+标注"的影子行，真实模式行只累积 regret，置信度单调
		// 下跌后 HardFloor 永久吞掉本应正确的条目（v3.9.0 修复只挪过了展示标注，
		// v3.9.1 补齐 SoftFloor 标注先于记账的漏网层）。
		if m.learn.Enabled && m.patterns != nil {
			if err := m.patterns.PatternPosted(ctx, pr.Platform, pr.Owner, pr.Repo, learning.FirstBy(f.By), probe, at); err != nil {
				m.log.Warn("argus.merger.pattern_posted_failed", "error", err.Error())
			}
		}
		// 模式级低置信标注（SoftFloor）：medium/high 也标注，人审优先看。
		// 匹配同样基于权威评语 probe；标注恒在记账/匹配之后（错账见上，
		// v3.9.0 审查修复确立的不变量）。
		if m.learn.Enabled {
			if hit := lookupLow(lowIdx, pkey, m.learn.SoftFloor); hit != nil {
				f.Comment += "（本仓库同类意见历史置信度低）"
			}
		}
		// 展示标注（注释在入库与模式记账之后改写，不污染指纹/prints 原文/
		// 模式键）：复现 = 同一问题跨轮再现；高 regret = 历史上多次被终审判为
		// 误报嫌疑。所以必须放在本循环最后。
		if prior != nil {
			if prior.TimesSeen >= 2 {
				f.Comment = fmt.Sprintf("%s（跨轮复现 %d 次）", f.Comment, prior.TimesSeen+1)
			}
			if prior.RegretCount >= 2 {
				f.Comment += fmt.Sprintf("（历史被 R3.5 反思剔除 %d 次，可信度低）", prior.RegretCount)
			}
		}
		out.Findings = append(out.Findings, f)
	}
}

// patternIndex 把低置信模式列表建成 key → *PatternState 索引（仅保留够样本的项）。
func patternIndex(low []learning.PatternState, minSamples int) map[string]*learning.PatternState {
	if len(low) == 0 {
		return nil
	}
	idx := make(map[string]*learning.PatternState, len(low))
	for i := range low {
		if low[i].Samples >= minSamples {
			idx[low[i].Pattern] = &low[i]
		}
	}
	return idx
}

// lookupLow 按 Pattern 键查低置信索引；confidence ≥ floor 时返回 nil（未够低不消费）。
func lookupLow(idx map[string]*learning.PatternState, key string, floor float64) *learning.PatternState {
	if idx == nil || key == "" {
		return nil
	}
	if hit := idx[key]; hit != nil && hit.Confidence < floor {
		return hit
	}
	return nil
}
