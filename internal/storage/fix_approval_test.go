// fix_approval_test.go —— 审批票：Ensure 幂等/陈旧化、裁决 CAS、修复门。
package storage

import (
	"context"
	"path/filepath"
	"testing"
)

func openFixApprovalStore(t *testing.T) *Store {
	t.Helper()
	st, err := Open(filepath.Join(t.TempDir(), "fixap.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st
}

func TestEnsureFixApprovalsIdempotentAndStale(t *testing.T) {
	st := openFixApprovalStore(t)
	ctx := context.Background()

	seeds := []FixApprovalSeed{
		{Print: "fpaaaa1111", File: "a.go", Severity: "high", Comment: "x"},
		{Print: "fpbbbb2222", File: "b.go", Severity: "critical", Comment: "y"},
		{Print: "fpcccc3333", File: "c.go", Severity: "medium", Comment: "low-sev-skipped"},
	}
	created, pending, err := st.EnsureFixApprovals(ctx, "gitea", "o", "r", "1", "head1", "t1", seeds)
	if err != nil || created != 2 || pending != 2 {
		t.Fatalf("first ensure: created=%d pending=%d err=%v", created, pending, err)
	}
	// 幂等
	created, pending, err = st.EnsureFixApprovals(ctx, "gitea", "o", "r", "1", "head1", "t1", seeds)
	if err != nil || created != 0 || pending != 2 {
		t.Fatalf("second ensure: created=%d pending=%d err=%v", created, pending, err)
	}
	// 新 head：旧 pending → stale，新 head 建 2 张
	created, pending, err = st.EnsureFixApprovals(ctx, "gitea", "o", "r", "1", "head2", "t2", seeds)
	if err != nil || created != 2 || pending != 2 {
		t.Fatalf("new head: created=%d pending=%d err=%v", created, pending, err)
	}
	rows, _ := st.ListFixApprovals(ctx, "gitea", "o", "r", "1", "head1")
	if len(rows) != 2 || rows[0].Status != FixApprovalStale && rows[1].Status != FixApprovalStale {
		// both should be stale
		for _, r := range rows {
			if r.Status != FixApprovalStale {
				t.Fatalf("old head ticket status=%q want stale", r.Status)
			}
		}
	}
}

func TestDecideFixApprovalCAS(t *testing.T) {
	st := openFixApprovalStore(t)
	ctx := context.Background()
	_, _, err := st.EnsureFixApprovals(ctx, "gitea", "o", "r", "2", "h", "t", []FixApprovalSeed{
		{Print: "fpdddd4444", Severity: "high", File: "d.go"},
	})
	if err != nil {
		t.Fatal(err)
	}
	list, _ := st.ListFixApprovals(ctx, "gitea", "o", "r", "2", "h")
	if len(list) != 1 {
		t.Fatalf("want 1 ticket, got %d", len(list))
	}
	id := list[0].ID

	ok, err := st.DecideFixApproval(ctx, id, FixApprovalApproved, "fp0", "lgtm")
	if err != nil || !ok {
		t.Fatalf("approve: ok=%v err=%v", ok, err)
	}
	// 二次裁决拒绝
	ok, err = st.DecideFixApproval(ctx, id, FixApprovalRejected, "fp0", "x")
	if err != nil || ok {
		t.Fatalf("re-decide should fail: ok=%v err=%v", ok, err)
	}
	got, _ := st.GetFixApproval(ctx, id)
	if got.Status != FixApprovalApproved || got.DecidedByFP != "fp0" || got.DecidedAt == nil {
		t.Fatalf("approved row: %+v", got)
	}
}

func TestFixApprovalGate(t *testing.T) {
	st := openFixApprovalStore(t)
	ctx := context.Background()
	seeds := []FixApprovalSeed{
		{Print: "fp1111aaaa", Severity: "high", File: "a.go"},
		{Print: "fp2222bbbb", Severity: "critical", File: "b.go"},
	}
	if _, _, err := st.EnsureFixApprovals(ctx, "gitea", "o", "r", "3", "h", "t", seeds); err != nil {
		t.Fatal(err)
	}
	list, _ := st.ListFixApprovals(ctx, "gitea", "o", "r", "3", "h")

	// 全 pending → blocked
	ok, blocked, err := st.FixApprovalGate(ctx, "gitea", "o", "r", "3", "h",
		[]string{"fp1111aaaa", "fp2222bbbb"})
	if err != nil || ok || len(blocked) != 2 {
		t.Fatalf("pending gate: ok=%v blocked=%v err=%v", ok, blocked, err)
	}

	// 批准一张 → 仍 blocked 一张
	if _, err := st.DecideFixApproval(ctx, list[0].ID, FixApprovalApproved, "fp", ""); err != nil {
		t.Fatal(err)
	}
	ok, blocked, _ = st.FixApprovalGate(ctx, "gitea", "o", "r", "3", "h",
		[]string{"fp1111aaaa", "fp2222bbbb"})
	if ok || len(blocked) != 1 {
		t.Fatalf("half approve: ok=%v blocked=%v", ok, blocked)
	}

	// 批准第二张 → 放行
	if _, err := st.DecideFixApproval(ctx, list[1].ID, FixApprovalApproved, "fp", ""); err != nil {
		t.Fatal(err)
	}
	ok, blocked, _ = st.FixApprovalGate(ctx, "gitea", "o", "r", "3", "h",
		[]string{"fp1111aaaa", "fp2222bbbb"})
	if !ok || len(blocked) != 0 {
		t.Fatalf("all approve: ok=%v blocked=%v", ok, blocked)
	}

	// 缺票 fail-closed
	ok, blocked, _ = st.FixApprovalGate(ctx, "gitea", "o", "r", "3", "h", []string{"fpmissing"})
	if ok || len(blocked) != 1 {
		t.Fatalf("missing ticket: ok=%v blocked=%v", ok, blocked)
	}

	// 空列表放行
	ok, _, _ = st.FixApprovalGate(ctx, "gitea", "o", "r", "3", "h", nil)
	if !ok {
		t.Fatal("empty list should pass")
	}
}

func TestIsBlockingSeverity(t *testing.T) {
	for _, s := range []string{"high", "HIGH", " critical ", "Critical"} {
		if !IsBlockingSeverity(s) {
			t.Errorf("%q should block", s)
		}
	}
	for _, s := range []string{"medium", "low", "", "banana"} {
		if IsBlockingSeverity(s) {
			t.Errorf("%q should not block", s)
		}
	}
}
