// prints_exact_test.go —— 精确通道/跨 PR 漂移/批量入库测试。

package storage

import (
	"context"
	"testing"
	"time"

	"github.com/yi-nology/argus/internal/domain"
)

// v3.8.0 回归：同 PR 推一版代码使行号整体位移 >±2 时，完全同评语的意见
// 仍并入既有条目（TimesSeen 延续、抑制不复活、行号跟随新值）——±2 近重复
// 窗口救不回的"行号漂移"由 exact 通道承接。
func TestUpsertPrint_LineDriftBeyondWindow_ExactCommentAbsorbed(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	at := time.Now()
	if _, err := s.UpsertPrint(ctx, "gitea", "acme", "app", "10",
		domain.Fingerprint("a.go", "资源释放缺失", 30),
		domain.Finding{File: "a.go", Line: 30, Severity: "medium", Comment: "资源释放缺失"}, "t1", at); err != nil {
		t.Fatal(err)
	}
	// 抑制后行号大幅位移（30 → 40，>±2），同评语复现
	suppressed, err := s.UpsertPrint(ctx, "gitea", "acme", "app", "10",
		domain.Fingerprint("a.go", "资源释放缺失", 40),
		domain.Finding{File: "a.go", Line: 40, Severity: "medium", Comment: "资源释放缺失"}, "t2", at)
	_ = suppressed
	if err != nil {
		t.Fatal(err)
	}
	var rows []FindingPrint
	if err := s.db.Where("platform=? AND owner=? AND repo=? AND number=?", "gitea", "acme", "app", "10").
		Find(&rows).Error; err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 {
		t.Fatalf("行号漂移的完全同评语应并入既有行，got %d 行", len(rows))
	}
	if rows[0].TimesSeen != 2 || rows[0].Line != 40 {
		t.Fatalf("计数应延续、行号应跟随: %+v", rows[0])
	}
}

// v3.8.0 回归：SuppressPrintNear 未命中时预插的 line=0 suppressed 行，必须能被
// 后续真实意见（任意行号）经 exact 通道并回——"先抑制后见到"才真正生效。
func TestSuppressPrintNear_PreinsertRecoveredByExactComment(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	at := time.Now()
	if _, matched, err := s.SuppressPrintNear(ctx, "gitea", "acme", "app", "11", "b.go", "并发写 map 未加锁", at); err != nil || matched {
		t.Fatalf("预插路径 matched 应为 false: %v %v", matched, err)
	}
	suppressed, err := s.UpsertPrint(ctx, "gitea", "acme", "app", "11",
		domain.Fingerprint("b.go", "并发写 map 未加锁", 88),
		domain.Finding{File: "b.go", Line: 88, Severity: "high", Comment: "并发写 map 未加锁"}, "t2", at)
	if err != nil {
		t.Fatal(err)
	}
	if !suppressed {
		t.Fatal("真实意见应并回预插的 suppressed 行（抑制生效）")
	}
	var rows []FindingPrint
	if err := s.db.Where("platform=? AND owner=? AND repo=? AND number=?", "gitea", "acme", "app", "11").
		Find(&rows).Error; err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].Line != 88 || rows[0].Status != PrintSuppressed {
		t.Fatalf("应并回单行且行号跟随: %+v", rows)
	}
}

// v3.9.1 回归：alias_fp 全局唯一主键下，两个 PR 出现同款措辞漂移（同一新指纹）
// 时，第二个 PR 重复创建别名此前会撞主键、UpsertPrint 整体回滚（该意见此后
// 每轮 print_upsert_failed，抑制/计数/复现全部失效）。别名写入现已幂等。
func TestUpsertPrint_SameDriftAcrossPRsNoAliasConflict(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	at := time.Now()
	base := "这里存在SQL注入风险"
	drift := "拼接 SQL 存在注入风险！"
	baseFP := domain.Fingerprint("a.go", base, 10)
	driftFP := domain.Fingerprint("a.go", drift, 11)

	for _, number := range []string{"7", "8"} {
		if _, err := s.UpsertPrint(ctx, "gitea", "acme", "app", number, baseFP,
			domain.Finding{File: "a.go", Line: 10, Severity: "high", Comment: base, By: "security"},
			"t-base-"+number, at); err != nil {
			t.Fatal(err)
		}
		if _, err := s.UpsertPrint(ctx, "gitea", "acme", "app", number, driftFP,
			domain.Finding{File: "a.go", Line: 11, Severity: "high", Comment: drift, By: "security"},
			"t-drift-"+number, at); err != nil {
			t.Fatalf("PR %s 漂移并入不得因别名主键冲突失败: %v", number, err)
		}
		if st, err := s.GetPrintState(ctx, "gitea", "acme", "app", number, baseFP); err != nil || st == nil || st.TimesSeen != 2 {
			t.Fatalf("PR %s 既有条目计数应延续（TimesSeen=2），got %+v err=%v", number, st, err)
		}
		// v3.9.2：别名 PR 域化后，两个 PR 各自的漂移指纹都应解析到本 PR 的规范行
		if st, err := s.GetPrintState(ctx, "gitea", "acme", "app", number, driftFP); err != nil || st == nil || st.TimesSeen != 2 {
			t.Fatalf("PR %s 漂移指纹应经本 PR 别名解析到规范行，got %+v err=%v", number, st, err)
		}
	}
}

// TestUpsertPrints_BatchSingleLock 批量入库：逐条独立、Prior 返回入库前状态、
// suppressed 语义与单条一致。整批共用一次 writeMu（本测试锁语义由实现保证，
// 此处验证结果正确性）。
func TestUpsertPrints_BatchSingleLock(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	now := time.Now()

	// 预置一条 suppressed，批量里既有既有指纹也有新指纹
	fpOld := domain.Fingerprint("a.go", "旧问题", 1)
	if _, err := s.UpsertPrint(ctx, "gitea", "acme", "app", "7", fpOld,
		domain.Finding{File: "a.go", Line: 1, Severity: "high", Comment: "旧问题", By: "s1"}, "t0", now); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SuppressPrint(ctx, "gitea", "acme", "app", "7", fpOld,
		domain.Finding{File: "a.go", Line: 1, Severity: "high", Comment: "旧问题"}, now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}

	fpNew := domain.Fingerprint("b.go", "新问题", 9)
	res := s.UpsertPrints(ctx, "gitea", "acme", "app", "7", "t1", now.Add(2*time.Second),
		[]domain.PrintUpsert{
			{FP: fpOld, Finding: domain.Finding{File: "a.go", Line: 1, Severity: "high", Comment: "旧问题", By: "s1"}},
			{FP: fpNew, Finding: domain.Finding{File: "b.go", Line: 9, Severity: "low", Comment: "新问题", By: "s2"}},
		})
	if len(res) != 2 {
		t.Fatalf("结果应与输入等长，got %d", len(res))
	}
	if res[0].Err != nil || !res[0].Suppressed {
		t.Fatalf("既有 suppressed 条目应被剔除，got %+v", res[0])
	}
	if res[0].Prior == nil || res[0].Prior.TimesSeen != 1 {
		t.Fatalf("Prior 应为入库前状态（TimesSeen=1），got %+v", res[0].Prior)
	}
	if res[1].Err != nil || res[1].Suppressed {
		t.Fatalf("新条目不应 suppressed，got %+v", res[1])
	}
	if res[1].Prior != nil {
		t.Fatalf("全新指纹 Prior 应为 nil，got %+v", res[1].Prior)
	}
	// 新条目已入库
	st, err := s.GetPrintState(ctx, "gitea", "acme", "app", "7", fpNew)
	if err != nil || st == nil || st.TimesSeen != 1 {
		t.Fatalf("新条目应入库 TimesSeen=1，got %+v err=%v", st, err)
	}
}
