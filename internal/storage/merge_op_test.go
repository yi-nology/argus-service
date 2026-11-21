// merge_op_test.go —— merge_operations 存储面：建查迁移 CAS、阻断发现计数、
// 当前 head 审查任务收窄。
package storage

import (
	"context"
	"path/filepath"
	"testing"
	"time"
)

func openMergeStore(t *testing.T) *Store {
	t.Helper()
	st, err := Open(filepath.Join(t.TempDir(), "merge.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st
}

func TestMergeOpCreateGetTransition(t *testing.T) {
	st := openMergeStore(t)
	ctx := context.Background()

	op := &MergeOp{
		Platform: "gitea", Owner: "acme", Repo: "app", Number: "7",
		ExpectedRemoteHead: "abc123",
		GateJSON:           `{"eligible":true}`,
	}
	if err := st.CreateMergeOp(ctx, op); err != nil {
		t.Fatalf("create: %v", err)
	}
	if op.ID == "" || op.EffectKey == "" || op.Status != MergeOpPending {
		t.Fatalf("factory fields: id=%q key=%q status=%q", op.ID, op.EffectKey, op.Status)
	}
	if op.EffectKey != MergeOpEffectKey(op.ID) {
		t.Fatalf("effect key mismatch: %q", op.EffectKey)
	}

	got, err := st.GetMergeOp(ctx, op.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.ExpectedRemoteHead != "abc123" {
		t.Fatalf("head: %q", got.ExpectedRemoteHead)
	}

	// CAS：pending → merged 成功；再迁出失败。
	ok, err := st.TransitionMergeOp(ctx, op.ID, MergeOpPending, MergeOpMerged, "done", `{"state":"merged"}`)
	if err != nil || !ok {
		t.Fatalf("transition: ok=%v err=%v", ok, err)
	}
	ok, err = st.TransitionMergeOp(ctx, op.ID, MergeOpPending, MergeOpFailed, "x", "")
	if err != nil || ok {
		t.Fatalf("terminal should not transition: ok=%v err=%v", ok, err)
	}
	got, _ = st.GetMergeOp(ctx, op.ID)
	if got.Status != MergeOpMerged || got.FinishedAt == nil {
		t.Fatalf("merged state: status=%q finished=%v", got.Status, got.FinishedAt)
	}
}

func TestMergeOpListPendingAndExpire(t *testing.T) {
	st := openMergeStore(t)
	ctx := context.Background()
	op := &MergeOp{Platform: "gitea", Owner: "o", Repo: "r", Number: "1", ExpectedRemoteHead: "h"}
	if err := st.CreateMergeOp(ctx, op); err != nil {
		t.Fatal(err)
	}
	list, err := st.ListPendingMergeOpsByPR(ctx, "gitea", "o", "r", "1")
	if err != nil || len(list) != 1 {
		t.Fatalf("pending list: n=%d err=%v", len(list), err)
	}
	if err := st.db.Exec(`UPDATE merge_operations SET created_at = ? WHERE id = ?`,
		time.Now().Add(-2*time.Hour), op.ID).Error; err != nil {
		t.Fatal(err)
	}
	n, err := st.ExpireStalePendingMergeOps(ctx, time.Hour)
	if err != nil || n != 1 {
		t.Fatalf("expire: n=%d err=%v", n, err)
	}
	got, _ := st.GetMergeOp(ctx, op.ID)
	if got.Status != MergeOpExpired {
		t.Fatalf("status=%q want expired", got.Status)
	}
}

func TestCountBlockingFindingsScopedByTask(t *testing.T) {
	st := openMergeStore(t)
	ctx := context.Background()
	base := FindingPrint{
		Platform: "gitea", Owner: "o", Repo: "r", Number: "9",
		Print: "fpdeadbeef", Severity: "high", Status: PrintOpen,
		LastTaskID: "t-current",
	}
	if err := st.db.Create(&base).Error; err != nil {
		t.Fatal(err)
	}
	old := FindingPrint{
		Platform: "gitea", Owner: "o", Repo: "r", Number: "9",
		Print: "fpcafebabe", Severity: "critical", Status: PrintOpen,
		LastTaskID: "t-old",
	}
	if err := st.db.Create(&old).Error; err != nil {
		t.Fatal(err)
	}
	sup := FindingPrint{
		Platform: "gitea", Owner: "o", Repo: "r", Number: "9",
		Print: "fpsuppress1", Severity: "high", Status: PrintSuppressed,
		LastTaskID: "t-current",
	}
	if err := st.db.Create(&sup).Error; err != nil {
		t.Fatal(err)
	}

	n, err := st.CountBlockingFindings(ctx, "gitea", "o", "r", "9", nil)
	if err != nil || n != 2 {
		t.Fatalf("all: n=%d err=%v", n, err)
	}
	n, err = st.CountBlockingFindings(ctx, "gitea", "o", "r", "9", []string{"t-current"})
	if err != nil || n != 1 {
		t.Fatalf("scoped: n=%d err=%v", n, err)
	}
}

func TestLatestReviewTaskForHead(t *testing.T) {
	st := openMergeStore(t)
	ctx := context.Background()

	if err := st.CreateTask(ctx, &Task{
		ID: "t-desc", Kind: "describe", Platform: "gitea", Owner: "o", Repo: "r",
		Number: "3", HeadSHA: "h1", Status: StatusDone, Verdict: "approve",
		CreatedAt: time.Now(),
	}); err != nil {
		t.Fatal(err)
	}
	if err := st.CreateTask(ctx, &Task{
		ID: "t-run", Kind: "code_review", Platform: "gitea", Owner: "o", Repo: "r",
		Number: "3", HeadSHA: "h1", Status: StatusRunning,
		CreatedAt: time.Now(),
	}); err != nil {
		t.Fatal(err)
	}
	fin := time.Now()
	if err := st.CreateTask(ctx, &Task{
		ID: "t-done", Kind: "requirement_compliance", Platform: "gitea", Owner: "o", Repo: "r",
		Number: "3", HeadSHA: "h1", Status: StatusDone, Verdict: "request_changes",
		FinishedAt: &fin, CreatedAt: time.Now(),
	}); err != nil {
		t.Fatal(err)
	}

	got, err := st.LatestReviewTaskForHead(ctx, "gitea", "o", "r", "3", "h1")
	if err != nil || got == nil || got.ID != "t-done" {
		t.Fatalf("latest: got=%v err=%v", got, err)
	}
	got, err = st.LatestReviewTaskForHead(ctx, "gitea", "o", "r", "3", "other")
	if err != nil || got != nil {
		t.Fatalf("mismatch should nil: got=%v err=%v", got, err)
	}

	ids, err := st.ReviewTaskIDsForHead(ctx, "gitea", "o", "r", "3", "h1")
	if err != nil || len(ids) != 2 {
		t.Fatalf("ids: %v err=%v", ids, err)
	}
}
