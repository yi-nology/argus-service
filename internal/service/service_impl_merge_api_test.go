// service_impl_merge_api_test.go —— mergeConfirm 门禁前状态机回归钉。
//
// mergeConfirm 是全系统最高危写路径（合并不可逆），其防线（binding 绑定、
// 终态幂等、裁定权、仓库维度授权）此前仅靠 43 实弹验证过一次，无自动化
// 回归。本文件只覆盖 evaluateMergeGate 之前的状态机；门后 effect/TOCTOU
// 段依赖具体类型 *argusctx.SDKCtx，待接口化后另批补。
package service

import (
	"context"
	"net/http"
	"path/filepath"
	"strings"
	"testing"

	argus "github.com/yi-nology/argus/biz/model/argus"
	"github.com/yi-nology/argus/internal/config"
	"github.com/yi-nology/argus/internal/storage"
)

func newMergeConfirmSvc(t *testing.T) *Service {
	t.Helper()
	st, err := storage.Open(filepath.Join(t.TempDir(), "merge-confirm.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return &Service{
		Store: st,
		Cfg:   &config.Config{},
		Log:   testLogger{},
	}
}

// seedOp 落一条指定状态/绑定 head 的 MergeOp，返回 opID。
func seedOp(t *testing.T, s *Service, status, head string) string {
	t.Helper()
	op := &storage.MergeOp{
		Platform:           "github",
		Owner:              "acme",
		Repo:               "app",
		Number:             "42",
		ExpectedRemoteHead: head,
		Status:             status,
	}
	if err := s.Store.CreateMergeOp(context.Background(), op); err != nil {
		t.Fatal(err)
	}
	return op.ID
}

func confirmReq(opID, expected string) *argus.MergeConfirmRequest {
	return &argus.MergeConfirmRequest{OpID: opID, ExpectedRemoteHead: expected}
}

func TestMergeConfirmRejectsBadInput(t *testing.T) {
	s := newMergeConfirmSvc(t)
	ctx := ContextWithScope(context.Background(), "admin")

	for _, tc := range []struct {
		name, opID, expected string
	}{
		{"empty opID", "", "abc"},
		{"empty expected", "op-1", ""},
		{"both empty", "", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			resp, code := mergeConfirm(s, ctx, confirmReq(tc.opID, tc.expected))
			if code != http.StatusBadRequest || resp != nil {
				t.Fatalf("code=%d resp=%v want 400/nil", code, resp)
			}
		})
	}
}

func TestMergeConfirmAuthzMatrix(t *testing.T) {
	s := newMergeConfirmSvc(t)
	const head = "deadbeef"
	opID := seedOp(t, s, storage.MergeOpPending, head)
	req := confirmReq(opID, head)

	t.Run("read scope denied", func(t *testing.T) {
		ctx := ContextWithScope(context.Background(), "read")
		if _, code := mergeConfirm(s, ctx, req); code != http.StatusForbidden {
			t.Fatalf("code=%d want 403", code)
		}
	})

	// 刻意设计：admin 会话（RoleAdmin）无裁定权——mergeAdmin 要求会话角色
	// 为 maintainer；静态超管 token（无会话）放行。此钉防回归成「admin 也行」。
	t.Run("admin session denied", func(t *testing.T) {
		ctx := ContextWithSession(ContextWithScope(context.Background(), "admin"), "root", storage.RoleAdmin)
		if _, code := mergeConfirm(s, ctx, req); code != http.StatusForbidden {
			t.Fatalf("code=%d want 403", code)
		}
	})

	t.Run("maintainer session passes gate check", func(t *testing.T) {
		ctx := ContextWithSession(ContextWithScope(context.Background(), "admin"), "m", storage.RoleMaintainer)
		// maintainer 过 mergeAdmin 后进入状态机；SDK 未装配 → 资格门失败
		// → 200 rejected（非 403），证明裁定权检查已放行。
		resp, code := mergeConfirm(s, ctx, req)
		if code != http.StatusOK {
			t.Fatalf("code=%d want 200", code)
		}
		if resp.GetStatus() != storage.MergeOpRejected {
			t.Fatalf("status=%q want rejected", resp.GetStatus())
		}
	})

	// 仓库维度授权从 op 记录解析，不信请求侧坐标：token 仓库约束外 → 403。
	t.Run("token repo constraint denied", func(t *testing.T) {
		ctx := ContextWithTokenRepos(ContextWithScope(context.Background(), "admin"), "other:*")
		if _, code := mergeConfirm(s, ctx, req); code != http.StatusForbidden {
			t.Fatalf("code=%d want 403", code)
		}
	})
}

func TestMergeConfirmOpNotFound(t *testing.T) {
	s := newMergeConfirmSvc(t)
	ctx := ContextWithScope(context.Background(), "admin")
	if _, code := mergeConfirm(s, ctx, confirmReq("no-such-op", "abc")); code != http.StatusNotFound {
		t.Fatalf("code=%d want 404", code)
	}
}

func TestMergeConfirmBindingMismatch(t *testing.T) {
	s := newMergeConfirmSvc(t)
	ctx := ContextWithScope(context.Background(), "admin")
	opID := seedOp(t, s, storage.MergeOpPending, "bound-head")

	resp, code := mergeConfirm(s, ctx, confirmReq(opID, "stale-head"))
	if code != http.StatusOK {
		t.Fatalf("code=%d want 200", code)
	}
	if resp.GetStatus() != storage.MergeOpRejected {
		t.Fatalf("status=%q want rejected", resp.GetStatus())
	}
	if msg := resp.GetMessage(); !strings.Contains(msg, "不一致") {
		t.Fatalf("message=%q want 不一致", msg)
	}
	// 绑定拒绝不落终态：op 仍 pending（可换正确 head 重试）。
	op, err := s.Store.GetMergeOp(ctx, opID)
	if err != nil {
		t.Fatal(err)
	}
	if op.Status != storage.MergeOpPending {
		t.Fatalf("op status=%q want pending", op.Status)
	}
}

func TestMergeConfirmTerminalIdempotency(t *testing.T) {
	s := newMergeConfirmSvc(t)
	ctx := ContextWithScope(context.Background(), "admin")
	const head = "cafebabe"

	cases := []struct {
		status, wantStatus string
		wantMsg            string
	}{
		{storage.MergeOpMerged, storage.MergeOpMerged, "already merged"},
		{storage.MergeOpFailed, storage.MergeOpRejected, "候选已终态"},
		{storage.MergeOpExpired, storage.MergeOpRejected, "候选已终态"},
		{storage.MergeOpRejected, storage.MergeOpRejected, "候选已终态"},
		{storage.MergeOpUncertain, storage.MergeOpUncertain, "reconcile"},
	}
	for _, tc := range cases {
		t.Run(tc.status, func(t *testing.T) {
			opID := seedOp(t, s, tc.status, head)
			resp, code := mergeConfirm(s, ctx, confirmReq(opID, head))
			if code != http.StatusOK {
				t.Fatalf("code=%d want 200", code)
			}
			if resp.GetStatus() != tc.wantStatus {
				t.Fatalf("status=%q want %q", resp.GetStatus(), tc.wantStatus)
			}
			if !strings.Contains(resp.GetMessage(), tc.wantMsg) {
				t.Fatalf("message=%q want contains %q", resp.GetMessage(), tc.wantMsg)
			}
			// 终态不被 confirm 改写：幂等回放/拒绝，store 状态保持。
			op, err := s.Store.GetMergeOp(ctx, opID)
			if err != nil {
				t.Fatal(err)
			}
			if op.Status != tc.status {
				t.Fatalf("store status=%q want %q", op.Status, tc.status)
			}
		})
	}
}

// TestMergeConfirmGateFailMarksRejected：pending + binding 正确 + SDK 未装配
// → 资格门 cr_open 失败 → 200 rejected 且 op 从 pending 落 rejected
// （不可再 confirm，透出机器码）。
func TestMergeConfirmGateFailMarksRejected(t *testing.T) {
	s := newMergeConfirmSvc(t)
	ctx := ContextWithScope(context.Background(), "admin")
	const head = "feedface"
	opID := seedOp(t, s, storage.MergeOpPending, head)

	resp, code := mergeConfirm(s, ctx, confirmReq(opID, head))
	if code != http.StatusOK {
		t.Fatalf("code=%d want 200", code)
	}
	if resp.GetStatus() != storage.MergeOpRejected {
		t.Fatalf("status=%q want rejected", resp.GetStatus())
	}
	if msg := resp.GetMessage(); !strings.Contains(msg, "cr_open") {
		t.Fatalf("message=%q want cr_open 机器码", msg)
	}
	op, err := s.Store.GetMergeOp(ctx, opID)
	if err != nil {
		t.Fatal(err)
	}
	if op.Status != storage.MergeOpRejected {
		t.Fatalf("store status=%q want rejected", op.Status)
	}
}
