package runner

import (
	"context"
	"testing"

	"github.com/yi-nology/argus/internal/tenant"
)

func TestModelBundleTenantResolution(t *testing.T) {
	tenantB := &ModelBundle{ModelName: "tenant-b-model"}
	r := &Runner{Model: nil, ModelName: "global-model", TenantStacks: map[string]*ModelBundle{"tenant-b": tenantB}}

	// default/未声明 → 默认视图（惰性快照自全局字段）。
	if got := r.modelBundle(context.Background()); got.ModelName != "global-model" {
		t.Fatalf("default 应默认视图: %s", got.ModelName)
	}
	// 租户命中 → 租户视图。
	ctxB := tenant.NewContext(context.Background(), "tenant-b")
	if got := r.modelBundle(ctxB); got.ModelName != "tenant-b-model" {
		t.Fatalf("tenant-b 应租户视图: %s", got.ModelName)
	}
	// 无覆盖租户 → 回落默认视图。
	ctxC := tenant.NewContext(context.Background(), "tenant-c")
	if got := r.modelBundle(ctxC); got != r.modelBundle(context.Background()) {
		t.Fatal("未覆盖租户应回落默认视图（同一快照实例）")
	}
	// 默认视图快照一次（并发读同一指针）。
	if r.defaultBundle() != r.defaultBundle() {
		t.Fatal("defaultBundle 应惰性单例")
	}
}
