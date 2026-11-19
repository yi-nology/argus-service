// store_misc_test.go —— 其余存储面测试：清理/租约/QA 会话/评审目标/阶段产物。

package storage

import (
	"context"
	"encoding/json"
	"testing"
	"time"
)

// 保留策略：只清已终结的超期任务，pending/running 不动。
func TestPruneTasks(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	old := time.Now().Add(-48 * time.Hour)
	for _, tc := range []*Task{
		{ID: "old-done", Kind: "code_review", Platform: "gitea", Owner: "o", Repo: "r", Number: "1",
			Status: StatusDone, CreatedAt: old},
		{ID: "old-failed", Kind: "code_review", Platform: "gitea", Owner: "o", Repo: "r", Number: "2",
			Status: StatusFailed, CreatedAt: old},
		{ID: "new-done", Kind: "code_review", Platform: "gitea", Owner: "o", Repo: "r", Number: "3",
			Status: StatusDone, CreatedAt: time.Now()},
		{ID: "old-pending", Kind: "code_review", Platform: "gitea", Owner: "o", Repo: "r", Number: "4",
			Status: StatusPending, CreatedAt: old},
	} {
		if err := s.CreateTask(ctx, tc); err != nil {
			t.Fatal(err)
		}
	}
	n, err := s.PruneTasks(ctx, time.Now().Add(-24*time.Hour))
	if err != nil || n != 2 {
		t.Fatalf("应删除 2 条超期已终结任务，got n=%d err=%v", n, err)
	}
	if _, err := s.GetTask(ctx, "old-pending"); err != nil {
		t.Fatal("pending 不应被清理")
	}
	if _, err := s.GetTask(ctx, "new-done"); err != nil {
		t.Fatal("未过期任务不应被清理")
	}
	depths, err := s.TaskCountsByTenant(ctx)
	if err != nil || depths["default"][0] != 1 || depths["default"][1] != 0 {
		t.Fatalf("TaskCountsByTenant 错误: depths=%v err=%v", depths, err)
	}
}

func TestLeaderLeaseAtomicity(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()

	// 首次获取
	if ok, err := s.TryAcquire(ctx, "poller", "A", time.Minute); err != nil || !ok {
		t.Fatalf("首次获取应成功: %v %v", ok, err)
	}
	// 他人持有未过期 → 拒绝
	if ok, err := s.TryAcquire(ctx, "poller", "B", time.Minute); err != nil || ok {
		t.Fatalf("他人持有未过期应拒绝: %v %v", ok, err)
	}
	// 持有者自续约 → 成功
	if ok, err := s.TryAcquire(ctx, "poller", "A", time.Minute); err != nil || !ok {
		t.Fatalf("持有者续约应成功: %v %v", ok, err)
	}
	// 主动让位
	if err := s.Release(ctx, "poller", "A"); err != nil {
		t.Fatal(err)
	}
	// B 立即当选
	if ok, err := s.TryAcquire(ctx, "poller", "B", time.Minute); err != nil || !ok {
		t.Fatalf("让位后 B 应当选: %v %v", ok, err)
	}
	// 非持有者 Release 无效（B 仍持有）
	if err := s.Release(ctx, "poller", "A"); err != nil {
		t.Fatal(err)
	}
	if ok, err := s.TryAcquire(ctx, "poller", "A", time.Minute); err != nil || ok {
		t.Fatalf("非持有者释放后仍应拒绝: %v %v", ok, err)
	}
	// 过期换主：直改 expires_at 模拟失联
	if err := s.db.Model(&LeaderLease{}).Where("lease_key = ?", "poller").
		Update("expires_at", time.Now().UTC().Add(-time.Second)).Error; err != nil {
		t.Fatal(err)
	}
	if ok, err := s.TryAcquire(ctx, "poller", "A", time.Minute); err != nil || !ok {
		t.Fatalf("过期后应换主: %v %v", ok, err)
	}
}

func TestQASessionRoundtripAndExpiry(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	key := "gitea:o/r#12"

	// 不存在 → nil
	if sess, err := s.GetQASession(ctx, key, time.Hour); err != nil || sess != nil {
		t.Fatalf("无会话应返回 nil: %v %v", sess, err)
	}
	// 保存 → 读回
	hist := []QATurn{{Question: "为什么这里没用锁?", Answer: "因为只读…", At: time.Now().UTC().Format(time.RFC3339)}}
	hj, _ := json.Marshal(hist)
	if err := s.SaveQASession(ctx, &QASession{SessionKey: key, HistoryJSON: string(hj)}); err != nil {
		t.Fatal(err)
	}
	sess, err := s.GetQASession(ctx, key, time.Hour)
	if err != nil || sess == nil {
		t.Fatalf("应读到会话: %v %v", sess, err)
	}
	var back []QATurn
	if err := json.Unmarshal([]byte(sess.HistoryJSON), &back); err != nil || len(back) != 1 {
		t.Fatalf("历史应往返一致: %v", err)
	}
	// 过期 → 视为无会话
	if err := s.db.Model(&QASession{}).Where("session_key = ?", key).
		Update("updated_at", time.Now().UTC().Add(-2*time.Hour)).Error; err != nil {
		t.Fatal(err)
	}
	if sess, err := s.GetQASession(ctx, key, time.Hour); err != nil || sess != nil {
		t.Fatalf("过期会话应视为不存在: %v %v", sess, err)
	}
}

// 收尾批修复回归：历史报告评论 ID 缓存——去重追加、封顶淘汰、新→旧读出。
func TestReviewTargetKnownCommentsRoundtrip(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()

	for _, id := range []int64{101, 102, 103} {
		if err := s.AppendReviewTargetComment(ctx, "gitea", "acme", "app", "7", id); err != nil {
			t.Fatal(err)
		}
	}
	// 重复追加去重
	if err := s.AppendReviewTargetComment(ctx, "gitea", "acme", "app", "7", 102); err != nil {
		t.Fatal(err)
	}
	ids, err := s.KnownReviewTargetComments(ctx, "gitea", "acme", "app", "7")
	if err != nil {
		t.Fatal(err)
	}
	if len(ids) != 3 || ids[0] != 103 || ids[2] != 101 {
		t.Fatalf("应 3 条且新→旧，got %v", ids)
	}
	// 无缓存仓库返回 nil 而非错误
	if ids, err := s.KnownReviewTargetComments(ctx, "gitea", "acme", "app", "8"); err != nil || ids != nil {
		t.Fatalf("无缓存应返回 nil/nil，got %v %v", ids, err)
	}
	// 非法 id 忽略
	if err := s.AppendReviewTargetComment(ctx, "gitea", "acme", "app", "7", 0); err != nil {
		t.Fatalf("id<=0 应静默忽略: %v", err)
	}
}

// v3.12.0 G2 断点续审：阶段产物 checkpoint 合并写入/回流。
func TestMergeTaskStageArtifacts(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	task := &Task{ID: "t-ckpt", Kind: "requirement_compliance",
		Platform: "gitea", Owner: "acme", Repo: "app", Number: "7"}
	if err := s.CreateTask(ctx, task); err != nil {
		t.Fatal(err)
	}
	id := task.ID
	if err := s.MergeTaskStageArtifacts(ctx, id, map[string]string{"checkpoint_r1_spec": `{"source":"x"}`}); err != nil {
		t.Fatal(err)
	}
	// 第二次合并：保留既有键 + 覆盖/新增
	if err := s.MergeTaskStageArtifacts(ctx, id, map[string]string{
		"checkpoint_r1_spec":    `{"source":"y"}`,
		"checkpoint_r2_summary": "新增了登录",
	}); err != nil {
		t.Fatal(err)
	}
	task, err := s.GetTask(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]string
	if err := json.Unmarshal([]byte(task.StageArtifacts), &got); err != nil {
		t.Fatal(err)
	}
	if got["checkpoint_r1_spec"] != `{"source":"y"}` || got["checkpoint_r2_summary"] != "新增了登录" {
		t.Fatalf("合并结果不符: %+v", got)
	}
}
