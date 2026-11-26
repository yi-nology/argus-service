// checkpoint.go —— 阶段产物单调提交（checkpoint.SeqGate 吸收）。
//
// argus 的 StageArtifacts（R1 spec / R2 摘要断点）此前是整 map 覆盖写：
// 并发或乱序写会回退。SeqGate 保证同一 RunID 的 Seq 严格递增——旧轮迟到
// 的写被拒，恢复点可信。
package gakloop

import (
	"fmt"
	"sync"

	"github.com/yi-nology/goagent-kernel/checkpoint"
)

// StageGate 阶段产物单调闸（RunID = 任务 ID）。
type StageGate struct {
	gate *checkpoint.SeqGate
	mu   sync.Mutex
}

// NewStageGate 构造单调闸。
func NewStageGate() *StageGate {
	return &StageGate{gate: checkpoint.NewSeqGate()}
}

// Commit 提交一次阶段产物：seq 必须大于该 RunID 已接受的最大值。
// 返回 false = 迟到写被拒（调用方忽略即可，不覆盖新产物）。
func (g *StageGate) Commit(runID, status string, done []string, fields map[string]string, seq uint64) (bool, error) {
	if g == nil {
		return false, fmt.Errorf("gakloop: StageGate 未装配")
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	snap := checkpoint.Snapshot{
		RunID:  runID,
		Seq:    seq,
		Status: status,
		Done:   done,
		Fields: fields,
	}
	if err := g.gate.Accept(snap); err != nil {
		// 迟到/乱序写：拒绝但不算基础设施失败。
		return false, nil
	}
	return true, nil
}

// Last 该 RunID 已接受的最大 Seq。
func (g *StageGate) Last(runID string) uint64 {
	if g == nil {
		return 0
	}
	return g.gate.Last(runID)
}

// Seed 从既有最大 seq 起步（任务重试恢复时调用，防首轮迟到写）。
func (g *StageGate) Seed(runID string, seq uint64) {
	if g == nil {
		return
	}
	g.gate.Seed(runID, seq)
}
