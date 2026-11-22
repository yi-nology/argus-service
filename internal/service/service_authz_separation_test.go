package service

import (
	"context"
	"testing"

	"github.com/yi-nology/argus/internal/storage"
)

// 平台管理与业务裁定物理分离（MergePilot 同款）：
// maintainer 可裁定不可管理；admin 可管理不可裁定；静态 token = 部署超管。
func TestDecideManageSeparation(t *testing.T) {
	maintainer := ContextWithSession(ContextWithScope(context.Background(), "admin"), "alice", storage.RoleMaintainer)
	admin := ContextWithSession(ContextWithScope(context.Background(), "admin"), "root", storage.RoleAdmin)
	super := ContextWithScope(context.Background(), "admin") // 静态 token
	readonly := ContextWithSession(ContextWithScope(context.Background(), "read"), "bob", storage.RoleViewer)

	// decide：maintainer ✓ · admin ✗ · super ✓ · viewer ✗
	if !canDecide(maintainer) {
		t.Error("maintainer 应可裁定（merge/decide/fix）")
	}
	if canDecide(admin) {
		t.Error("admin 不得裁定（平台管理≠业务裁定）")
	}
	if !canDecide(super) {
		t.Error("静态 token 部署超管应可裁定")
	}
	if canDecide(readonly) {
		t.Error("viewer 不得裁定")
	}

	// manage：maintainer ✗ · admin ✓ · super ✓ · viewer ✗
	if canManage(maintainer) {
		t.Error("maintainer 不得管理（邀请/实例）")
	}
	if !canManage(admin) {
		t.Error("admin 应可管理")
	}
	if !canManage(super) {
		t.Error("静态 token 部署超管应可管理")
	}
	if canManage(readonly) {
		t.Error("viewer 不得管理")
	}
}

func TestPermissionsProjectionNonRecursive(t *testing.T) {
	m := permissionsForScope("admin", storage.RoleMaintainer)
	if !m.DecideReview || !m.MergePr || !m.ApplyFix {
		t.Error("maintainer 应有裁定三权")
	}
	if m.ManageMembership || m.ManageInstance {
		t.Error("maintainer 不得有管理权")
	}

	a := permissionsForScope("admin", storage.RoleAdmin)
	if a.DecideReview || a.MergePr || a.ApplyFix {
		t.Error("admin 不得有裁定三权（非递增）")
	}
	if !a.ManageMembership || !a.ManageInstance {
		t.Error("admin 应有管理权")
	}

	s := permissionsForScope("admin", "")
	if !s.DecideReview || !s.ManageInstance {
		t.Error("静态超管应两面皆可")
	}

	v := permissionsForScope("read", storage.RoleViewer)
	if v.DecideReview || v.MergePr || v.ManageInstance {
		t.Error("viewer 只读")
	}
}
