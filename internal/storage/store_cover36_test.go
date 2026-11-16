// store_cover36_test.go —— #36 kernel 60/70 storage 数据面补测（行为钉住）：
// 任务查询族（ListTasks/ListTasksByPR/FindReportsByHead/GetLastReviewedDiff/
// LatestDoneTask/窄写口）、review target 显式清零、指纹解析与聚合清单、模式
// 信号读侧/合并/清理、预留聚合、用量事件明细、三类 Prune 分批删除。
package storage

import (
	"context"
	"testing"
	"time"

	"github.com/yi-nology/argus/internal/domain"
	"github.com/yi-nology/argus/internal/learning"
)

func mkTask36(t *testing.T, s *Store, id string, mut func(*Task)) {
	t.Helper()
	tk := &Task{
		ID: id, Kind: "code_review", Platform: "gitea",
		Owner: "acme", Repo: "app", Number: "7",
		HeadSHA: "sha-" + id, Status: StatusPending, CreatedAt: time.Now().UTC(),
	}
	if mut != nil {
		mut(tk)
	}
	if err := s.CreateTask(context.Background(), tk); err != nil {
		t.Fatal(err)
	}
}

// ---------- 任务查询族与窄写口 ----------

func TestTaskQueries36(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()

	// 窄写口三连：FixStatus / HeadSHA / Debug 各只动一列。
	mkTask36(t, s, "t-1", nil)
	if err := s.UpdateTaskFixStatus(ctx, "t-1", "pushed"); err != nil {
		t.Fatal(err)
	}
	if err := s.SetTaskHeadSHA(ctx, "t-1", "sha-new"); err != nil {
		t.Fatal(err)
	}
	if err := s.SetTaskDebug(ctx, "t-1", `{"k":"v"}`); err != nil {
		t.Fatal(err)
	}
	tk, err := s.GetTask(ctx, "t-1")
	if err != nil {
		t.Fatal(err)
	}
	if tk.FixStatus != "pushed" || tk.HeadSHA != "sha-new" || tk.DebugJSON != `{"k":"v"}` {
		t.Fatalf("窄写口落库失真: %+v", tk)
	}

	// ListTasks：created_at 倒序 + status 过滤 + limit 收口（0→20 缺省、>100→100）。
	mkTask36(t, s, "t-2", func(x *Task) { x.Status = StatusRunning })
	mkTask36(t, s, "t-3", func(x *Task) { x.Status = StatusDone })
	all, err := s.ListTasks(ctx, "", 0)
	if err != nil || len(all) != 3 {
		t.Fatalf("ListTasks 全量失真: n=%d err=%v", len(all), err)
	}
	if all[0].ID != "t-3" {
		t.Fatalf("应按创建倒序（同秒内后建在前）: %+v", all[0])
	}
	running, err := s.ListTasks(ctx, StatusRunning, 999)
	if err != nil || len(running) != 1 || running[0].ID != "t-2" {
		t.Fatalf("status 过滤失真: %+v err=%v", running, err)
	}
	two, _ := s.ListTasks(ctx, "", 2)
	if len(two) != 2 {
		t.Fatalf("limit 收口失真: %d", len(two))
	}

	// ListTasksByPR：只含本 PR（其他仓任务不串），limit 缺省 5。
	mkTask36(t, s, "t-4", func(x *Task) { x.Repo = "other" })
	byPR, err := s.ListTasksByPR(ctx, "gitea", "acme", "app", "7", 0)
	if err != nil || len(byPR) != 3 {
		t.Fatalf("ListTasksByPR 失真: n=%d err=%v", len(byPR), err)
	}

	// FindReportsByHead：只取 done 且有报告；无记录返回空切片非 error。
	if err := s.FinishTask(ctx, "t-3", StatusDone, "approve", "## 报告", "", "", 0, 0); err != nil {
		t.Fatal(err)
	}
	rs, err := s.FindReportsByHead(ctx, "sha-t-3", 0)
	if err != nil || len(rs) != 1 || rs[0].ID != "t-3" {
		t.Fatalf("FindReportsByHead 失真: %+v err=%v", rs, err)
	}
	if none, err := s.FindReportsByHead(ctx, "sha-none", 0); err != nil || len(none) != 0 {
		t.Fatalf("无报告应空切片: %+v err=%v", none, err)
	}

	// GetLastReviewedDiff：done + result_json.diff_text 提取；无前置记录空串；
	// 坏 JSON 不阻塞（空串）。
	if err := s.FinishTask(ctx, "t-3", StatusDone, "approve", "## 报告",
		`{"diff_text":"diff --git a/x.go"}`, "", 0, 0); err != nil {
		t.Fatal(err)
	}
	diff, err := s.GetLastReviewedDiff(ctx, "gitea", "acme", "app", "7")
	if err != nil || diff != "diff --git a/x.go" {
		t.Fatalf("GetLastReviewedDiff 失真: %q %v", diff, err)
	}
	if d, err := s.GetLastReviewedDiff(ctx, "gitea", "acme", "other", "7"); err != nil || d != "" {
		t.Fatalf("无前置应空串: %q %v", d, err)
	}
	mkTask36(t, s, "t-5", func(x *Task) {
		x.Status = StatusDone
		x.ResultJSON = "{not-json"
	})
	fin := time.Now().UTC().Add(time.Minute) // 比 t-3 新
	if err := s.db.Model(&Task{}).Where("id = ?", "t-5").
		Updates(map[string]any{"finished_at": fin}).Error; err != nil {
		t.Fatal(err)
	}
	if d, err := s.GetLastReviewedDiff(ctx, "gitea", "acme", "app", "7"); err != nil || d != "" {
		t.Fatalf("坏 result_json 应空串: %q %v", d, err)
	}
}

// LatestDoneTask：miss → (nil, nil)；命中取 finished_at 最新。
func TestLatestDoneTask36(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	if tk, err := s.LatestDoneTask(ctx, "gitea", "acme", "app", "7"); err != nil || tk != nil {
		t.Fatalf("无记录应 (nil,nil): %+v %v", tk, err)
	}
	mkTask36(t, s, "d-1", nil)
	old := time.Now().UTC().Add(-time.Hour)
	if err := s.FinishTask(ctx, "d-1", StatusDone, "approve", "r1", "", "", 0, 0); err != nil {
		t.Fatal(err)
	}
	if err := s.db.Model(&Task{}).Where("id = ?", "d-1").Update("finished_at", old).Error; err != nil {
		t.Fatal(err)
	}
	mkTask36(t, s, "d-2", nil)
	if err := s.FinishTask(ctx, "d-2", StatusDone, "request_changes", "r2", "", "", 0, 0); err != nil {
		t.Fatal(err)
	}
	tk, err := s.LatestDoneTask(ctx, "gitea", "acme", "app", "7")
	if err != nil || tk == nil || tk.ID != "d-2" || tk.Verdict != "request_changes" {
		t.Fatalf("应取最新 done: %+v %v", tk, err)
	}
}

// ---------- review target：显式清零与查询 ----------

func TestReviewTargetClear36(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	if rt, err := s.GetReviewTarget(ctx, "gitea", "acme", "app", "7"); err != nil || rt != nil {
		t.Fatalf("无行应 (nil,nil): %+v %v", rt, err)
	}
	if err := s.UpsertReviewTarget(ctx, "gitea", "acme", "app", "7", 42, "sha-a"); err != nil {
		t.Fatal(err)
	}
	rt, err := s.GetReviewTarget(ctx, "gitea", "acme", "app", "7")
	if err != nil || rt == nil || rt.LastSummaryCommentID != 42 || rt.LastHeadSHA != "sha-a" {
		t.Fatalf("Upsert 后查询失真: %+v %v", rt, err)
	}
	// 评论 404 自愈：清零定位、head SHA 不动（同 SHA 去重语义保留）。
	if err := s.ClearReviewTargetComment(ctx, "gitea", "acme", "app", "7"); err != nil {
		t.Fatal(err)
	}
	rt2, _ := s.GetReviewTarget(ctx, "gitea", "acme", "app", "7")
	if rt2.LastSummaryCommentID != 0 || rt2.LastHeadSHA != "sha-a" {
		t.Fatalf("清零语义失真: %+v", rt2)
	}
	// 行不存在时清零 = no-op 不报错。
	if err := s.ClearReviewTargetComment(ctx, "gitea", "acme", "app", "99"); err != nil {
		t.Fatalf("无行清零应 no-op: %v", err)
	}
}

// ---------- 指纹：短码解析 / regret 建行 / 聚合清单 / 清理 ----------

const fpX36 = "abcdef1234567890abcdef1234567890abcdef1234567890abcdef1234567890"

func TestPrintResolveAndAggregates36(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()

	// 无记录：短码解析 miss。
	if _, ok := s.ResolvePrintFingerprint(ctx, "gitea", "acme", "app", "7", fpX36[:8]); ok {
		t.Fatal("无记录应 miss")
	}
	// RecordPrintRegret 行不存在 → 以 finding 原始内容建行（regret=1, open）。
	f := domain.Finding{File: "a.go", Line: 3, Severity: "high", Comment: "未检查错误", By: "security"}
	if err := s.RecordPrintRegret(ctx, "gitea", "acme", "app", "7", fpX36, f); err != nil {
		t.Fatal(err)
	}
	fp, pluginName, ok := s.ResolvePrintWithPlugin(ctx, "gitea", "acme", "app", "7", fpX36[:8])
	if !ok || fp != fpX36 || pluginName != "security" {
		t.Fatalf("短码解析失真: fp=%q plugin=%q ok=%v", fp, pluginName, ok)
	}
	// 无效前缀（短于 4 / 非 hex）：拒绝解析（LIKE 通配注入防线）。
	for _, bad := range []string{"ab", "xyz!", "gggg", ""} {
		if _, ok := s.ResolvePrintFingerprint(ctx, "gitea", "acme", "app", "7", bad); ok {
			t.Fatalf("非法前缀 %q 应拒绝", bad)
		}
	}
	// 再 regret 一次：已有行计数累加，不建新行。
	if err := s.RecordPrintRegret(ctx, "gitea", "acme", "app", "7", fpX36, f); err != nil {
		t.Fatal(err)
	}
	prints, err := s.ListAllPrints(ctx)
	if err != nil || len(prints) != 1 || prints[0].RegretCount != 2 {
		t.Fatalf("regret 累加失真: n=%d err=%v %+v", len(prints), err, prints)
	}

	// suppress 一条 → ListSuppressedPrints 只含 suppressed。
	if _, err := s.SuppressPrint(ctx, "gitea", "acme", "app", "7", "1234abcdef1234567890abcdef1234567890abcdef1234567890abcdef12345678",
		domain.Finding{File: "b.go", Comment: "误报"}, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	sup, err := s.ListSuppressedPrints(ctx, 0)
	if err != nil || len(sup) != 1 || sup[0].Status != PrintSuppressed {
		t.Fatalf("ListSuppressedPrints 失真: %+v err=%v", sup, err)
	}
	if lim, err := s.ListSuppressedPrints(ctx, 0); err != nil || len(lim) != 1 {
		t.Fatalf("limit<=0 应全量: %+v", lim)
	}

	// FindingHistoryByFiles：按文件聚合 total/suppressed；空入参空 map。
	hist, err := s.FindingHistoryByFiles(ctx, "gitea", "acme", "app", []string{"a.go", "b.go", "c.go"})
	if err != nil {
		t.Fatal(err)
	}
	if hist["a.go"].Total != 1 || hist["b.go"].Total != 1 || hist["b.go"].Suppressed != 1 {
		t.Fatalf("文件聚合失真: %+v", hist)
	}
	if _, hit := hist["c.go"]; hit {
		t.Fatalf("无意见文件不应出现: %+v", hist)
	}
	if empty, err := s.FindingHistoryByFiles(ctx, "gitea", "acme", "app", nil); err != nil || len(empty) != 0 {
		t.Fatalf("空入参应空 map: %+v %v", empty, err)
	}

	// PruneStalePrints：cutoff 未来 → 全清（含孤儿别名跟随）；cutoff 过去 → 零。
	// 注：RecordPrintRegret 建行用 time.Now()（本地时区），SQLite 方言按字符串
	// 比较时间列——统一把测试行归一到 UTC 后再验清理边界（大 margin 不踩时区差）。
	if err := s.db.Model(&FindingPrint{}).Where("1 = 1").
		Update("last_seen_at", time.Now().UTC()).Error; err != nil {
		t.Fatal(err)
	}
	if n, err := s.PruneStalePrints(ctx, time.Now().UTC().Add(-365*24*time.Hour)); err != nil || n != 0 {
		t.Fatalf("新鲜指纹不应清: n=%d err=%v", n, err)
	}
	if n, err := s.PruneStalePrints(ctx, time.Now().UTC().Add(365*24*time.Hour)); err != nil || n != 2 {
		t.Fatalf("过期指纹应清 2: n=%d err=%v", n, err)
	}
	if left, _ := s.ListAllPrints(ctx); len(left) != 0 {
		t.Fatalf("清后应空: %+v", left)
	}
}

// ---------- 模式信号：读侧投影 / 仓库清单 / 合并 / 清理 ----------

func TestPatternFaces36(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	at := time.Now().UTC()
	f := domain.Finding{File: "a.go", Line: 1, Severity: "low", Comment: "未检查 error 返回"}

	if err := s.PatternPosted(ctx, "gitea", "acme", "app", "security", f, at); err != nil {
		t.Fatal(err)
	}
	if err := s.PatternPosted(ctx, "gitea", "acme", "app", "security", f, at); err != nil {
		t.Fatal(err)
	}
	f2 := domain.Finding{File: "b.go", Line: 2, Severity: "low", Comment: "魔数硬编码"}
	if err := s.PatternPosted(ctx, "gitea", "acme", "other", "style", f2, at); err != nil {
		t.Fatal(err)
	}

	rows, err := s.ListRepoPatterns(ctx, "gitea", "acme", "app")
	if err != nil || len(rows) != 1 || rows[0].Posted != 2 {
		t.Fatalf("ListRepoPatterns 失真: %+v err=%v", rows, err)
	}
	repos, err := s.ListPatternRepos(ctx)
	if err != nil || len(repos) != 2 {
		t.Fatalf("ListPatternRepos 失真: %+v err=%v", repos, err)
	}
	states, err := s.ListRepoPatternStates(ctx, "gitea", "acme", "app")
	if err != nil || len(states) != 1 || states[0].Posted != 2 {
		t.Fatalf("ListRepoPatternStates 失真: %+v err=%v", states, err)
	}
	all, err := s.ListAllPatternSignals(ctx)
	if err != nil || len(all) != 2 {
		t.Fatalf("ListAllPatternSignals 失真: n=%d err=%v", len(all), err)
	}
	// 投影字段与 learning.PatternState 对齐（PatternKey 归一化键）。
	if states[0].Pattern != learning.PatternKey("security", f.Comment) {
		t.Fatalf("pattern 键投影失真: %+v", states[0])
	}

	// MergePatternInto：守卫（0/自并）no-op；合法合并计数并入 + dup 删除。
	var keep, dup PatternSignal
	if err := s.db.Where("repo = ? AND plugin = ?", "app", "security").First(&keep).Error; err != nil {
		t.Fatal(err)
	}
	if err := s.db.Where("repo = ?", "other").First(&dup).Error; err != nil {
		t.Fatal(err)
	}
	if err := s.MergePatternInto(ctx, "gitea", "acme", "app", 0, dup.ID, at); err != nil {
		t.Fatalf("keepID=0 应 no-op: %v", err)
	}
	if err := s.MergePatternInto(ctx, "gitea", "acme", "app", keep.ID, keep.ID, at); err != nil {
		t.Fatalf("自并应 no-op: %v", err)
	}
	// 合并同仓 keep+dup（再造一行同仓、不同键的 dup；注意 PatternKey 归一化
	// 会吞标点——评语必须实质不同才会生成新键新行）。
	f3 := domain.Finding{File: "c.go", Line: 3, Severity: "low", Comment: "危险并发写共享变量"}
	if err := s.PatternPosted(ctx, "gitea", "acme", "app", "security", f3, at); err != nil {
		t.Fatal(err)
	}
	var dup2 PatternSignal
	if err := s.db.Where("repo = ? AND plugin = ?", "app", "security").Order("id DESC").First(&dup2).Error; err != nil {
		t.Fatal(err)
	}
	if dup2.ID == keep.ID {
		t.Fatalf("dup 行应与 keep 不同键: key=%q", dup2.Pattern)
	}
	dup2.Regret = 3
	oldSeen := at.Add(-48 * time.Hour)
	dup2.LastSeenAt, dup2.FirstSeenAt = oldSeen, oldSeen
	if err := s.db.Save(&dup2).Error; err != nil {
		t.Fatal(err)
	}
	if err := s.MergePatternInto(ctx, "gitea", "acme", "app", keep.ID, dup2.ID, at); err != nil {
		t.Fatal(err)
	}
	var merged PatternSignal
	if err := s.db.First(&merged, keep.ID).Error; err != nil {
		t.Fatal(err)
	}
	if merged.Regret != 3 || merged.Posted != 3 {
		t.Fatalf("合并计数失真: posted=%d regret=%d", merged.Posted, merged.Regret)
	}
	if !merged.LastSeenAt.Equal(at) || !merged.FirstSeenAt.Equal(oldSeen) {
		t.Fatalf("合并时间应取 later/earlier: last=%v first=%v", merged.LastSeenAt, merged.FirstSeenAt)
	}
	if err := s.db.First(&dup2, dup2.ID).Error; err == nil {
		t.Fatal("dup 行应已删除")
	}

	// PruneStalePatterns：cutoff 未来清全（分批删除），cutoff 过去零删除。
	if n, err := s.PruneStalePatterns(ctx, at.Add(-365*24*time.Hour)); err != nil || n != 0 {
		t.Fatalf("新鲜模式不应清: n=%d err=%v", n, err)
	}
	if n, err := s.PruneStalePatterns(ctx, at.Add(365*24*time.Hour)); err != nil || n != 2 {
		t.Fatalf("过期模式应清 2: n=%d err=%v", n, err)
	}
}

// laterTime/earlierTime 单元（合并语义的时间取侧）。
func TestLaterEarlier36(t *testing.T) {
	a := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	b := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	if laterTime(a, b) != b || laterTime(b, a) != b || laterTime(a, a) != a {
		t.Fatal("laterTime 语义失真")
	}
	if earlierTime(a, b) != a || earlierTime(b, a) != a || earlierTime(b, b) != b {
		t.Fatal("earlierTime 语义失真")
	}
}

// ---------- 预留聚合与用量事件明细 ----------

func TestReservationAggregate36(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC()
	mk := func(taskID, repo string, tokens int, expires time.Time, status string) {
		t.Helper()
		if err := s.db.Create(&BudgetReservation{
			TaskID: taskID, Platform: "gitea", Owner: "acme", Repo: repo,
			Tokens: tokens, Status: status, ExpiresAt: expires,
		}).Error; err != nil {
			t.Fatal(err)
		}
	}
	// expires 取 ±1 年大 margin：SQLite 方言时间列为带时区字符串比较，
	// 小 margin 会被本地时区偏移翻转（生产方言为原生时间类型，无此问题）。
	mk("r-1", "app", 100, now.Add(365*24*time.Hour), ReservationReserved)
	mk("r-2", "app", 200, now.Add(365*24*time.Hour), ReservationReserved)
	mk("r-3", "web", 50, now.Add(365*24*time.Hour), ReservationReserved)
	mk("r-4", "app", 999, now.Add(-365*24*time.Hour), ReservationReserved) // 过期：不计入
	mk("r-5", "app", 500, now.Add(365*24*time.Hour), ReservationSettled)   // 已核销：不计入

	if total, err := s.RepoMonthlyReserved(ctx, "gitea", "acme", "app"); err != nil || total != 300 {
		t.Fatalf("RepoMonthlyReserved 失真: %d %v", total, err)
	}
	agg, err := s.MonthlyReservedByRepo(ctx)
	if err != nil || len(agg) != 2 {
		t.Fatalf("MonthlyReservedByRepo 失真: %+v err=%v", agg, err)
	}
	if agg[0].Repo != "app" || agg[0].Tokens != 300 || agg[0].Tasks != 2 {
		t.Fatalf("聚合行失真（tokens 降序）: %+v", agg[0])
	}
}

func TestTaskUsageEvents36(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	if _, err := s.TaskUsageEvents(ctx, "no-task"); err != nil {
		t.Fatal(err)
	}
	base := time.Now().UTC().Add(-time.Hour)
	for i, inv := range []string{"inv-a", "inv-b"} {
		if err := s.RecordUsageEvent(ctx, &UsageEvent{
			TaskID: "u-1", InvocationID: inv, Platform: "gitea", Owner: "acme", Repo: "app",
			InputTokens: 10 * (i + 1), OccurredAt: base.Add(time.Duration(i) * time.Minute),
		}); err != nil {
			t.Fatal(err)
		}
	}
	evs, err := s.TaskUsageEvents(ctx, "u-1")
	if err != nil || len(evs) != 2 {
		t.Fatalf("事件明细失真: n=%d err=%v", len(evs), err)
	}
	if !evs[0].OccurredAt.Before(evs[1].OccurredAt) || evs[1].InvocationID != "inv-b" {
		t.Fatalf("应按发生时间升序: %+v", evs)
	}
}

// ---------- QA 会话与投递账本的 Prune 分批删除 ----------

func TestPruneQASessionsAndDeliveries36(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC()

	if err := s.SaveQASession(ctx, &QASession{SessionKey: "k-fresh", HistoryJSON: "[]"}); err != nil {
		t.Fatal(err)
	}
	if err := s.db.Exec(`INSERT INTO qa_sessions (session_key, history_json, updated_at) VALUES ('k-old', '[]', '` +
		now.Add(-48*time.Hour).Format("2006-01-02 15:04:05") + `')`).Error; err != nil {
		t.Fatal(err)
	}
	if n, err := s.PruneQASessions(ctx, now.Add(-24*time.Hour)); err != nil || n != 1 {
		t.Fatalf("PruneQASessions 失真: n=%d err=%v", n, err)
	}
	if sess, _ := s.GetQASession(ctx, "k-fresh", time.Hour); sess == nil {
		t.Fatal("新鲜会话不应被清")
	}

	// 投递账本：新记录保留，过期记录分批清。
	if fresh, err := s.MarkCommentDelivery(ctx, "d-fresh", now); err != nil || !fresh {
		t.Fatalf("MarkCommentDelivery 失真: %v %v", fresh, err)
	}
	if _, err := s.MarkCommentDelivery(ctx, "d-old", now.Add(-8*24*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if n, err := s.PruneCommentDeliveries(ctx, now.Add(-24*time.Hour)); err != nil || n != 1 {
		t.Fatalf("PruneCommentDeliveries 失真: n=%d err=%v", n, err)
	}
	var left int64
	if err := s.db.Model(&CommentDelivery{}).Count(&left).Error; err != nil || left != 1 {
		t.Fatalf("清后应剩 1: %d %v", left, err)
	}
	// 空库再清：零删除不报错（空批次退出路径）。
	if n, err := s.PruneCommentDeliveries(ctx, now.Add(-24*time.Hour)); err != nil || n != 0 {
		t.Fatalf("空批次应零删除: n=%d err=%v", n, err)
	}
}

// EnsureInFlightGuard / checkInFlightGuard（SQLite 方言路径）：建防线索前后
// check 的 active 翻转——verify 模式「只校验不迁移」承诺的可观测行为。
func TestInFlightGuardSQLite36(t *testing.T) {
	s := openTestStore(t)
	// openTestStore 的 Open 默认建防线（EnsureInFlightGuard 在迁移租约内），
	// 此处只复核 check 只读面与 Ensure 幂等。
	active, err := s.checkInFlightGuard()
	if err != nil {
		t.Fatalf("SQLite 探测失败: %v", err)
	}
	if !active {
		t.Fatal("Open 后防线索应已建立")
	}
	if a2, err := s.EnsureInFlightGuard(); err != nil || !a2 {
		t.Fatalf("Ensure 幂等重建失真: %v %v", a2, err)
	}
	// 别名同语义。
	if a3, err := s.EnsureSameSHAGuard(); err != nil || !a3 {
		t.Fatalf("EnsureSameSHAGuard 别名失真: %v %v", a3, err)
	}
}
