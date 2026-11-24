// gate.go —— 同口径质量门禁（v8.5.0，roadmap §8）：把「任一核心指标同口径
// 环比恶化超阈值」从纪律文字变成可执行判定。纯函数，不取数。

package qualityops

import "fmt"

// GateThresholds 同口径环比门禁阈值（零值=该项关闭）。
type GateThresholds struct {
	MaxF1Drop          float64 // F1 最大允许跌幅（正数，如 0.05）
	MaxCleanCaseFPRise float64 // 干净样例误报率最大允许涨幅（如 0.05）
	MaxUnlabeledRise   int     // Unlabeled 最大允许增加条数（如 10）
}

// DefaultGateThresholds 缺省阈值：F1 跌 >0.05 / 干净样例误报率涨 >0.05 /
// Unlabeled 增 >10 条 → 不通过（与 docs/ops/quality-ops.md 纪律一致）。
func DefaultGateThresholds() GateThresholds {
	return GateThresholds{MaxF1Drop: 0.05, MaxCleanCaseFPRise: 0.05, MaxUnlabeledRise: 10}
}

// floatEps 浮点比较容差：0.90-0.85 在 IEEE754 下是 0.050000000000000044，
// 恰等于阈值不应判失败（严格大于语义）——阈值判定必须带容差。
const floatEps = 1e-9

// GateResult 门禁结论：跨口径切换线（旧↔新）不可比 → Skipped（不失败）。
type GateResult struct {
	OK      bool
	Skipped bool
	Reasons []string
}

// Gate 比较 prev→cur 两批次；任一启用阈值被突破即 OK=false 并累积原因。
// 跨口径段直接 Skipped（差值仅供形式展示，禁解读——Phase1② 纪律）。
func Gate(prev, cur BatchPoint, th GateThresholds) GateResult {
	if prev.OldMetric != cur.OldMetric {
		return GateResult{OK: true, Skipped: true, Reasons: []string{
			fmt.Sprintf("跨口径切换线（%s 旧=%v → %s 新=%v），不可直比，跳过", prev.Name, prev.OldMetric, cur.Name, cur.OldMetric),
		}}
	}
	res := GateResult{OK: true}
	if th.MaxF1Drop > 0 {
		if drop := prev.F1 - cur.F1; drop > th.MaxF1Drop+floatEps {
			res.OK = false
			res.Reasons = append(res.Reasons,
				fmt.Sprintf("F1 下跌 %.3f（>%.3f）: %s %.3f → %s %.3f", drop, th.MaxF1Drop, prev.Name, prev.F1, cur.Name, cur.F1))
		}
	}
	if th.MaxCleanCaseFPRise > 0 {
		if rise := cur.CleanCaseFPRate - prev.CleanCaseFPRate; rise > th.MaxCleanCaseFPRise+floatEps {
			res.OK = false
			res.Reasons = append(res.Reasons,
				fmt.Sprintf("干净样例误报率上涨 %.3f（>%.3f）: %s %.3f → %s %.3f",
					rise, th.MaxCleanCaseFPRise, prev.Name, prev.CleanCaseFPRate, cur.Name, cur.CleanCaseFPRate))
		}
	}
	if th.MaxUnlabeledRise > 0 {
		if rise := cur.UnlabeledCount - prev.UnlabeledCount; rise > th.MaxUnlabeledRise {
			res.OK = false
			res.Reasons = append(res.Reasons,
				fmt.Sprintf("Unlabeled 增加 %d 条（>%d）: %s %d → %s %d",
					rise, th.MaxUnlabeledRise, prev.Name, prev.UnlabeledCount, cur.Name, cur.UnlabeledCount))
		}
	}
	return res
}
