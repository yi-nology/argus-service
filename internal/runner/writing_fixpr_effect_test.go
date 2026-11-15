// writing_fixpr_effect_test.go —— 修复 PR 创建的副作用闸接线（C1）：
// 同一 task+head 重跑命中 Replayed，不重复 CreateCR、也不重复通知评论。
package runner

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/yi-nology/argus/internal/storage"
)

func TestOpenFixPR_EffectReplaysCR(t *testing.T) {
	store, err := storage.Open(filepath.Join(t.TempDir(), "fixpr.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })

	plat := &fixPRFake{}
	r := fixPRRunner(t, plat)
	r.Store = store
	task := &storage.Task{ID: "t-effect-cr", HeadSHA: "sha-1"}
	pr := fixPRContext()
	branch := fixBranchName(pr)

	r.openFixPR(context.Background(), task, pr, branch, 3)
	r.openFixPR(context.Background(), task, pr, branch, 3)

	opts, notes := plat.snapshot()
	if len(opts) != 1 {
		t.Fatalf("同一 task+head 重跑不得重复 CreateCR: %d", len(opts))
	}
	if len(notes) != 1 {
		t.Fatalf("重放不得重复发通知评论（首轮已发）: %d", len(notes))
	}
	if task.FixStatus != "pr_created" {
		t.Fatalf("重放后仍应落 pr_created: %q", task.FixStatus)
	}
}
