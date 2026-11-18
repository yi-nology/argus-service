// Package qualityops 质量运营数据面（Phase 4 spec）：批次趋势环比、
// 误报/回归候选清单、有效发现率与修复率两个可证伪指标的计算单源。
// 纯函数纪律：只算不取数——数据经参数注入（archive 文件/查询结果切片），
// 取数面在 storage 与 test 入口。
package qualityops

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// BatchPoint 一个评测批次的指标快照（同口径才可环比——口径切换点标注）。
type BatchPoint struct {
	Name            string  `json:"name"` // 文件名（批次名）
	GeneratedAt     string  `json:"generated_at"`
	Model           string  `json:"model"`
	Precision       float64 `json:"precision"`
	Recall          float64 `json:"recall"`
	F1              float64 `json:"f1"`
	CleanCaseFPRate float64 `json:"clean_case_fp_rate"` // 旧口径文件=0 且 OldMetric=true
	UnlabeledCount  int     `json:"unlabeled_count"`
	Errored         int     `json:"errored"`
	OldMetric       bool    `json:"old_metric"` // 口径切换点前（Phase1②）——环比跨此线标不可直比
}

// LoadBatchPoints 读目录下全部 *.json 批次报告为趋势序列（按 GeneratedAt 升序）。
// 旧口径文件（无 clean_case_fp_rate 键）标 OldMetric=true。
func LoadBatchPoints(dir string) ([]BatchPoint, error) {
	files, err := filepath.Glob(filepath.Join(dir, "*.json"))
	if err != nil {
		return nil, err
	}
	var out []BatchPoint
	for _, f := range files {
		raw, err := os.ReadFile(f)
		if err != nil {
			continue // 单文件坏不阻断序列
		}
		var probe struct {
			GeneratedAt string `json:"generated_at"`
			Model       string `json:"model"`
			Errored     int    `json:"errored"`
			Overall     struct {
				Precision       float64  `json:"precision"`
				Recall          float64  `json:"recall"`
				F1              float64  `json:"f1"`
				CleanCaseFPRate *float64 `json:"clean_case_fp_rate"`
				UnlabeledCount  *int     `json:"unlabeled_count"`
			} `json:"overall"`
		}
		if json.Unmarshal(raw, &probe) != nil {
			continue
		}
		p := BatchPoint{
			Name:        strings.TrimSuffix(filepath.Base(f), ".json"),
			GeneratedAt: probe.GeneratedAt,
			Model:       probe.Model,
			Precision:   probe.Overall.Precision,
			Recall:      probe.Overall.Recall,
			F1:          probe.Overall.F1,
			Errored:     probe.Errored,
		}
		if probe.Overall.CleanCaseFPRate == nil || probe.Overall.UnlabeledCount == nil {
			// 旧口径或缺分母：只有 clean_case_fp_rate 没有 unlabeled_count
			// 时该率不可解释（此前直接解引用空指针 panic）。
			p.OldMetric = true
		} else {
			p.CleanCaseFPRate = *probe.Overall.CleanCaseFPRate
			p.UnlabeledCount = *probe.Overall.UnlabeledCount
		}
		out = append(out, p)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].GeneratedAt < out[j].GeneratedAt })
	return out, nil
}

// Delta 两批次环比（同口径差值；跨口径切换线标 CrossMetric 不可直比）。
type Delta struct {
	From, To              string
	CrossMetric           bool // 一旧一新口径——差值仅供形式展示，禁解读
	Precision, Recall, F1 float64
	CleanCaseFPRate       float64 // 仅同新口径有意义
	Unlabeled             int
}

// Deltas 相邻批次环比序列（n-1 段）。
func Deltas(pts []BatchPoint) []Delta {
	var out []Delta
	for i := 1; i < len(pts); i++ {
		a, b := pts[i-1], pts[i]
		out = append(out, Delta{
			From: a.Name, To: b.Name,
			CrossMetric:     a.OldMetric != b.OldMetric,
			Precision:       b.Precision - a.Precision,
			Recall:          b.Recall - a.Recall,
			F1:              b.F1 - a.F1,
			CleanCaseFPRate: b.CleanCaseFPRate - a.CleanCaseFPRate,
			Unlabeled:       b.UnlabeledCount - a.UnlabeledCount,
		})
	}
	return out
}

// EffectiveFindings 有效发现率（Phase 4 定义）：verified+structurally_valid
// 意见数 / 总意见数（Phase 2 Evidence 四态直接消费）。
func EffectiveFindings(statuses []string) (rate float64, num, den int) {
	den = len(statuses)
	if den == 0 {
		return 0, 0, 0
	}
	for _, st := range statuses {
		if st == "verified" || st == "structurally_valid" {
			num++
		}
	}
	return float64(num) / float64(den), num, den
}

// FixOutcome 修复尝试结果（Phase 3 FixStatus 聚合输入）。
type FixOutcome string

const (
	FixMerged   FixOutcome = "merged" // 修复 PR 已合并（真采纳）
	FixOpen     FixOutcome = "open"   // pr_created/cr_existed 未合并
	FixNotTried FixOutcome = "not_tried"
	FixFailed   FixOutcome = "failed" // push_failed/pr_failed
)

// FixRate 修复率（Phase 4 定义）：merged / (merged+open)——failed/not_tried
// 不入分母（修复尝试未达成不冒充分母）；unknown（平台 merge 查询不可得）
// 按 open 计（保守不放大）。
func FixRate(outcomes []FixOutcome) (rate float64, merged, denom int) {
	for _, o := range outcomes {
		switch o {
		case FixMerged:
			merged++
			denom++
		case FixOpen, FixNotTried: // unknown 归 open（保守）
			if o == FixOpen {
				denom++
			}
		}
	}
	if denom == 0 {
		return 0, 0, 0
	}
	return float64(merged) / float64(denom), merged, denom
}

// RenderDeltaMarkdown 环比表渲染（同口径段落 + 跨口径警示行）。
func RenderDeltaMarkdown(pts []BatchPoint, ds []Delta) string {
	var b strings.Builder
	b.WriteString("# 质量运营环比报告（Phase 4）\n\n")
	b.WriteString(fmt.Sprintf("- 生成：%s\n- 批次数：%d（环比段 %d）\n\n",
		time.Now().UTC().Format(time.RFC3339), len(pts), len(ds)))

	b.WriteString("## 批次序列\n\n| 批次 | 生成时间 | P | R | F1 | CleanCaseFP | Unlabeled | 口径 |\n|---|---|---|---|---|---|---|---|\n")
	for _, p := range pts {
		metric := "新"
		if p.OldMetric {
			metric = "旧"
		}
		b.WriteString(fmt.Sprintf("| %s | %s | %.3f | %.3f | %.3f | %.3f | %d | %s |\n",
			p.Name, p.GeneratedAt, p.Precision, p.Recall, p.F1, p.CleanCaseFPRate, p.UnlabeledCount, metric))
	}

	b.WriteString("\n## 相邻环比（Δ=后−前）\n\n| 从 → 到 | ΔP | ΔR | ΔF1 | ΔCleanCaseFP | ΔUnlabeled | 口径线 |\n|---|---|---|---|---|---|---|\n")
	for _, d := range ds {
		line := "—"
		if d.CrossMetric {
			line = "**跨口径切换线，禁直比**"
		}
		b.WriteString(fmt.Sprintf("| %s → %s | %+.3f | %+.3f | %+.3f | %+.3f | %+d | %s |\n",
			d.From, d.To, d.Precision, d.Recall, d.F1, d.CleanCaseFPRate, d.Unlabeled, line))
	}
	b.WriteString("\n> 红绿判定纪律（Phase 4 §3.3）：任一核心指标同口径环比恶化超阈值（如 ΔP < -0.05）→ 该批次标黄须写归因。\n")
	b.WriteString("> 定义见 docs/ops/quality-ops.md（有效发现率/修复率）；跨口径段差值仅供形式展示。\n")
	return b.String()
}
