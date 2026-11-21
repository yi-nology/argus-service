// tenant_test.go —— tenant 基元行为钉（Normalize/ctx/Key/逃生门）。

package tenant

import (
	"context"
	"testing"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func TestNormalize(t *testing.T) {
	cases := map[string]string{
		"":        Default,
		"   ":     Default,
		"  acme ": "acme",
		"acme":    "acme",
	}
	for in, want := range cases {
		if got := Normalize(in); got != want {
			t.Errorf("Normalize(%q) = %q, want %q", in, got, want)
		}
	}
	// 字面量钉：tenantDefault（storage 迁移）与本包同源。
	if Default != "default" {
		t.Fatalf(`Default 字面量漂移（storage tenantDefault 钉 "default"）: %q`, Default)
	}
}

func TestContextRoundTrip(t *testing.T) {
	ctx := NewContext(context.Background(), "")
	if FromContext(ctx) != Default {
		t.Fatalf("空租户写入应归一 Default, got %q", FromContext(ctx))
	}
	ctx = NewContext(context.Background(), "acme")
	if FromContext(ctx) != "acme" {
		t.Fatalf("got %q", FromContext(ctx))
	}
	if FromContext(context.Background()) != Default {
		t.Fatal("未写入 ctx 应取 Default")
	}
	//lint:ignore SA1012 nil 入参防御是 FromContext 契约（内部路径可能传 nil），显式钉行为。
	if FromContext(nil) != Default {
		t.Fatal("nil ctx 应取 Default（防内部路径 panic）")
	}
}

func TestKey(t *testing.T) {
	if got := Key("acme", "gitea:owner/repo#7"); got != "acme/gitea:owner/repo#7" {
		t.Fatalf("got %q", got)
	}
	if got := Key("", "rest"); got != "default/rest" {
		t.Fatalf("空租户前缀应归一, got %q", got)
	}
	if got := Key("acme", ""); got != "acme" {
		t.Fatalf("空 rest 应只留租户段, got %q", got)
	}
}

func TestScopeSkipped(t *testing.T) {
	if ScopeSkipped(context.Background()) {
		t.Fatal("普通 ctx 不应跳过")
	}
	if !ScopeSkipped(WithoutScope(context.Background())) {
		t.Fatal("WithoutScope 应标记跳过")
	}
}

// TestWithDeleted 软删除可见性逃生门（#109a 机制面）：默认不含软删行，
// WithDeleted 显式放行；nil ctx 安全回落 false（普通查询永不意外看到软删行）。
func TestWithDeleted(t *testing.T) {
	if DeletedIncluded(context.Background()) {
		t.Fatal("普通 ctx 不应包含软删除行")
	}
	if !DeletedIncluded(WithDeleted(context.Background())) {
		t.Fatal("WithDeleted 应标记包含软删除行")
	}
	//lint:ignore SA1012 nil 入参防御是 DeletedIncluded 契约（同 FromContext），显式钉行为。
	if DeletedIncluded(nil) {
		t.Fatal("nil ctx 应回落 false（fail-closed：不包含软删行）")
	}
}

// TestScoped gorm scope 谓词注入（#94 机制面）：有声明 → tenant_id 过滤；
// WithoutScope 逃生门 → 原样放行（不追加任何条件）。
func TestScoped(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	type row struct {
		ID       int
		TenantID string `gorm:"column:tenant_id"`
		Name     string `gorm:"column:name"`
	}
	if err := db.Exec(`CREATE TABLE rows (id INTEGER PRIMARY KEY, tenant_id TEXT, name TEXT)`).Error; err != nil {
		t.Fatalf("create table: %v", err)
	}
	for _, r := range []row{{1, "acme", "a"}, {2, "beta", "b"}} {
		if err := db.Exec(`INSERT INTO rows (id, tenant_id, name) VALUES (?, ?, ?)`, r.ID, r.TenantID, r.Name).Error; err != nil {
			t.Fatalf("insert: %v", err)
		}
	}
	count := func(ctx context.Context) int64 {
		var n int64
		if err := db.WithContext(ctx).Scopes(Scoped(ctx)).Table("rows").Count(&n).Error; err != nil {
			t.Fatalf("count: %v", err)
		}
		return n
	}
	if got := count(NewContext(context.Background(), "acme")); got != 1 {
		t.Fatalf("acme 只见 1 行, got %d", got)
	}
	if got := count(context.Background()); got != 0 {
		t.Fatalf("无声明回落 default 桶（表内无 default 行）应见 0 行——fail-closed 方向, got %d", got)
	}
	if got := count(WithoutScope(context.Background())); got != 2 {
		t.Fatalf("WithoutScope 应见全量 2 行, got %d", got)
	}
}
