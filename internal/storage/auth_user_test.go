// auth_user_test.go —— 多用户最小集：邀请/登录/会话/角色投影。
package storage

import (
	"context"
	"path/filepath"
	"testing"
	"time"
)

func openAuthStore(t *testing.T) *Store {
	t.Helper()
	st, err := Open(filepath.Join(t.TempDir(), "auth.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st
}

func TestRoleToScope(t *testing.T) {
	cases := map[string]string{
		RoleViewer: "read", RoleAuditor: "read",
		RoleOperator:   "ci",
		RoleMaintainer: "admin", RoleAdmin: "admin",
		"": "read", "unknown": "read",
	}
	for role, want := range cases {
		if got := RoleToScope(role); got != want {
			t.Errorf("RoleToScope(%q)=%q want %q", role, got, want)
		}
	}
}

func TestInviteAcceptLoginSession(t *testing.T) {
	st := openAuthStore(t)
	ctx := context.Background()

	// 邀请不能是 admin
	if _, _, err := st.Auths().CreateInvite(ctx, RoleAdmin, "", "sys", time.Hour); err == nil {
		t.Fatal("admin invite should fail")
	}

	token, inv, err := st.Auths().CreateInvite(ctx, RoleMaintainer, "alice", "admin1", time.Hour)
	if err != nil || token == "" || inv.Role != RoleMaintainer {
		t.Fatalf("invite: tok=%q inv=%+v err=%v", token, inv, err)
	}

	// 错误 token
	if _, err := st.Auths().AcceptInvite(ctx, "deadbeef", "alice", "password123"); err == nil {
		t.Fatal("bad token should fail")
	}

	// 接受邀请
	u, err := st.Auths().AcceptInvite(ctx, token, "alice", "password123")
	if err != nil || u.Username != "alice" || u.Role != RoleMaintainer {
		t.Fatalf("accept: u=%+v err=%v", u, err)
	}
	// 二次使用拒绝
	if _, err := st.Auths().AcceptInvite(ctx, token, "bob", "password123"); err == nil {
		t.Fatal("reuse invite should fail")
	}

	// 登录
	if _, err := st.Auths().VerifyLogin(ctx, "alice", "wrong-password"); err == nil {
		t.Fatal("bad password should fail")
	}
	u2, err := st.Auths().VerifyLogin(ctx, "alice", "password123")
	if err != nil || u2.ID != u.ID {
		t.Fatalf("login: u=%+v err=%v", u2, err)
	}

	// 会话
	sessTok, _, err := st.Auths().CreateSession(ctx, u.ID, u.Role, time.Hour)
	if err != nil || sessTok == "" {
		t.Fatalf("session: %v", err)
	}
	gotSess, gotUser, err := st.Auths().GetSessionByToken(ctx, sessTok)
	if err != nil || gotUser == nil || gotUser.Username != "alice" || gotSess.Role != RoleMaintainer {
		t.Fatalf("get session: sess=%+v user=%+v err=%v", gotSess, gotUser, err)
	}
	// 登出后失效
	if err := st.Auths().RevokeSession(ctx, sessTok); err != nil {
		t.Fatal(err)
	}
	if _, _, err := st.Auths().GetSessionByToken(ctx, sessTok); err == nil {
		t.Fatal("revoked session should fail")
	}
}

func TestSeedBootstrapAdminIdempotent(t *testing.T) {
	st := openAuthStore(t)
	ctx := context.Background()

	created, err := st.Auths().SeedBootstrapAdmin(ctx, "root", "password123")
	if err != nil || !created {
		t.Fatalf("first seed: created=%v err=%v", created, err)
	}
	created, err = st.Auths().SeedBootstrapAdmin(ctx, "root2", "password123")
	if err != nil || created {
		t.Fatalf("second seed should skip: created=%v err=%v", created, err)
	}
	u, err := st.Auths().GetUserByUsername(ctx, "root")
	if err != nil || u.Role != RoleAdmin {
		t.Fatalf("bootstrap admin: %+v err=%v", u, err)
	}
}
