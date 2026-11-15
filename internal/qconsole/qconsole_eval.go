package qconsole

import (
	"encoding/json"
	"os"
	"time"
)

// LoadEvalTrend 读评测基线报告（P2-02 artifact，eval/results/baseline-latest.json）；
// 文件不存在返回 nil（趋势面缺位属常态，不构成控制台错误）。
//
// by_language 形态（终审 Important）：生产者 evalq.LanguageReport 是嵌套
// [{language, metrics:{fp_rate,miss_rate,...}}]——EvalLangMets 是扁平投影，
// 直接 Unmarshal 会把每个分语言指标零填（/console 分语言行恒渲 0.0%）。故经
// 嵌套影子结构解码再映射；包内联 metric 字段结构、不 import evalq（保持解耦）。
// 容错口径不变：缺文件 → nil、坏 JSON → nil、缺 by_language → 空表。
func LoadEvalTrend(path string) *EvalTrend {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	// 影子结构：镜像 EvalTrend，唯 ByLanguage 按生产者嵌套形态建模。
	var shadow struct {
		GeneratedAt time.Time     `json:"generated_at"`
		Model       string        `json:"model"`
		Stability   float64       `json:"stability"`
		Overall     EvalTrendMets `json:"overall"`
		ByLanguage  []struct {
			Language string `json:"language"`
			Metrics  struct {
				FP        float64 `json:"fp_rate"`
				Miss      float64 `json:"miss_rate"`
				P         float64 `json:"precision"`
				R         float64 `json:"recall"`
				CFP       float64 `json:"clean_case_fp_rate"`
				Unlabeled int     `json:"unlabeled_count"`
			} `json:"metrics"`
		} `json:"by_language"`
	}
	if err := json.Unmarshal(raw, &shadow); err != nil {
		return nil
	}
	t := &EvalTrend{
		GeneratedAt: shadow.GeneratedAt,
		Model:       shadow.Model,
		Stability:   shadow.Stability,
		Overall:     shadow.Overall,
		ByLanguage:  make([]EvalLangMets, 0, len(shadow.ByLanguage)),
	}
	for _, l := range shadow.ByLanguage {
		t.ByLanguage = append(t.ByLanguage, EvalLangMets{
			Language: l.Language,
			FP:       l.Metrics.FP,
			Miss:     l.Metrics.Miss,
			CFP:      l.Metrics.CFP,
		})
	}
	return t
}
