// side_effects_payload_test.go —— payload 身份载荷列（收编 adjudicate 前置）：
// 载荷字节入库/取回/重建身份全量比对、异载荷同 Key 拒绝、补列迁移幂等。
package storage

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/oagentkit/goagent-kernel/sideeffect"
)

// TestSideEffectPayloadRoundTrip 载荷字节 Acquire 入库 → ListUncertain 取回 →
// 重建完整 Action 过 ensureIdentity（对账按完整身份裁决的输入面）；异载荷同
// Key 必须撞 ErrActionConflict（有字节即全量比对，不放水）。
func TestSideEffectPayloadRoundTrip(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	st := s.SideEffects()
	act := sideeffect.Action{
		Key: sideeffect.Key("k-payload"), Resource: "res:p",
		Payload:     []byte("identity:branch->head"),
		RetrySafety: sideeffect.RetrySafetyUnsafe,
	}
	outcome, lease, _, err := st.Acquire(ctx, act, time.Minute)
	if err != nil || outcome != sideeffect.OutcomeApplied {
		t.Fatalf("Acquire outcome=%v err=%v", outcome, err)
	}
	if err := st.MarkUncertain(ctx, act, lease, "boom"); err != nil {
		t.Fatal(err)
	}

	// 投影取回：payload 字节原样在场。cutoff 取「未来」（与控制面
	// service_impl_controlplane 同款）——刚标的行 updated_at=now，grace
	// 语义的 now-1h 会把它滤掉；且必须 UTC（CI TZ=UTC 与本地 UTC+8
	// 混比结果曾相反，本地侥幸绿掩盖了 cutoff 语义错）。
	rows, err := s.SideEffectAdmin().ListUncertain(ctx, time.Now().UTC().Add(time.Second), 10)
	if err != nil {
		t.Fatal(err)
	}
	var row *UncertainSideEffect
	for i := range rows {
		if rows[i].Key == "k-payload" {
			row = &rows[i]
		}
	}
	if row == nil {
		t.Fatalf("ListUncertain 未返回 k-payload: %+v", rows)
	}
	if string(row.Payload) != string(act.Payload) {
		t.Fatalf("载荷字节未往返: got %q want %q", row.Payload, act.Payload)
	}

	// 重建完整身份 → 裁决通过（哈希与登记一致）。Unsafe 动作的「未生效」
	// 需 ForceRetryable 签字（kernel 语义），此处以已生效裁决验证身份面。
	rebuilt := sideeffect.Action{
		Key: sideeffect.Key(row.Key), Resource: row.Resource,
		Payload: row.Payload, RetrySafety: sideeffect.RetrySafety(row.RetrySafety),
	}
	if _, _, err := st.ResolveUncertain(ctx, rebuilt,
		sideeffect.Resolution{Applied: true, Result: sideeffect.Result{Payload: []byte(`{"id":1}`)}}); err != nil {
		t.Fatalf("重建身份裁决应通过: %v", err)
	}

	// 异载荷同 Key：ensureIdentity 全量比对拒绝（行已转 pending，身份校验仍在
	// lockAction 先于状态判定执行）。
	bad := rebuilt
	bad.Payload = []byte("different-content")
	if _, _, err := st.ResolveUncertain(ctx, bad, sideeffect.Resolution{Applied: false}); !errors.Is(err, sideeffect.ErrActionConflict) {
		t.Fatalf("异载荷同 Key 应 ErrActionConflict, got %v", err)
	}
}

// TestMigrateSideEffectPayloadIdempotent 补列迁移：历史库无列 → ALTER 建列
// （幂等，二次跳过）→ 新动作载荷可写。
func TestMigrateSideEffectPayloadIdempotent(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	// 模拟历史库形态：删掉 payload 列（SQLite ≥3.35 / PG / MySQL 8 均支持）。
	if err := s.db.WithContext(ctx).Exec(`ALTER TABLE side_effects DROP COLUMN payload`).Error; err != nil {
		t.Skipf("方言不支持 DROP COLUMN: %v", err)
	}
	if s.db.Migrator().HasColumn(&sideEffectRow{}, "payload") {
		t.Fatal("删列后 HasColumn 应为 false")
	}
	if _, err := s.migrateSideEffectPayload(ctx); err != nil {
		t.Fatalf("补列迁移: %v", err)
	}
	if !s.db.Migrator().HasColumn(&sideEffectRow{}, "payload") {
		t.Fatal("迁移后 payload 列应在")
	}
	// 二次运行幂等跳过（已有列不再 ALTER）。
	if _, err := s.migrateSideEffectPayload(ctx); err != nil {
		t.Fatalf("二次迁移应幂等: %v", err)
	}
	// 补列后新动作的载荷可写可读。
	st := s.SideEffects()
	act := sideeffect.Action{Key: "k-after-migrate", Resource: "res:m",
		Payload: []byte("p"), RetrySafety: sideeffect.RetrySafetySafe}
	if outcome, _, _, err := st.Acquire(ctx, act, time.Minute); err != nil || outcome != sideeffect.OutcomeApplied {
		t.Fatalf("补列后 Acquire outcome=%v err=%v", outcome, err)
	}
}
