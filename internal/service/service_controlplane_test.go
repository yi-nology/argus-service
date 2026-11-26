// service_controlplane_test.go —— 控制面 API 的 service 面（#48）：admin 二次
// 判定 fail-closed、裁决两条语义（applied 落库可重放 / unsafe 未生效需签字）、
// 坏输入 400、租约清单/强制释放、任务终止 404/409/幂等。
package service

import (
	"context"
	"net/http"
	"path/filepath"
	"testing"
	"time"

	oagentprogress "github.com/oagentkit/runtime/progress"
	"github.com/yi-nology/goagent-kernel/sideeffect"

	argus "github.com/yi-nology/argus/biz/model/argus"
	"github.com/yi-nology/argus/internal/config"
	argusprogress "github.com/yi-nology/argus/internal/progress"
	"github.com/yi-nology/argus/internal/storage"
)

func newControlPlaneSvc(t *testing.T) (*Service, *storage.Store) {
	t.Helper()
	st, err := storage.Open(filepath.Join(t.TempDir(), "controlplane.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return &Service{Store: st, Cfg: &config.Config{}, Log: testLogger{}}, st
}

func adminCtx() context.Context { return ContextWithScope(context.Background(), "admin") }

func strptr(s string) *string { return &s }

// seedUncertainAction 造一条 uncertain 记录（Acquire → MarkUncertain → Release 资源闸）。
func seedUncertainAction(t *testing.T, svc *Service, key, resource string, safety sideeffect.RetrySafety) sideeffect.Action {
	t.Helper()
	act := sideeffect.Action{
		Key: sideeffect.Key(key), Resource: resource,
		Payload: []byte("payload-" + key), RetrySafety: safety,
	}
	ctx := context.Background()
	outcome, lease, _, err := svc.Store.SideEffects().Acquire(ctx, act, time.Minute)
	if err != nil || outcome != sideeffect.OutcomeApplied {
		t.Fatalf("Acquire(%s) outcome=%v err=%v", key, outcome, err)
	}
	if err := svc.Store.SideEffects().MarkUncertain(ctx, act, lease, "boom-"+key); err != nil {
		t.Fatalf("MarkUncertain(%s): %v", key, err)
	}
	if err := svc.Store.SideEffects().Release(ctx, act, lease); err != nil {
		t.Fatalf("Release(%s): %v", key, err)
	}
	return act
}

func TestControlPlaneRequiresAdmin(t *testing.T) {
	svc, _ := newControlPlaneSvc(t)
	calls := []struct {
		name string
		call func(context.Context) int
	}{
		{"list_effects", func(ctx context.Context) int { _, c := svc.ListUncertainEffects(ctx); return c }},
		{"resolve", func(ctx context.Context) int {
			_, c := svc.ResolveUncertainEffect(ctx, &argus.ControlPlaneResolveRequest{Key: "k"})
			return c
		}},
		{"list_leases", func(ctx context.Context) int { _, c := svc.ListLeases(ctx); return c }},
		{"release_lease", func(ctx context.Context) int {
			_, c := svc.ReleaseLease(ctx, &argus.ControlPlaneReleaseLeaseRequest{Resource: "r"})
			return c
		}},
		{"cancel_task", func(ctx context.Context) int {
			_, c := svc.CancelTask(ctx, &argus.ControlPlaneCancelTaskRequest{TaskID: "t"})
			return c
		}},
	}
	for _, tc := range calls {
		for _, scope := range []string{"", "read", "ci"} {
			ctx := context.Background()
			if scope != "" {
				ctx = ContextWithScope(ctx, scope)
			}
			if got := tc.call(ctx); got != http.StatusForbidden {
				t.Errorf("%s scope=%q = %d, want 403", tc.name, scope, got)
			}
		}
	}
}

func TestResolveUncertainEffectAppliedReplays(t *testing.T) {
	svc, _ := newControlPlaneSvc(t)
	act := seedUncertainAction(t, svc, "k-applied", "res-applied", sideeffect.RetrySafetyUnsafe)
	payload := `{"comment_id":42}`

	resp, code := svc.ResolveUncertainEffect(adminCtx(), &argus.ControlPlaneResolveRequest{
		Key: "k-applied", Applied: true, Payload: &payload,
	})
	if code != http.StatusOK {
		t.Fatalf("resolve applied = %d, want 200", code)
	}
	if resp.Outcome != "replayed" {
		t.Errorf("outcome = %q, want replayed（已生效裁决写 done 并返回结果）", resp.Outcome)
	}
	if resp.Payload == nil || *resp.Payload != payload {
		t.Errorf("payload = %v, want %q", resp.Payload, payload)
	}

	// 再 Acquire 同一动作：done 事实重放，payload 落库。
	outcome, _, res, err := svc.Store.SideEffects().Acquire(context.Background(), act, time.Minute)
	if err != nil || outcome != sideeffect.OutcomeReplayed {
		t.Fatalf("Acquire after resolve outcome=%v err=%v, want replayed", outcome, err)
	}
	if string(res.Payload) != payload {
		t.Errorf("replayed payload = %s, want %s", res.Payload, payload)
	}
}

func TestResolveUncertainEffectUnsafeNeedsSignature(t *testing.T) {
	svc, _ := newControlPlaneSvc(t)
	act := seedUncertainAction(t, svc, "k-unsafe", "res-unsafe", sideeffect.RetrySafetyUnsafe)

	// 未生效 + 无 force：Unsafe 动作不解除阻塞 → 409。
	if _, code := svc.ResolveUncertainEffect(adminCtx(), &argus.ControlPlaneResolveRequest{
		Key: "k-unsafe", Applied: false,
	}); code != http.StatusConflict {
		t.Fatalf("unsafe not-applied without force = %d, want 409", code)
	}

	// 显式签字后放行 → pending（Acquire 可再次授予）。
	resp, code := svc.ResolveUncertainEffect(adminCtx(), &argus.ControlPlaneResolveRequest{
		Key: "k-unsafe", Applied: false, ForceRetryable: true, Reason: strptr("人工确认未生效"),
	})
	if code != http.StatusOK || resp.Outcome != "applied" {
		t.Fatalf("force resolve = %+v code=%d, want outcome=applied 200", resp, code)
	}
	outcome, _, _, err := svc.Store.SideEffects().Acquire(context.Background(), act, time.Minute)
	if err != nil || outcome != sideeffect.OutcomeApplied {
		t.Fatalf("Acquire after force outcome=%v err=%v, want applied", outcome, err)
	}
}

func TestResolveUncertainEffectBadInput(t *testing.T) {
	svc, _ := newControlPlaneSvc(t)
	badJSON := `{"comment_id":`
	cases := []struct {
		name string
		req  *argus.ControlPlaneResolveRequest
	}{
		{"nil_request", nil},
		{"empty_key", &argus.ControlPlaneResolveRequest{Key: "", Applied: true}},
		{"key_too_long", &argus.ControlPlaneResolveRequest{Key: string(make([]byte, 256)), Applied: true}},
		{"bad_json", &argus.ControlPlaneResolveRequest{Key: "k", Applied: true, Payload: &badJSON}},
	}
	for _, tc := range cases {
		if _, code := svc.ResolveUncertainEffect(adminCtx(), tc.req); code != http.StatusBadRequest {
			t.Errorf("%s = %d, want 400", tc.name, code)
		}
	}
}

func TestControlPlaneListEffectsAndLeases(t *testing.T) {
	svc, _ := newControlPlaneSvc(t)
	seedUncertainAction(t, svc, "k-u1", "res-u1", sideeffect.RetrySafetyUnsafe)
	seedUncertainAction(t, svc, "k-u2", "res-u2", sideeffect.RetrySafetySafe)

	effects, code := svc.ListUncertainEffects(adminCtx())
	if code != http.StatusOK {
		t.Fatalf("ListUncertainEffects = %d, want 200", code)
	}
	if len(effects.Effects) != 2 {
		t.Fatalf("effects = %+v, want 2", effects.Effects)
	}
	byKey := map[string]*argus.ControlPlaneEffect{}
	for _, e := range effects.Effects {
		byKey[e.Key] = e
	}
	if e := byKey["k-u1"]; e == nil || e.Resource != "res-u1" ||
		e.RetrySafety != int32(sideeffect.RetrySafetyUnsafe) || e.UpdatedAt == 0 {
		t.Errorf("k-u1 投影错: %+v", e)
	}

	// 活跃租约：一条持有中的动作 → 清单可见，过期/空闲不可见（存储层已测）。
	act := sideeffect.Action{Key: "k-live", Resource: "res-live", Payload: []byte("p")}
	outcome, _, _, err := svc.Store.SideEffects().Acquire(context.Background(), act, 10*time.Minute)
	if err != nil || outcome != sideeffect.OutcomeApplied {
		t.Fatalf("Acquire live outcome=%v err=%v", outcome, err)
	}
	leases, code := svc.ListLeases(adminCtx())
	if code != http.StatusOK {
		t.Fatalf("ListLeases = %d, want 200", code)
	}
	if len(leases.Leases) != 1 {
		t.Fatalf("leases = %+v, want 1", leases.Leases)
	}
	if l := leases.Leases[0]; l.Resource != "res-live" || l.Holder == "" || l.Fence != 1 || l.ExpiresAt == 0 {
		t.Errorf("lease 投影错: %+v", l)
	}

	// 强制释放（#64 协议：confirm=true + reason）：清意图后同一动作可重新 Acquire。
	rel, code := svc.ReleaseLease(adminCtx(), &argus.ControlPlaneReleaseLeaseRequest{
		Resource: "res-live", Reason: strptr("持有者失联"), Confirm: true,
	})
	if code != http.StatusOK || rel.ClearedActions != 1 || rel.AuditID == 0 {
		t.Fatalf("ReleaseLease = %+v code=%d, want cleared=1 audit>0 200", rel, code)
	}
	if leases, code = svc.ListLeases(adminCtx()); code != http.StatusOK || len(leases.Leases) != 0 {
		t.Fatalf("leases after release = %+v code=%d, want empty", leases, code)
	}
	if outcome, _, _, err = svc.Store.SideEffects().Acquire(context.Background(), act, time.Minute); err != nil || outcome != sideeffect.OutcomeApplied {
		t.Fatalf("Acquire after release outcome=%v err=%v, want applied", outcome, err)
	}

	// 不存在 → 404；空 resource → 400。
	if _, code := svc.ReleaseLease(adminCtx(), &argus.ControlPlaneReleaseLeaseRequest{Resource: "res-missing", Confirm: true, Reason: strptr("x")}); code != http.StatusNotFound {
		t.Errorf("release missing = %d, want 404", code)
	}
	if _, code := svc.ReleaseLease(adminCtx(), &argus.ControlPlaneReleaseLeaseRequest{}); code != http.StatusBadRequest {
		t.Errorf("release empty resource = %d, want 400", code)
	}
	if _, code := svc.ReleaseLease(adminCtx(), nil); code != http.StatusBadRequest {
		t.Errorf("release nil request = %d, want 400", code)
	}
}

// TestControlPlaneReleaseLeaseProtocol #64 两段式协议的 API 面：
//   - dry_run=true：返回受影响 Action 清单（dry-run 优先于 confirm/reason 校验），
//     不修改任何行、不落审计；
//   - 正式执行缺 confirm（二次确认）或缺 reason（操作理由）→ 400；
//   - 正式执行：审计行持久化（operator 指纹 + 理由）。
func TestControlPlaneReleaseLeaseProtocol(t *testing.T) {
	svc, st := newControlPlaneSvc(t)
	act := sideeffect.Action{Key: "k-proto", Resource: "res-proto",
		Payload: []byte("p"), RetrySafety: sideeffect.RetrySafetyUnsafe}
	ctx := context.Background()
	if _, _, _, err := svc.Store.SideEffects().Acquire(ctx, act, 10*time.Minute); err != nil {
		t.Fatal(err)
	}

	// dry-run：清单含受影响 Key，行原样（holder 仍在、无审计行）。
	resp, code := svc.ReleaseLease(adminCtx(), &argus.ControlPlaneReleaseLeaseRequest{
		Resource: "res-proto", DryRun: true})
	if code != http.StatusOK || resp.Plan == nil {
		t.Fatalf("dry_run = %d plan=%+v, want 200 with plan", code, resp.Plan)
	}
	if len(resp.Plan.Actions) != 1 || resp.Plan.Actions[0].Key != "k-proto" ||
		resp.Plan.Actions[0].Status != "pending" {
		t.Fatalf("dry_run 清单不符: %+v", resp.Plan.Actions)
	}
	if resp.Plan.WouldMarkUncertain != 1 {
		t.Fatalf("WouldMarkUncertain = %d, want 1（Unsafe 在飞）", resp.Plan.WouldMarkUncertain)
	}
	if resp.Plan.Holder == "" || resp.Plan.Fence == 0 || resp.Plan.ExpiresAt == 0 {
		t.Fatalf("dry_run 资源现场投影不符: %+v", resp.Plan)
	}
	row, err := st.SideEffectAdmin().GetLeaseByResourceForAuthz(ctx, "res-proto")
	if err != nil || row.Holder == "" {
		t.Fatalf("dry_run 不得释放租约: row=%+v err=%v", row, err)
	}

	// 正式执行缺二次确认 → 400（dry_run=false、confirm=false）。
	if _, code := svc.ReleaseLease(adminCtx(), &argus.ControlPlaneReleaseLeaseRequest{
		Resource: "res-proto", Reason: strptr("有理由但未确认")}); code != http.StatusBadRequest {
		t.Errorf("release without confirm = %d, want 400", code)
	}
	// 正式执行缺操作理由 → 400。
	if _, code := svc.ReleaseLease(adminCtx(), &argus.ControlPlaneReleaseLeaseRequest{
		Resource: "res-proto", Confirm: true}); code != http.StatusBadRequest {
		t.Errorf("release without reason = %d, want 400", code)
	}
	// 二次确认 + 理由 → 200；AuditID>0 即审计行已持久化（行内容与释放事实
	// 同事务，逐列断言在 storage 面测试）。
	rel, code := svc.ReleaseLease(ContextWithTokenFP(adminCtx(), "fp-audit99"), &argus.ControlPlaneReleaseLeaseRequest{
		Resource: "res-proto", Confirm: true, Reason: strptr("生产事故处置")})
	if code != http.StatusOK || rel.MarkedUncertain != 1 || rel.AuditID == 0 || rel.ClearedActions != 1 {
		t.Fatalf("confirmed release = %+v code=%d, want marked=1 audit>0 200", rel, code)
	}
	// 释放后 Unsafe 在飞动作不得重新开门（等对账/人工 force_retryable）。
	outcome, _, _, aerr := svc.Store.SideEffects().Acquire(ctx, act, time.Minute)
	if aerr != nil || outcome != sideeffect.OutcomeUncertain {
		t.Fatalf("Acquire after release outcome=%v err=%v, want uncertain", outcome, aerr)
	}
}

func TestControlPlaneCancelTask(t *testing.T) {
	svc, st := newControlPlaneSvc(t)
	ctx := context.Background()
	newTask := func(status string) *storage.Task {
		task := storage.NewTask("code_review", "gitea", "o", "r", "1")
		if err := st.CreateTask(ctx, task); err != nil {
			t.Fatal(err)
		}
		if status != storage.StatusPending {
			if err := st.FinishTask(ctx, task.ID, status, "", "", "", "", 0, 0); err != nil {
				t.Fatal(err)
			}
		}
		return task
	}

	pending := newTask(storage.StatusPending)
	resp, code := svc.CancelTask(adminCtx(), &argus.ControlPlaneCancelTaskRequest{TaskID: pending.ID, Reason: strptr("事故")})
	if code != http.StatusOK || !resp.Cancelled || resp.Status != storage.StatusFailed || resp.TaskID != pending.ID {
		t.Fatalf("cancel pending = %+v code=%d", resp, code)
	}
	// 幂等：重复终止返回 cancelled=false。
	resp, code = svc.CancelTask(adminCtx(), &argus.ControlPlaneCancelTaskRequest{TaskID: pending.ID})
	if code != http.StatusOK || resp.Cancelled {
		t.Fatalf("second cancel = %+v code=%d, want cancelled=false 200", resp, code)
	}

	done := newTask(storage.StatusDone)
	if _, code := svc.CancelTask(adminCtx(), &argus.ControlPlaneCancelTaskRequest{TaskID: done.ID}); code != http.StatusConflict {
		t.Errorf("cancel done = %d, want 409", code)
	}
	if _, code := svc.CancelTask(adminCtx(), &argus.ControlPlaneCancelTaskRequest{TaskID: "t-missing"}); code != http.StatusNotFound {
		t.Errorf("cancel missing = %d, want 404", code)
	}
	if _, code := svc.CancelTask(adminCtx(), &argus.ControlPlaneCancelTaskRequest{}); code != http.StatusBadRequest {
		t.Errorf("cancel empty id = %d, want 400", code)
	}
	if _, code := svc.CancelTask(adminCtx(), nil); code != http.StatusBadRequest {
		t.Errorf("cancel nil request = %d, want 400", code)
	}
}

// 控制面绕过 runner 的 finishTask 漏斗：必须自行补 task.finished SSE 事件，
// 否则 /events 客户端在操作员终止后看不到终态。
func TestControlPlaneCancelPublishesFinishedEvent(t *testing.T) {
	svc, st := newControlPlaneSvc(t)
	svc.Bus = oagentprogress.NewBus[argusprogress.Event]()
	subCtx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ch, unsub := svc.Bus.Subscribe(subCtx)
	defer unsub()

	task := storage.NewTask("code_review", "gitea", "o", "r", "7")
	if err := st.CreateTask(context.Background(), task); err != nil {
		t.Fatal(err)
	}
	if _, code := svc.CancelTask(adminCtx(), &argus.ControlPlaneCancelTaskRequest{TaskID: task.ID}); code != http.StatusOK {
		t.Fatalf("cancel code=%d", code)
	}
	select {
	case ev := <-ch:
		if ev.Type != argusprogress.EventTaskFinished || ev.TaskID != task.ID ||
			ev.Status != storage.StatusFailed {
			t.Fatalf("终态事件不符: %+v", ev)
		}
	case <-time.After(time.Second):
		t.Fatal("未收到 task.finished 事件")
	}
}
