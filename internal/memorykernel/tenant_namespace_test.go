// tenant_namespace_test.go —— 记忆命名空间租户分桶钉（#84）：
// 同坐标仓跨租户命名空间不同（R3 prompt 注入面隔离）；default 租户保持
// legacy 形态（存量 kernel.db 数据原位兼容，不触发重导）。

package memorykernel

import "testing"

func TestNamespaceOfTenantPrefix(t *testing.T) {
	ref := Ref{Platform: "gitea", Owner: "acme", Repo: "web"}
	if got := NamespaceOf(ref); got != "gitea-acme-web" {
		t.Fatalf("default（空 Tenant）应 legacy 形态, got %q", got)
	}
	refA := Ref{Platform: "gitea", Owner: "acme", Repo: "web", Tenant: "tenant-a"}
	refB := Ref{Platform: "gitea", Owner: "acme", Repo: "web", Tenant: "tenant-b"}
	if NamespaceOf(refA) == NamespaceOf(refB) {
		t.Fatalf("同坐标跨租户命名空间必须不同: %q", NamespaceOf(refA))
	}
	if got := NamespaceOf(refA); got != "t-tenant-a/gitea-acme-web" {
		t.Fatalf("租户前缀形态漂移: %q", got)
	}
	// default 显式形态与空形态一致（单租户兼容）。
	refDef := Ref{Platform: "gitea", Owner: "acme", Repo: "web", Tenant: "default"}
	if NamespaceOf(refDef) != NamespaceOf(ref) {
		t.Fatalf("显式 default 应与空形态同命名空间: %q vs %q", NamespaceOf(refDef), NamespaceOf(ref))
	}
}

func TestOrgNamespaceOfTenantBucket(t *testing.T) {
	if OrgNamespaceOf("") != OrgNamespace || OrgNamespaceOf("default") != OrgNamespace {
		t.Fatalf("default 组织桶应 legacy 形态 %q", OrgNamespace)
	}
	ta, tb := OrgNamespaceOf("tenant-a"), OrgNamespaceOf("tenant-b")
	if ta == tb || ta == OrgNamespace {
		t.Fatalf("组织桶跨租户必须分桶: %q vs %q", ta, tb)
	}
	if ta != "t-tenant-a/_org" {
		t.Fatalf("组织桶租户前缀形态漂移: %q", ta)
	}
}
