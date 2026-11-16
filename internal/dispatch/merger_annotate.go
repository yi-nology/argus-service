// merger_annotate.go —— 标注面：交叉审查分歧标注与学习环低置信标注重放。

package dispatch

import (
	"context"
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/yi-nology/argus/internal/learning"
	"github.com/yi-nology/argus/internal/plugin"
)

// AnnotateCrossReview 交叉审查分歧标注（v3.3）：编队成员（≥2）对同一文件
// ±5 行内各自报告了不同指纹的意见时，给"仅一方报告"的意见追加分歧标注——
// 人审时优先看专家意见不一致的区域。幂等：重复调用不叠加标注。
func (m *Merger) AnnotateCrossReview(mf *plugin.MergedFindings, members []string) {
	if mf == nil || len(members) < 2 {
		return
	}
	memberSet := map[string]bool{}
	for _, n := range members {
		memberSet[n] = true
	}
	m.annotateCross(mf, memberSet)
}

// annotateCross 实际标注（AnnotateCrossReview 的实现体，独立便于测试）。
func (m *Merger) annotateCross(mf *plugin.MergedFindings, memberSet map[string]bool) {
	const crossMark = "⚠️ 交叉审查分歧"
	// 先剥离旧标注（幂等）
	for i := range mf.Findings {
		f := &mf.Findings[i]
		if idx := strings.Index(f.Comment, "（"+crossMark); idx >= 0 {
			f.Comment = strings.TrimSpace(f.Comment[:idx])
		}
	}
	type rec struct {
		idx  int
		bys  []string
		line int
		file string
	}
	// byMembers 拆分 By：Merge 阶段多插件命中同题会把 By 拼成 "a,b"——不拆开
	// 的话合并条目（恰是专家们意见一致的证据）整体不进 recs，反而被误标
	// 「编队另一专家未复现」（深挖修复：标注方向此前反了）。
	byMembers := func(by string) []string {
		var out []string
		for _, n := range strings.Split(by, ",") {
			if n = strings.TrimSpace(n); memberSet[n] {
				out = append(out, n)
			}
		}
		return out
	}
	sharesAny := func(a, b []string) bool {
		for _, x := range a {
			for _, y := range b {
				if x == y {
					return true
				}
			}
		}
		return false
	}
	var recs []rec
	for i, f := range mf.Findings {
		bys := byMembers(f.By)
		if len(bys) == 0 {
			continue
		}
		recs = append(recs, rec{idx: i, bys: bys, line: f.Line, file: f.File})
	}
	if len(recs) < 2 {
		return
	}
	// 按文件分桶：分歧判定只在同文件内两两比对，免全量 O(n²)（大编队多意见时）。
	byFile := map[string][]rec{}
	for _, r := range recs {
		byFile[r.file] = append(byFile[r.file], r)
	}
	disagreed := 0
	for _, a := range recs {
		// 条目自身已聚合多专家（Merge 合并，bys≥2）= 意见一致，无分歧可言
		replicated := len(a.bys) >= 2
		if !replicated {
			for _, b := range byFile[a.file] {
				if a.idx == b.idx || sharesAny(a.bys, b.bys) {
					continue
				}
				if abs(a.line-b.line) <= 5 {
					replicated = true
					break
				}
			}
		}
		if !replicated {
			f := &mf.Findings[a.idx]
			f.Comment += fmt.Sprintf("（%s：编队另一专家未复现此问题，请优先人工复核）", crossMark)
			disagreed++
		}
	}
	if disagreed > 0 {
		names := slices.Sorted(maps.Keys(memberSet))
		mf.CrossReview = fmt.Sprintf("%s 编队交叉审查，分歧 %d 处已标注", strings.Join(names, "+"), disagreed)
	}
}

// AnnotateLowConfidence 在 R3.5 之后重放学习环低置信标注。
// 原因：R3.5 按 id 回写 comment 时会用模型修订稿覆盖 Merger 阶段追加的后缀，
// 「历史置信度低」提示在终审后丢失。幂等：已含标注则跳过。
func (m *Merger) AnnotateLowConfidence(ctx context.Context, pr plugin.PRContext, mf *plugin.MergedFindings) {
	if mf == nil || !m.learn.Enabled || m.patterns == nil {
		return
	}
	low, err := m.patterns.ListLowConfidencePatterns(ctx, pr.Platform, pr.Owner, pr.Repo,
		m.learn.MinSamples, m.learn.SoftFloor, 0)
	if err != nil || len(low) == 0 {
		return
	}
	const mark = "（本仓库同类意见历史置信度低）"
	lowIdx := patternIndex(low, m.learn.MinSamples)
	for i := range mf.Findings {
		f := &mf.Findings[i]
		if strings.Contains(f.Comment, mark) {
			continue
		}
		pkey := learning.PatternKey(learning.FirstBy(f.By), f.Comment)
		if hit := lookupLow(lowIdx, pkey, m.learn.SoftFloor); hit != nil {
			f.Comment += mark
		}
	}
}
