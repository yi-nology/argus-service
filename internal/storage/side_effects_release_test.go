// side_effects_release_test.go —— #64 强释协议存储面：dry-run 零写入清单、
// Unsafe+在飞转 uncertain（不得仅凭释放重试）、持久化审计逐笔落账。
package storage

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/yi-nology/goagent-kernel/sideeffect"
)

// Action 清单且不修改任何行（无审计行、资源行/动作行原样）。
func TestForceReleaseDryRunListsAffectedWithoutWrites(t *testing.T) {
	store := openTestStore(t)
	st := store.SideEffects()
	ctx := context.Background()
	clock := time.Date(2026, 2, 1, 0, 0, 0, 0, time.UTC)
	st.UseClock(func() time.Time { return clock })

	// Unsafe 在飞意图（dry-run 预估会转 uncertain 的那类）。资源闸独占：k-a
	// 先只放闸留意图，k-unsafe 再持闸——两行意图共存于同一资源。
	actA := sideeffect.Action{Key: "k-a", Resource: "res-d", Payload: []byte("payload-k-a"),
		RetrySafety: sideeffect.RetrySafetyUnknown}
	_, leaseA := seedLease(t, st, "k-a", "res-d", 10*time.Minute)
	if err := st.Release(ctx, actA, leaseA); err != nil {
		t.Fatalf("Release k-a: %v", err)
	}
	actU := sideeffect.Action{Key: "k-unsafe", Resource: "res-d", Payload: []byte("p"),
		RetrySafety: sideeffect.RetrySafetyUnsafe}
	outcome, _, _, err := st.Acquire(ctx, actU, 10*time.Minute)
	if err != nil || outcome != sideeffect.OutcomeApplied {
		t.Fatalf("Acquire unsafe outcome=%v err=%v", outcome, err)
	}
	// 干净基线：dry-run 前的行快照。
	var fenceBefore, fenceAfter sideEffectFenceRow
	if err := store.db.Where("resource = ?", "res-d").Take(&fenceBefore).Error; err != nil {
		t.Fatal(err)
	}
	var rowsBefore []sideEffectRow
	if err := store.db.Where("resource = ?", "res-d").Order("effect_key").Find(&rowsBefore).Error; err != nil {
		t.Fatal(err)
	}

	plan, err := st.admin().DryRunForceRelease(ctx, "res-d")
	if err != nil {
		t.Fatalf("DryRunForceRelease: %v", err)
	}
	if plan.Resource != "res-d" || plan.Holder == "" || plan.Fence == 0 {
		t.Fatalf("资源现场投影不符: %+v", plan)
	}
	if len(plan.Actions) != 2 {
		t.Fatalf("受影响清单 = %d 行, want 2: %+v", len(plan.Actions), plan.Actions)
	}
	// 按 Key 升序。
	if plan.Actions[0].Key != "k-a" || plan.Actions[1].Key != "k-unsafe" {
		t.Fatalf("清单排序/键不符: %+v", plan.Actions)
	}
	if plan.WouldMarkUncertain != 1 {
		t.Fatalf("WouldMarkUncertain = %d, want 1（仅 Unsafe 在飞行）", plan.WouldMarkUncertain)
	}
	if plan.Actions[1].Status != sideEffectStatusPending || plan.Actions[1].RetrySafety != int16(sideeffect.RetrySafetyUnsafe) {
		t.Fatalf("Unsafe 行投影不符: %+v", plan.Actions[1])
	}

	// 无写入证据：资源行与动作行逐列一致，且无审计行落库。
	if err := store.db.Where("resource = ?", "res-d").Take(&fenceAfter).Error; err != nil {
		t.Fatal(err)
	}
	// time.Time 跨驱动语义（wall/loc）不做 == 比较，逐字段比对。
	if fenceAfter.Holder != fenceBefore.Holder || fenceAfter.Fence != fenceBefore.Fence ||
		fenceAfter.UpdatedAt.Unix() != fenceBefore.UpdatedAt.Unix() ||
		(fenceAfter.LeaseUntil == nil) != (fenceBefore.LeaseUntil == nil) ||
		(fenceAfter.LeaseUntil != nil && fenceBefore.LeaseUntil != nil &&
			fenceAfter.LeaseUntil.Unix() != fenceBefore.LeaseUntil.Unix()) {
		t.Fatalf("dry-run 改动了资源行: %+v -> %+v", fenceBefore, fenceAfter)
	}
	var rowsAfter []sideEffectRow
	if err := store.db.Where("resource = ?", "res-d").Order("effect_key").Find(&rowsAfter).Error; err != nil {
		t.Fatal(err)
	}
	if len(rowsAfter) != len(rowsBefore) {
		t.Fatalf("dry-run 改动了动作行数: %d -> %d", len(rowsBefore), len(rowsAfter))
	}
	// time.Time 的 == / DeepEqual 跨驱动有 wall/loc 陷阱，改 JSON 序列化比对
	// （RFC3339 确定性输出；行数与逐行内容都不变才证明零写入）。
	jb, mErr := json.Marshal(rowsBefore)
	if mErr != nil {
		t.Fatal(mErr)
	}
	ja, jErr := json.Marshal(rowsAfter)
	if jErr != nil {
		t.Fatal(jErr)
	}
	if string(jb) != string(ja) {
		t.Fatalf("dry-run 改动了动作行:\n before=%s\n after=%s", jb, ja)
	}
	var auditCount int64
	if err := store.db.Model(&SideEffectReleaseAudit{}).Count(&auditCount).Error; err != nil {
		t.Fatal(err)
	}
	if auditCount != 0 {
		t.Fatalf("dry-run 不应落审计行, got %d", auditCount)
	}

	// 不存在的资源：ErrNotFound。
	if _, err := st.admin().DryRunForceRelease(ctx, "res-missing"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("DryRunForceRelease(missing) err=%v, want ErrNotFound", err)
	}
}

// TestForceReleaseMarksUnsafeInFlightUncertain #64 验收：强制释放后 Unsafe 且
// 可能仍在执行的 Action 不会仅因租约释放而被重新执行——转入 uncertain 等下游
// 事实核验（resolver 自动对账）或人工 force_retryable；Safe 行释放后照旧可重试。
func TestForceReleaseMarksUnsafeInFlightUncertain(t *testing.T) {
	store := openTestStore(t)
	st := store.SideEffects()
	ctx := context.Background()

	// Unsafe 在飞（模拟旧执行者外部请求在途）：push 型动作。
	actUnsafe := sideeffect.Action{Key: "fix:push:t1:abc:branch", Resource: "res-u",
		Payload: []byte("p"), RetrySafety: sideeffect.RetrySafetyUnsafe}
	outcome, leaseUnsafe, _, err := st.Acquire(ctx, actUnsafe, 10*time.Minute)
	if err != nil || outcome != sideeffect.OutcomeApplied {
		t.Fatalf("Acquire unsafe outcome=%v err=%v", outcome, err)
	}
	// 资源闸独占：Unsafe 只放闸留意图（旧执行者外部请求仍可能在途），Safe 再持闸。
	if err := st.Release(ctx, actUnsafe, leaseUnsafe); err != nil {
		t.Fatalf("Release unsafe: %v", err)
	}
	actSafe := sideeffect.Action{Key: "k-safe", Resource: "res-u",
		Payload: []byte("p"), RetrySafety: sideeffect.RetrySafetySafe}
	if outcome, _, _, err = st.Acquire(ctx, actSafe, 10*time.Minute); err != nil || outcome != sideeffect.OutcomeApplied {
		t.Fatalf("Acquire safe outcome=%v err=%v", outcome, err)
	}

	res, err := st.admin().ForceReleaseResource(ctx, "res-u", ForceReleaseInput{Reason: "ops"})
	if err != nil {
		t.Fatalf("ForceReleaseResource: %v", err)
	}
	if res.ClearedActions != 2 || res.MarkedUncertain != 1 {
		t.Fatalf("result = %+v, want cleared=2 marked=1", res)
	}

	// Unsafe 行：uncertain + 留痕理由；再 Acquire 被「结果未知」挡住（不得重试）。
	var unsafeRow sideEffectRow
	if err := store.db.Where("effect_key = ?", "fix:push:t1:abc:branch").Take(&unsafeRow).Error; err != nil {
		t.Fatal(err)
	}
	if unsafeRow.Status != sideEffectStatusUncertain {
		t.Fatalf("Unsafe 在飞行 status = %s, want uncertain", unsafeRow.Status)
	}
	if unsafeRow.Reason != "forced release: ops" {
		t.Fatalf("reason = %q, want forced release 留痕", unsafeRow.Reason)
	}
	if unsafeRow.Holder != nil || unsafeRow.Fence != nil {
		t.Fatalf("Unsafe 行执行意图未清: %+v", unsafeRow)
	}
	outcome, _, _, err = st.Acquire(ctx, actUnsafe, time.Minute)
	if err != nil || outcome != sideeffect.OutcomeUncertain {
		t.Fatalf("Acquire after release outcome=%v err=%v, want uncertain（不得仅凭释放重试）", outcome, err)
	}

	// 对账路径仍然闭环：确认未生效 + 显式签字 → pending（重新开门）。
	if _, _, err := st.ResolveUncertain(ctx, actUnsafe, sideeffect.Resolution{
		Applied: false, ForceRetryable: true, Reason: "ls-remote 无该分支",
	}); err != nil {
		t.Fatalf("ResolveUncertain: %v", err)
	}
	outcomeUnsafe, leaseUnsafe2, _, aerr := st.Acquire(ctx, actUnsafe, time.Minute)
	if aerr != nil || outcomeUnsafe != sideeffect.OutcomeApplied {
		t.Fatalf("Acquire after reconcile outcome=%v err=%v, want applied", outcomeUnsafe, aerr)
	}
	if err := st.Release(ctx, actUnsafe, leaseUnsafe2); err != nil {
		t.Fatalf("Release unsafe after reconcile: %v", aerr)
	}

	// Safe 行：status 仍 pending，释放后可重试（语义不变）。
	var safeRow sideEffectRow
	if err := store.db.Where("effect_key = ?", "k-safe").Take(&safeRow).Error; err != nil {
		t.Fatal(err)
	}
	if safeRow.Status != sideEffectStatusPending {
		t.Fatalf("Safe 行 status = %s, want pending", safeRow.Status)
	}
	if outcome, _, _, err = st.Acquire(ctx, actSafe, time.Minute); err != nil || outcome != sideeffect.OutcomeApplied {
		t.Fatalf("Safe re-Acquire outcome=%v err=%v, want applied", outcome, err)
	}
}

// TestForceReleaseAuditRowsRecorded #64 验收：释放操作有持久化审计（多次释放
// 逐行落账，查询面可回看）。
func TestForceReleaseAuditRowsRecorded(t *testing.T) {
	store := openTestStore(t)
	st := store.SideEffects()
	ctx := context.Background()

	seedLease(t, st, "k1", "res-audit", 10*time.Minute)
	if _, err := st.admin().ForceReleaseResource(ctx, "res-audit", ForceReleaseInput{
		OperatorFP: "fp-op1", Reason: "第一次",
	}); err != nil {
		t.Fatal(err)
	}
	// 重复释放（资源已空闲）：0 行受影响，审计仍落（操作事实成立）。
	res, err := st.admin().ForceReleaseResource(ctx, "res-audit", ForceReleaseInput{
		OperatorFP: "fp-op2", Reason: "第二次",
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.ClearedActions != 0 || res.AuditID == 0 {
		t.Fatalf("second release = %+v, want cleared=0 audit 落行", res)
	}

	var rows []SideEffectReleaseAudit
	if err := store.db.Order("id ASC").Find(&rows).Error; err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 {
		t.Fatalf("审计行 = %d, want 2", len(rows))
	}
	if rows[0].OperatorFP != "fp-op1" || rows[0].Reason != "第一次" || rows[0].ClearedActions != 1 {
		t.Errorf("第一笔审计不符: %+v", rows[0])
	}
	if rows[1].OperatorFP != "fp-op2" || rows[1].Reason != "第二次" || rows[1].ClearedActions != 0 {
		t.Errorf("第二笔审计不符: %+v", rows[1])
	}
}
