// gate_test.go —— make quality-gate（v8.5.0，roadmap §8）：最新两批同口径
// 环比阈值判定。跨口径切换线段跳过（禁直比）；批次不足 2 时跳过（无可比）。
package quality_test

import (
	"path/filepath"
	"testing"

	"github.com/yi-nology/argus/internal/qualityops"
)

func TestQualityGate(t *testing.T) {
	pts, err := qualityops.LoadBatchPoints(filepath.Join("..", "..", "eval", "results"))
	if err != nil {
		t.Fatalf("读取批次序列失败: %v", err)
	}
	if len(pts) < 2 {
		t.Skip("批次不足 2，无环比可检")
	}
	prev, cur := pts[len(pts)-2], pts[len(pts)-1]
	res := qualityops.Gate(prev, cur, qualityops.DefaultGateThresholds())
	for _, reason := range res.Reasons {
		t.Log(reason)
	}
	if res.Skipped {
		t.Logf("跨口径跳过：%s → %s（差值仅供形式展示）", prev.Name, cur.Name)
		return
	}
	if !res.OK {
		t.Fatalf("质量门禁不通过（%s → %s）: %v", prev.Name, cur.Name, res.Reasons)
	}
	t.Logf("质量门禁通过：%s → %s", prev.Name, cur.Name)
}
