// runner_reply_controls_test.go —— 仓级回复控制三旋钮消费点（v8.4.0，TDD）：
// findings cap/floor 过滤 + throttle 门控 + OCR 按仓开关。
package runner

import (
	"context"
	"testing"
	"time"

	"github.com/oekit/logx"

	"github.com/yi-nology/argus/internal/config"
	"github.com/yi-nology/argus/internal/domain"
	"github.com/yi-nology/argus/internal/storage"
)

func fnd(sev string, line int) domain.Finding {
	return domain.Finding{Severity: sev, File: "a.go", Line: line}
}

// floor：地板以下静默，地板及以上保留（critical>high>medium>low）。
func TestFilterFindingsSeverityFloor(t *testing.T) {
	in := []domain.Finding{fnd("critical", 1), fnd("high", 2), fnd("medium", 3), fnd("low", 4)}
	got := filterFindingsForReply(in, 0, "medium")
	if len(got) != 3 {
		t.Fatalf("medium 地板应留 3 条, got %d", len(got))
	}
	for _, f := range got {
		if f.Severity == "low" {
			t.Fatal("low 应被静默")
		}
	}
	// 空地板=全报
	if got := filterFindingsForReply(in, 0, ""); len(got) != 4 {
		t.Fatalf("空地板应全报, got %d", len(got))
	}
}

// cap：severity 降序取 top N；同级保序（原顺序稳定）。
func TestFilterFindingsCap(t *testing.T) {
	in := []domain.Finding{fnd("low", 1), fnd("critical", 2), fnd("high", 3), fnd("high", 4), fnd("medium", 5)}
	got := filterFindingsForReply(in, 3, "")
	if len(got) != 3 {
		t.Fatalf("cap=3 应留 3 条, got %d", len(got))
	}
	if got[0].Severity != "critical" || got[1].Severity != "high" || got[2].Severity != "high" {
		t.Fatalf("应 severity 降序 top3: %v %v %v", got[0].Severity, got[1].Severity, got[2].Severity)
	}
	if got[1].Line != 3 || got[2].Line != 4 {
		t.Fatal("同级应保持原顺序")
	}
	// cap=0 不封顶
	if got := filterFindingsForReply(in, 0, ""); len(got) != 5 {
		t.Fatalf("cap=0 应不封顶, got %d", len(got))
	}
}

// floor+cap 叠加：先地板后截断。
func TestFilterFindingsFloorThenCap(t *testing.T) {
	in := []domain.Finding{fnd("low", 1), fnd("high", 2), fnd("medium", 3), fnd("high", 4)}
	got := filterFindingsForReply(in, 1, "medium")
	if len(got) != 1 || got[0].Severity != "high" || got[0].Line != 2 {
		t.Fatalf("floor 后 cap=1 应留首个 high: %+v", got)
	}
}

// throttle：窗口内命中（跳过回帖），窗口外/无历史/未配置不命中。
func TestReplyThrottleGate(t *testing.T) {
	st, err := storage.Open(t.TempDir() + "/throttle.db")
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	r := &Runner{Store: st, Log: logx.Nop(), Cfg: &config.Config{}}
	ctx := context.Background()

	// 无历史任务：不命中
	task := storage.NewTask("code_review", "gitlab", "o", "r", "8")
	if r.replyThrottled(ctx, task, taskKnobs{commentThrottleMin: 30}) {
		t.Fatal("无历史不应节流")
	}
	// 未配置节流：不命中
	if r.replyThrottled(ctx, task, taskKnobs{commentThrottleMin: 0}) {
		t.Fatal("throttle=0 不应节流")
	}
	// 落一个 5 分钟前 done 的同 PR 任务
	done := storage.NewTask("code_review", "gitlab", "o", "r", "8")
	done.Status = storage.StatusDone
	fin := time.Now().Add(-5 * time.Minute)
	done.FinishedAt = &fin
	if err := st.CreateTask(ctx, done); err != nil {
		t.Fatal(err)
	}
	if !r.replyThrottled(ctx, task, taskKnobs{commentThrottleMin: 30}) {
		t.Fatal("5 分钟前 done + 30 分钟窗口应节流")
	}
	if r.replyThrottled(ctx, task, taskKnobs{commentThrottleMin: 3}) {
		t.Fatal("3 分钟窗口外不应节流")
	}
	// 别仓任务不影响
	other := storage.NewTask("code_review", "gitlab", "o", "other", "8")
	if r.replyThrottled(ctx, other, taskKnobs{commentThrottleMin: 30}) {
		t.Fatal("别仓不应被节流")
	}
}

// OCR 按仓开关：false 时 inject 直接返回（不触碰工作副本/执行）。
func TestOCRDelegateRepoSwitchOff(t *testing.T) {
	r := &Runner{Cfg: &config.Config{OcrBin: "/bin/true"}, Log: logx.Nop()}
	// 开关关闭：即使配置了 bin 也不执行（early return，无告警无降级路径）
	r.injectOCRDelegate(context.Background(), nil, nil, nil, taskKnobs{ocrDelegateEnabled: false})
	// 开关开启但无工作副本：走既有降级（不 panic）
	r.injectOCRDelegate(context.Background(), nil, nil, nil, taskKnobs{ocrDelegateEnabled: true})
}
