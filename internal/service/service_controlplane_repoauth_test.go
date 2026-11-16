// service_controlplane_repoauth_test.go —— #60 跨仓拒绝矩阵（service 直调路径）：
// repo_admin（admin@pattern）仅约束内仓库可见/可操作；system_admin（无约束 admin）
// 全局。资源身份从库记录解析（pr:/branch: 形态），不可解析资源仅 system_admin
// 可见。e2e 的 HTTP 路由路径见 test/e2e/authz_repo_matrix_test.go。
package service

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/yi-nology/goagent-kernel/sideeffect"

	argus "github.com/yi-nology/argus/biz/model/argus"
	"github.com/yi-nology/argus/internal/effect"
	"github.com/yi-nology/argus/internal/storage"
)

// repoAdminCtx admin 档 + 仓库约束（repo_admin）。
func repoAdminCtx(pattern string) context.Context {
	return ContextWithTokenRepos(ContextWithScope(context.Background(), "admin"), pattern)
}

func seedRunningTask(t *testing.T, svc *Service, platform, owner, repo, number string) *storage.Task {
	t.Helper()
	task := storage.NewTask("code_review", platform, owner, repo, number)
	task.Status = storage.StatusRunning
	if err := svc.Store.CreateTask(context.Background(), task); err != nil {
		t.Fatal(err)
	}
	return task
}

func activeLease(t *testing.T, svc *Service, resource string) {
	t.Helper()
	act := sideeffect.Action{Key: sideeffect.Key("lease-" + resource), Resource: resource,
		Payload: []byte("p"), RetrySafety: sideeffect.RetrySafetyUnsafe}
	outcome, _, _, err := svc.Store.SideEffects().Acquire(context.Background(), act, time.Minute)
	if err != nil || outcome != sideeffect.OutcomeApplied {
		t.Fatalf("Acquire(%s) outcome=%v err=%v", resource, outcome, err)
	}
}

// admin@repo-A 取消 repo-B 任务：403，任务状态与取消标记原样不动。
func TestControlPlaneCancelCrossRepoDenied(t *testing.T) {
	svc, st := newControlPlaneSvc(t)
	mine := seedRunningTask(t, svc, "gitea", "myorg", "app", "1")
	theirs := seedRunningTask(t, svc, "gitea", "other", "secret", "2")
	ctx := repoAdminCtx("gitea:myorg/*")

	_, code := svc.CancelTask(ctx, &argus.ControlPlaneCancelTaskRequest{TaskID: theirs.ID})
	if code != http.StatusForbidden {
		t.Fatalf("跨仓取消 = %d, want 403", code)
	}
	got, err := st.GetTask(context.Background(), theirs.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != storage.StatusRunning || got.CancelRequestedAt != nil {
		t.Fatalf("跨仓拒绝不得动任务状态: status=%q cancel_requested_at=%v", got.Status, got.CancelRequestedAt)
	}

	// 约束内正常取消
	_, code = svc.CancelTask(ctx, &argus.ControlPlaneCancelTaskRequest{TaskID: mine.ID})
	if code != http.StatusOK {
		t.Fatalf("约束内取消 = %d, want 200", code)
	}
	// system_admin 全局
	third := seedRunningTask(t, svc, "gitea", "other", "secret", "3")
	_, code = svc.CancelTask(adminCtx(), &argus.ControlPlaneCancelTaskRequest{TaskID: third.ID})
	if code != http.StatusOK {
		t.Fatalf("system_admin 跨仓取消 = %d, want 200", code)
	}
}

// 列表按约束过滤：repo-B 行与不可解析资源行对 repo_admin 不可见。
func TestControlPlaneListFilteredByRepoConstraint(t *testing.T) {
	svc, _ := newControlPlaneSvc(t)
	seedUncertainAction(t, svc, "k-mine", "branch:gitea:myorg/app:argus/fix/1", sideeffect.RetrySafetySafe)
	seedUncertainAction(t, svc, "k-theirs", "branch:gitea:other/secret:argus/fix/2", sideeffect.RetrySafetySafe)
	seedUncertainAction(t, svc, "k-legacy", "res-applied", sideeffect.RetrySafetySafe)

	resp, code := svc.ListUncertainEffects(repoAdminCtx("gitea:myorg/*"))
	if code != http.StatusOK {
		t.Fatalf("list = %d, want 200", code)
	}
	for _, e := range resp.Effects {
		if e.Key == "k-theirs" || e.Key == "k-legacy" {
			t.Fatalf("repo_admin 不应看到约束外/不可解析行: %s", e.Key)
		}
	}
	if len(resp.Effects) != 1 || resp.Effects[0].Key != "k-mine" {
		t.Fatalf("repo_admin 应只见约束内行: %+v", resp.Effects)
	}
	// system_admin 全量（含不可解析）
	resp, code = svc.ListUncertainEffects(adminCtx())
	if code != http.StatusOK || len(resp.Effects) != 3 {
		t.Fatalf("system_admin 应见全部: code=%d n=%d", code, len(resp.Effects))
	}
}

// 裁决：约束外 Key 403；不可解析资源 403（repo_admin）/200（system_admin）。
func TestControlPlaneResolveCrossRepoDenied(t *testing.T) {
	svc, _ := newControlPlaneSvc(t)
	seedUncertainAction(t, svc, "k-mine", "pr:gitea:myorg/app#1", sideeffect.RetrySafetySafe)
	seedUncertainAction(t, svc, "k-theirs", "pr:gitea:other/secret#2", sideeffect.RetrySafetySafe)
	seedUncertainAction(t, svc, "k-legacy", "res-applied", sideeffect.RetrySafetySafe)
	ctx := repoAdminCtx("gitea:myorg/*")

	if _, code := svc.ResolveUncertainEffect(ctx, &argus.ControlPlaneResolveRequest{Key: "k-theirs"}); code != http.StatusForbidden {
		t.Fatalf("跨仓裁决 = %d, want 403", code)
	}
	if _, code := svc.ResolveUncertainEffect(ctx, &argus.ControlPlaneResolveRequest{Key: "k-legacy"}); code != http.StatusForbidden {
		t.Fatalf("repo_admin 裁决不可解析资源 = %d, want 403", code)
	}
	if _, code := svc.ResolveUncertainEffect(ctx, &argus.ControlPlaneResolveRequest{
		Key: "k-mine", Applied: false}); code != http.StatusOK {
		t.Fatalf("约束内裁决 = %d, want 200", code)
	}
	if _, code := svc.ResolveUncertainEffect(adminCtx(), &argus.ControlPlaneResolveRequest{
		Key: "k-legacy", Applied: false}); code != http.StatusOK {
		t.Fatalf("system_admin 裁决不可解析资源 = %d, want 200", code)
	}
}

// 强释：约束外资源 403 且租约原样；约束内资源 200；租约清单行过滤。
func TestControlPlaneLeaseCrossRepoDenied(t *testing.T) {
	svc, st := newControlPlaneSvc(t)
	activeLease(t, svc, "branch:gitea:myorg/app:argus/fix/1")
	activeLease(t, svc, "branch:gitea:other/secret:argus/fix/2")
	ctx := repoAdminCtx("gitea:myorg/*")

	resp, code := svc.ListLeases(ctx)
	if code != http.StatusOK || len(resp.Leases) != 1 ||
		resp.Leases[0].Resource != "branch:gitea:myorg/app:argus/fix/1" {
		t.Fatalf("repo_admin 租约清单应只见约束内: code=%d leases=%+v", code, resp.Leases)
	}

	if _, code := svc.ReleaseLease(ctx, &argus.ControlPlaneReleaseLeaseRequest{
		Resource: "branch:gitea:other/secret:argus/fix/2"}); code != http.StatusForbidden {
		t.Fatalf("跨仓强释 = %d, want 403", code)
	}
	// 租约原样未被释放
	row, err := st.SideEffects().GetLeaseByResourceForAuthz(context.Background(), "branch:gitea:other/secret:argus/fix/2")
	if err != nil || row.Holder == "" {
		t.Fatalf("跨仓拒绝不得释放租约: row=%+v err=%v", row, err)
	}

	if _, code := svc.ReleaseLease(ctx, &argus.ControlPlaneReleaseLeaseRequest{
		Resource: "branch:gitea:myorg/app:argus/fix/1"}); code != http.StatusOK {
		t.Fatalf("约束内强释 = %d, want 200", code)
	}
}

// 资源身份解析器与 runner 编码器单源对账（防两处格式漂移）——service 面用
// effect.ParseResourceRepo，编码器在 runner（service 不 import runner，此处
// 只钉 service 侧消费语义；编码器回环钉在 runner 测试）。
func TestParseResourceRepoConsumed(t *testing.T) {
	p, o, r, ok := effect.ParseResourceRepo("branch:gitea:myorg/app:argus/fix/42")
	if !ok || p != "gitea" || o != "myorg" || r != "app" {
		t.Fatalf("ParseResourceRepo = %q %q %q %v", p, o, r, ok)
	}
}
