// service_console_members_test.go —— GET /auth/members 契约（v8.8.0）：
// admin 面 200 + 凭证零投影；非 admin 403。
package service

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/yi-nology/argus/internal/config"
	"github.com/yi-nology/argus/internal/storage"
)

// seedUserViaInvite 走真实邀请链建号（覆盖 CreateInvite→AcceptInvite 路径）。
func seedUserViaInvite(t *testing.T, st *storage.Store, username, role, pass string) {
	t.Helper()
	ctx := context.Background()
	tok, _, err := st.Auths().CreateInvite(ctx, role, "", "seed-admin", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.Auths().AcceptInvite(ctx, tok, username, pass); err != nil {
		t.Fatal(err)
	}
}

func TestMembers(t *testing.T) {
	st := openVisStore(t)
	s := &Service{Cfg: &config.Config{}, Store: st}
	seedUserViaInvite(t, st, "alice", storage.RoleMaintainer, "pass-1234")
	seedUserViaInvite(t, st, "bob", storage.RoleViewer, "pass-1234")

	adminCtx := ContextWithSession(ContextWithScope(context.Background(), "admin"), "root", storage.RoleAdmin)
	data, code := s.Members(adminCtx)
	if code != http.StatusOK {
		t.Fatalf("admin status = %d, want 200", code)
	}
	members, _ := data["members"].([]map[string]any)
	if len(members) != 2 {
		t.Fatalf("members 数 = %d, want 2", len(members))
	}
	seen := map[string]string{}
	for _, m := range members {
		if _, leak := m["password_hash"]; leak {
			t.Fatal("成员投影不得含凭证字段")
		}
		seen[m["username"].(string)] = m["role"].(string)
	}
	if seen["alice"] != storage.RoleMaintainer || seen["bob"] != storage.RoleViewer {
		t.Fatalf("角色投影不符: %v", seen)
	}

	// 维护者（可裁定不可管理）/ read 档 → 403
	for _, ctx := range []context.Context{
		ContextWithSession(ContextWithScope(context.Background(), "admin"), "maint", storage.RoleMaintainer),
		ContextWithScope(context.Background(), "read"),
	} {
		if _, code := s.Members(ctx); code != http.StatusForbidden {
			t.Fatalf("非 admin status = %d, want 403", code)
		}
	}
}
