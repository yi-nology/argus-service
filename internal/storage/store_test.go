// store_test.go —— 存储测试基座（openTestStore/MySQL/PG 隔离库）与 webhook 幂等去重。

package storage

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib" // pg 测试管理面（sql.Open("pgx")）

	_ "github.com/go-sql-driver/mysql"
)

// testDBCache 同测试名复用同一测试库（fencing 跨实例测试同测试内多次
// openTestStore 要打同一库——每调用建新库会撞名/拆台，pg 矩阵实锤）。
var testDBCache = map[string]string{}

func openTestStore(t *testing.T) *Store {
	t.Helper()
	dsn := filepath.Join(t.TempDir(), "test.db")
	if mysqlDSN := os.Getenv("ARGUS_TEST_MYSQL_DSN"); mysqlDSN != "" {
		dsn = setupTestMySQL(t, mysqlDSN)
	}
	// pg 分支（v6.1.7）：生产方言是 PostgreSQL（43 k8s 自 v5.4.8），此前测试矩阵
	// 只接 MySQL——生产方言零覆盖。与 setupTestMySQL 同款独立库+幂等清建。
	if pgDSN := os.Getenv("ARGUS_TEST_PG_DSN"); pgDSN != "" {
		dsn = setupTestPG(t, pgDSN)
	}
	s, err := Open(dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

// setupTestMySQL creates an isolated test database from the base DSN.
// The base DSN should point to the server without a specific database,
// e.g. "root:test@tcp(localhost:3306)/argus_test?parseTime=true".
// The dbname portion is used as a prefix; a unique suffix is appended per test.
func setupTestMySQL(t *testing.T, baseDSN string) string {
	t.Helper()
	if dsn, ok := testDBCache[t.Name()]; ok {
		return dsn
	}
	// Extract the database name from the DSN to use as a prefix.
	// DSN format: user:pass@tcp(host:port)/dbname?params
	dbName := "argus_test"
	afterSlash := baseDSN
	if idx := strings.Index(baseDSN, "/"); idx >= 0 {
		afterSlash = baseDSN[idx+1:]
	}
	if qIdx := strings.Index(afterSlash, "?"); qIdx >= 0 {
		dbName = afterSlash[:qIdx]
	} else if afterSlash != "" {
		dbName = afterSlash
	}

	// Build a server-level DSN (no database) for admin operations.
	serverDSN := baseDSN
	if idx := strings.Index(baseDSN, "/"); idx >= 0 {
		q := ""
		if qIdx := strings.Index(baseDSN[idx:], "?"); qIdx >= 0 {
			q = baseDSN[idx+qIdx:]
		}
		serverDSN = baseDSN[:idx+1] + q
	}

	admin, err := sql.Open("mysql", serverDSN)
	if err != nil {
		t.Fatalf("openTestStore: sql.Open admin: %v", err)
	}
	if err := admin.Ping(); err != nil {
		t.Fatalf("openTestStore: admin ping: %v", err)
	}

	// Sanitize test name for use as a database name.
	safe := strings.NewReplacer("/", "_", " ", "_", "-", "_", ".", "_").Replace(t.Name())
	testDB := fmt.Sprintf("%s_%s", dbName, safe)
	if len(testDB) > 64 {
		testDB = testDB[:64]
	}

	// 幂等（CI 二轮实锤）：库名可能被 64 字符截断撞名或残留重跑——先清再建。
	_, _ = admin.Exec("DROP DATABASE IF EXISTS `" + testDB + "`")
	_, err = admin.Exec("CREATE DATABASE `" + testDB + "` CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci")
	if err != nil {
		t.Fatalf("openTestStore: create db %q: %v", testDB, err)
	}
	t.Cleanup(func() {
		_, _ = admin.Exec("DROP DATABASE IF EXISTS `" + testDB + "`")
		_ = admin.Close()
	})

	// Return a DSN pointing at the new test database.
	// Replace the database name in the original DSN.
	testDSN := strings.Replace(baseDSN, dbName, testDB, 1)
	testDBCache[t.Name()] = testDSN
	return testDSN
}

// setupTestPG 从 base DSN 建隔离测试库（与 setupTestMySQL 同构）：库名前缀+
// 测试名 sanitize+64 截断，DROP IF EXISTS 幂等（截断撞名/残留重跑兜底）。
// base DSN 形如 postgres://user:pass@host:port/dbname?sslmode=disable——
// 库名段作前缀；管理连接改指系统库 postgres。
func setupTestPG(t *testing.T, baseDSN string) string {
	t.Helper()
	if dsn, ok := testDBCache[t.Name()]; ok {
		return dsn
	}
	u, err := url.Parse(baseDSN)
	if err != nil {
		t.Fatalf("setupTestPG: 解析 DSN: %v", err)
	}
	dbName := strings.TrimPrefix(u.Path, "/")
	if dbName == "" {
		dbName = "argus_test"
	}
	safe := strings.NewReplacer("/", "_", " ", "_", "-", "_", ".", "_").Replace(t.Name())
	// 全小写：pg 裸标识符折叠小写、带引号 DDL 保持大小写——统一小写后两态归一，
	// 残留库/折叠库/DSN 连接名恒一致（pg 矩阵实弹两轮撞名实锤）。
	testDB := strings.ToLower(fmt.Sprintf("%s_%s", dbName, safe))
	if len(testDB) > 63 { // pg 标识符上限 63，比 MySQL 64 再收一格
		testDB = testDB[:63]
	}
	adminDSN := *u
	adminDSN.Path = "/postgres"
	admin, err := sql.Open("pgx", adminDSN.String())
	if err != nil {
		t.Fatalf("setupTestPG: open admin: %v", err)
	}
	if err := admin.Ping(); err != nil {
		t.Fatalf("setupTestPG: admin ping: %v", err)
	}
	// 幂等：先清再建（pg CREATE DATABASE 不幂等，且不能在事务内）。
	// 双引号钉住大小写：pg 裸标识符折叠小写，而 DSN 连接库名保持原大小写——
	// 不加引号建出小写库、连不上原名库（pg 本地实弹 FATAL 3D000 实锤）。
	_, _ = admin.Exec(`DROP DATABASE IF EXISTS "` + testDB + `"`)
	if _, err = admin.Exec(`CREATE DATABASE "` + testDB + `" ENCODING 'utf8'`); err != nil {
		t.Fatalf("setupTestPG: create db %q: %v", testDB, err)
	}
	t.Cleanup(func() {
		_, _ = admin.Exec(`DROP DATABASE IF EXISTS "` + testDB + `"`)
		_ = admin.Close()
	})
	testDSN := *u
	testDSN.Path = "/" + testDB
	out := testDSN.String()
	testDBCache[t.Name()] = out
	return out
}

func mkTask(id string, eventID *string, headSHA string) *Task {
	return &Task{
		ID: id, Kind: "requirement_compliance",
		Platform: "gitea", Owner: "acme", Repo: "app", Number: "7",
		HeadSHA: headSHA, EventID: eventID, Status: StatusPending,
		CreatedAt: time.Now(),
	}
}

// 幂等：同 delivery ID 重发 → 命中既有任务。
func TestEventIdempotency(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	delivery := "d-111"
	dup := delivery

	if _, _, err := s.CreateTaskForWebhook(ctx, mkTask("t1", &dup, "sha-a")); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetTaskByEventID(ctx, delivery)
	if err != nil || got.ID != "t1" {
		t.Fatalf("幂等查询失败: %v %+v", err, got)
	}
	// 再次插入同 event_id（绕过业务检查直接 create）应违反唯一约束
	if err := s.CreateTask(ctx, mkTask("t2", &delivery, "sha-b")); err == nil {
		t.Fatal("同 event_id 第二次插入应被唯一索引拒绝")
	}
}

// 契约：无此 event_id 返回 ErrNotFound 哨兵（v6.2.0：此前裸透 gorm 错误，
// 违反"ORM 错误语义不出 storage 包"）。
func TestGetTaskByEventIDNotFound(t *testing.T) {
	s := openTestStore(t)
	got, err := s.GetTaskByEventID(context.Background(), "d-nope")
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("未命中应返回 ErrNotFound，got err=%v got=%+v", err, got)
	}
	if got != nil {
		t.Fatalf("未命中不应返回任务行: %+v", got)
	}
}

// 同 head SHA 去重：last_head_sha 命中 / in-flight 命中。
func TestHeadSHADedup(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()

	// 场景 A：无任何历史 → 创建成功
	d1 := "d1"
	reason, existing, err := s.CreateTaskForWebhook(ctx, mkTask("t1", &d1, "sha-1"))
	if err != nil || reason != "" {
		t.Fatalf("首次创建不应去重: reason=%q err=%v existing=%q", reason, err, existing)
	}

	// 场景 B：同 SHA 任务 pending 中 → in_flight 去重
	d2 := "d2"
	reason, existing, err = s.CreateTaskForWebhook(ctx, mkTask("t2", &d2, "sha-1"))
	if err != nil || reason != "in_flight" || existing != "t1" {
		t.Fatalf("in_flight 去重失败: reason=%q existing=%q err=%v", reason, existing, err)
	}

	// 模拟 t1 完成（done）并更新 review_targets.last_head_sha
	_ = s.FinishTask(ctx, "t1", StatusDone, "approve", "report", "{}", "", 100, 42)
	if err := s.UpsertReviewTarget(ctx, "gitea", "acme", "app", "7", 42, "sha-1"); err != nil {
		t.Fatal(err)
	}

	// 场景 C：last_head_sha == 本次 head → 去重
	d3 := "d3"
	reason, _, err = s.CreateTaskForWebhook(ctx, mkTask("t3", &d3, "sha-1"))
	if err != nil || reason != "last_head_sha" {
		t.Fatalf("last_head_sha 去重失败: reason=%q err=%v", reason, err)
	}

	// 场景 D：新 SHA → 正常创建
	d4 := "d4"
	reason, _, err = s.CreateTaskForWebhook(ctx, mkTask("t4", &d4, "sha-2"))
	if err != nil || reason != "" {
		t.Fatalf("新 SHA 不应去重: reason=%q err=%v", reason, err)
	}
}

// 缺 delivery header → event_id 为 NULL，多条 NULL 共存不冲突。
func TestNullEventId(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	if _, _, err := s.CreateTaskForWebhook(ctx, mkTask("t1", nil, "sha-1")); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.CreateTaskForWebhook(ctx, mkTask("t2", nil, "sha-2")); err != nil {
		t.Fatalf("多个 NULL event_id 应共存: %v", err)
	}
}
