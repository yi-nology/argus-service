// store_task_test.go —— 任务生命周期测试：抢占/复位/staleness/直建/fencing/退避/优先级。

package storage

import (
	"context"
	"testing"
	"time"
)

// worker 抢占：pending→running 原子；启动重置 running→pending。
func TestClaimAndReset(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	t1 := mkTask("t1", nil, "")
	t2 := mkTask("t2", nil, "")
	t2.Number = "8" // v6.8.0 在飞唯一约束（P1-03）：认领机制测试各占一号
	if err := s.CreateTask(ctx, t1); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateTask(ctx, t2); err != nil {
		t.Fatal(err)
	}
	task, ok, err := s.ClaimNextPending(ctx)
	if err != nil || !ok || task.ID != "t1" {
		t.Fatalf("抢占顺序错误: %v %v %+v", ok, err, task)
	}
	if task.Status != StatusRunning {
		t.Fatalf("抢占后应 running: %s", task.Status)
	}
	// 重置（模拟启动恢复；staleAfter=0 ⇒ 心跳必过期）
	n, err := s.ResetRunningToPending(ctx, 0)
	if err != nil || n != 1 {
		t.Fatalf("重置失败: n=%d err=%v", n, err)
	}
}

// v3.0.2 回归：心跳新鲜的 running 任务不得被重置（多副本共库时滚动重启
// 不误伤其他实例在途任务的核心不变量）；心跳死亡（阈值归零）才重置。
func TestResetRunningRespectsHeartbeat(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	if err := s.CreateTask(ctx, mkTask("t1", nil, "")); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := s.ClaimNextPending(ctx); err != nil || !ok {
		t.Fatalf("claim 失败: ok=%v err=%v", ok, err)
	}
	// claim 即打心跳（刚刚）：90s 阈值下不得重置——丢了 heartbeat_at 条件会双执行
	if n, err := s.ResetRunningToPending(ctx, 90*time.Second); err != nil || n != 0 {
		t.Fatalf("心跳新鲜不应重置: n=%d err=%v", n, err)
	}
	// 阈值归零 ⇒ 心跳必过期：应重置（崩溃恢复路径）
	if n, err := s.ResetRunningToPending(ctx, 0); err != nil || n != 1 {
		t.Fatalf("过期心跳应重置: n=%d err=%v", n, err)
	}
}

// v3.0.2 回归：空 head 的手动任务不构成 staleness——此前"手动任务先压 stale 再失败"
// 会让 webhook 任务本轮审查永久静默丢失。
func TestStalenessIgnoresEmptyHeadTasks(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	t1 := mkTask("t1", nil, "sha-a") // webhook 任务（带 head）
	if err := s.CreateTask(ctx, t1); err != nil {
		t.Fatal(err)
	}
	time.Sleep(10 * time.Millisecond)
	manual := mkTask("t2", nil, "") // 手动任务（head 为空，执行期才回填）
	if err := s.CreateTask(ctx, manual); err != nil {
		t.Fatal(err)
	}
	// 更晚的手动任务 pending：不压制 t1
	stale, err := s.HasNewerNonFailedTask(ctx, "gitea", "acme", "app", "7", "t1", t1.CreatedAt)
	if err != nil || stale {
		t.Fatalf("空 head 手动任务不应构成 staleness: stale=%v err=%v", stale, err)
	}
	// 对照：带 head 的更晚任务仍构成 staleness
	_ = s.CreateTask(ctx, mkTask("t3", nil, "sha-b"))
	stale, err = s.HasNewerNonFailedTask(ctx, "gitea", "acme", "app", "7", "t1", t1.CreatedAt)
	if err != nil || !stale {
		t.Fatalf("带 head 的更晚任务应构成 staleness: stale=%v err=%v", stale, err)
	}
}

// staleness 单条件：存在更晚创建的非 failed 任务 ⇒ stale。
func TestStaleness(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	t1 := mkTask("t1", nil, "sha-a")
	if err := s.CreateTask(ctx, t1); err != nil {
		t.Fatal(err)
	}
	time.Sleep(10 * time.Millisecond) // 保证 created_at 可区分
	t2 := mkTask("t2", nil, "sha-b")
	if err := s.CreateTask(ctx, t2); err != nil {
		t.Fatal(err)
	}

	// t1（更早）：存在更晚的 t2（pending）→ stale
	stale, err := s.HasNewerNonFailedTask(ctx, "gitea", "acme", "app", "7", "t1", t1.CreatedAt)
	if err != nil || !stale {
		t.Fatalf("t1 应 stale: %v %v", stale, err)
	}
	// t2（最晚）：无更晚任务 → 非 stale
	stale, err = s.HasNewerNonFailedTask(ctx, "gitea", "acme", "app", "7", "t2", t2.CreatedAt)
	if err != nil || stale {
		t.Fatalf("t2 不应 stale: %v %v", stale, err)
	}
	// 更晚的任务 failed 时不构成 staleness
	_ = s.FinishTask(ctx, "t2", StatusFailed, "", "", "", "boom", 0, 0)
	t2AfterFail, _ := s.GetTask(ctx, "t2")
	stale, err = s.HasNewerNonFailedTask(ctx, "gitea", "acme", "app", "7", "t1", t1.CreatedAt)
	if err != nil || stale || t2AfterFail.Status != StatusFailed {
		t.Fatalf("failed 任务不应构成 staleness: stale=%v err=%v", stale, err)
	}
}

// 幂等直建：同 PR 已有 pending/running 任务时不再新建，返回既有任务；
// 无在飞任务时正常创建。TaskAPI 手工触发路径的防重复审查防线。
func TestCreateTaskDirectIfIdle(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()

	first := &Task{
		ID: NewTaskID(), Kind: "requirement_compliance", Platform: "gitea",
		Owner: "o", Repo: "r", Number: "1", Status: StatusPending, CreatedAt: time.Now(),
	}
	existing, created, err := s.CreateTaskDirectIfIdle(ctx, first)
	if err != nil {
		t.Fatal(err)
	}
	if !created || existing != nil {
		t.Fatalf("first create: created=%v existing=%v, want true/nil", created, existing)
	}

	// pending 在飞 → 幂等命中
	second := &Task{
		ID: NewTaskID(), Kind: "requirement_compliance", Platform: "gitea",
		Owner: "o", Repo: "r", Number: "1", Status: StatusPending, CreatedAt: time.Now(),
	}
	existing, created, err = s.CreateTaskDirectIfIdle(ctx, second)
	if err != nil {
		t.Fatal(err)
	}
	if created || existing == nil || existing.ID != first.ID {
		t.Fatalf("in-flight dedup: created=%v existing=%v, want false/%s", created, existing, first.ID)
	}

	// 不同 PR 不受影响
	other := &Task{
		ID: NewTaskID(), Kind: "requirement_compliance", Platform: "gitea",
		Owner: "o", Repo: "r", Number: "2", Status: StatusPending, CreatedAt: time.Now(),
	}
	if _, created, err = s.CreateTaskDirectIfIdle(ctx, other); err != nil || !created {
		t.Fatalf("other PR should create: created=%v err=%v", created, err)
	}

	// 在飞任务完成后允许再次直建（手工强制重审是合法用途）
	if err := s.FinishTask(ctx, first.ID, StatusDone, "comment", "report", "{}", "", 0, 0); err != nil {
		t.Fatal(err)
	}
	if err := s.FinishTask(ctx, other.ID, StatusDone, "comment", "report", "{}", "", 0, 0); err != nil {
		t.Fatal(err)
	}
	again := &Task{
		ID: NewTaskID(), Kind: "requirement_compliance", Platform: "gitea",
		Owner: "o", Repo: "r", Number: "1", Status: StatusPending, CreatedAt: time.Now(),
	}
	if _, created, err = s.CreateTaskDirectIfIdle(ctx, again); err != nil || !created {
		t.Fatalf("re-create after done: created=%v err=%v", created, err)
	}
}

// v3.8.0 回归：FinishTaskFenced 归属 fencing——stale 重置后旧执行体（runner_id
// 不匹配或任务已易主）的终态写入被丢弃，新执行体的写入不受影响。
func TestFinishTaskFenced(t *testing.T) {
	s := openTestStore(t)
	s.RunnerID = "runner-a"
	ctx := context.Background()

	claim := func(id string) {
		if err := s.db.Model(&Task{}).Where("id = ?", id).
			Updates(map[string]any{"status": StatusRunning, "runner_id": s.RunnerID}).Error; err != nil {
			t.Fatal(err)
		}
	}
	t1 := mkTask("ft-1", nil, "sha-a")
	if err := s.CreateTask(ctx, t1); err != nil {
		t.Fatal(err)
	}
	claim("ft-1")
	fenced, err := s.FinishTaskFenced(ctx, "ft-1", StatusDone, "comment", "r", "{}", "", 5, 0)
	if err != nil || fenced {
		t.Fatalf("归属匹配应成功写入: fenced=%v err=%v", fenced, err)
	}

	// 模拟 stale 重置后被 runner-b 认领：旧执行体 runner-a 的写入必须被拒
	if err := s.db.Model(&Task{}).Where("id = ?", "ft-1").
		Updates(map[string]any{"status": StatusRunning, "runner_id": "runner-b"}).Error; err != nil {
		t.Fatal(err)
	}
	fenced, err = s.FinishTaskFenced(ctx, "ft-1", StatusFailed, "", "", "", "zombie", 1, 0)
	if err != nil || !fenced {
		t.Fatalf("僵尸写入应被 fencing 拒绝: fenced=%v err=%v", fenced, err)
	}
	got, _ := s.GetTask(ctx, "ft-1")
	if got.Status != StatusRunning || got.FailReason != "" {
		t.Fatalf("僵尸写入不得改动任务: %+v", got)
	}

	// RunnerID 未装配（合成路径）：跳过 fencing，直接可写
	s2 := openTestStore(t)
	t2 := mkTask("ft-2", nil, "sha-b")
	if err := s2.CreateTask(ctx, t2); err != nil {
		t.Fatal(err)
	}
	if err := s2.FinishTask(ctx, "ft-2", StatusDone, "comment", "r", "{}", "", 0, 0); err != nil {
		t.Fatalf("未装配 RunnerID 时应跳过 fencing: %v", err)
	}
}

// v3.8.0 回归：failed 退避重试——failed 时 attempt+1 并写 next_retry_at；
// ResetFailedForRetry 只重置 attempt 未超限且已到期的任务。
func TestFailedRetryBackoff(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	t1 := mkTask("fr-1", nil, "sha-a")
	if err := s.CreateTask(ctx, t1); err != nil {
		t.Fatal(err)
	}
	// 模拟首次失败（RunnerID 空 → 无 fencing，同合成路径）
	if err := s.FinishTask(ctx, "fr-1", StatusFailed, "", "", "", "boom", 0, 0); err != nil {
		t.Fatal(err)
	}
	got, _ := s.GetTask(ctx, "fr-1")
	if got.Attempt != 1 || got.NextRetryAt == nil {
		t.Fatalf("失败应记 attempt+1 与退避时间: %+v", got)
	}
	// 退避未到期：不重置
	if n, err := s.ResetFailedForRetry(ctx, 2, got.NextRetryAt.Add(-time.Second)); err != nil || n != 0 {
		t.Fatalf("未到期不得重置: n=%d err=%v", n, err)
	}
	// 到期：重置回 pending（attempt 保留）
	if n, err := s.ResetFailedForRetry(ctx, 2, got.NextRetryAt.Add(time.Second)); err != nil || n != 1 {
		t.Fatalf("到期应重置: n=%d err=%v", n, err)
	}
	got, _ = s.GetTask(ctx, "fr-1")
	if got.Status != StatusPending || got.Attempt != 1 {
		t.Fatalf("重置后 pending 且 attempt 保留: %+v", got)
	}
	// 超限：attempt 达到 maxRetries 后不再重置
	_ = s.db.Model(&Task{}).Where("id = ?", "fr-1").Update("attempt", 2).Error
	_ = s.db.Model(&Task{}).Where("id = ?", "fr-1").Updates(map[string]any{
		"status": StatusFailed, "next_retry_at": time.Now().Add(-time.Minute)}).Error
	if n, err := s.ResetFailedForRetry(ctx, 2, time.Now()); err != nil || n != 0 {
		t.Fatalf("超限不得重置: n=%d err=%v", n, err)
	}
}

// MarkReviewPosted + Task.ReviewPosted 回读（v3.36.0 poster 库标记短路的数据面）。
func TestMarkReviewPosted(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	tk := &Task{ID: "t-rp", Kind: "review", Platform: "gitea", Owner: "acme", Repo: "app", Number: "1", Status: "running"}
	if err := s.CreateTask(ctx, tk); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetTask(ctx, "t-rp")
	if err != nil || got.ReviewPosted {
		t.Fatalf("初始应为 false，err=%v posted=%v", err, got.ReviewPosted)
	}
	if err := s.MarkReviewPosted(ctx, "t-rp"); err != nil {
		t.Fatal(err)
	}
	got, err = s.GetTask(ctx, "t-rp")
	if err != nil || !got.ReviewPosted {
		t.Fatalf("标记后应为 true，err=%v posted=%v", err, got.ReviewPosted)
	}
}

// 优先级队列（v3.40.0）：priority 大值先认领，同优先级保持 FIFO。
func TestClaimNextPendingPriority(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	mk := func(id string, pri int, offset int) {
		tk := &Task{ID: id, Kind: "review", Platform: "gitea", Owner: "acme", Repo: "app",
			Number: id, // v6.8.0 在飞唯一约束（P1-03）：优先级测试各占一号
			Status: StatusPending, Priority: pri,
			CreatedAt: time.Now().Add(time.Duration(offset) * time.Second)}
		if err := s.CreateTask(ctx, tk); err != nil {
			t.Fatal(err)
		}
	}
	mk("t-low-poll", 0, 0)     // 更早创建
	mk("t-high-human", 10, 60) // 更晚创建但优先级高
	got, ok, err := s.ClaimNextPending(ctx)
	if err != nil || !ok {
		t.Fatalf("claim: ok=%v err=%v", ok, err)
	}
	if got.ID != "t-high-human" {
		t.Fatalf("高优先级应先认领，got %s", got.ID)
	}
}
