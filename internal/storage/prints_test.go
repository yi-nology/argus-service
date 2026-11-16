// prints_test.go —— 指纹读写测试：suppress/restore 往返、近重复合并与别名回退。

package storage

import (
	"context"
	"testing"
	"time"

	"github.com/yi-nology/argus/internal/domain"
)

// suppress → restore 全往返 + 负控（不存在前缀 / 前缀歧义）。
// restore 是 suppress 的反向操作：只把 suppressed 行置回 open，不误伤其他行。
func TestRestorePrint_RoundTrip(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	at := time.Now().UTC()
	fp := "abcdef1234567890"
	f := domain.Finding{File: "a.go", Line: 10, Severity: "warn", Comment: "问题甲"}

	// 首次 suppress 库中尚无该指纹：预插 suppressed，matched=false（文档语义）。
	matched, err := s.SuppressPrint(ctx, "gitea", "acme", "app", "12", fp, f, at)
	if err != nil || matched {
		t.Fatalf("首次 SuppressPrint 预插: matched=%v err=%v", matched, err)
	}
	st, err := s.GetPrintState(ctx, "gitea", "acme", "app", "12", fp)
	if err != nil || st == nil || st.Status != "suppressed" {
		t.Fatalf("suppress 后状态=%+v err=%v", st, err)
	}

	// 既有指纹再次 suppress：matched=true
	matched, err = s.SuppressPrint(ctx, "gitea", "acme", "app", "12", fp, f, at)
	if err != nil || !matched {
		t.Fatalf("二次 SuppressPrint: matched=%v err=%v", matched, err)
	}

	// 前缀命中 → 恢复 open
	matched, err = s.RestorePrint(ctx, "gitea", "acme", "app", 12, fp[:8])
	if err != nil || !matched {
		t.Fatalf("RestorePrint: matched=%v err=%v", matched, err)
	}
	st, err = s.GetPrintState(ctx, "gitea", "acme", "app", "12", fp)
	if err != nil || st == nil || st.Status != "open" {
		t.Fatalf("restore 后状态=%+v err=%v", st, err)
	}

	// 负控1：不存在的前缀不 matched
	if matched, _ := s.RestorePrint(ctx, "gitea", "acme", "app", 12, "deadbeef"); matched {
		t.Fatal("不存在的前缀不应 matched")
	}

	// 负控2：两条 suppressed 共享前缀 → 歧义不猜
	fp2 := fp[:8] + "99999999"
	if _, err := s.SuppressPrint(ctx, "gitea", "acme", "app", "12", fp2, f, at); err != nil {
		t.Fatal(err)
	}
	if matched, _ := s.RestorePrint(ctx, "gitea", "acme", "app", 12, fp[:8]); matched {
		t.Fatal("前缀歧义不应 matched")
	}
}

// ---- v3.7.0 指纹 v2 + 近重复匹配层 ----

// 措辞漂移：第二轮措辞微调（精确未命中）→ 近重复并入既有条目（TimesSeen 延续、
// 规范指纹不变、别名落库）；第三轮同一新措辞 → 别名精确命中。
func TestUpsertPrint_NearDuplicateWordingMerges(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	at := time.Now()
	v2a := domain.Fingerprint("a.go", "这里存在SQL注入风险", 10)

	// 第一轮：原始措辞
	if _, err := s.UpsertPrint(ctx, "gitea", "acme", "app", "7", v2a,
		domain.Finding{File: "a.go", Line: 10, Severity: "high", Comment: "这里存在SQL注入风险", By: "security"},
		"t1", at); err != nil {
		t.Fatal(err)
	}
	// 第二轮：措辞漂移（精确未命中 → 近重复并入）
	drifted := domain.Fingerprint("a.go", "拼接 SQL 存在注入风险！", 11)
	suppressed, err := s.UpsertPrint(ctx, "gitea", "acme", "app", "7", drifted,
		domain.Finding{File: "a.go", Line: 11, Severity: "high", Comment: "拼接 SQL 存在注入风险！", By: "security"},
		"t2", at)
	if err != nil || suppressed {
		t.Fatalf("近重复应并入既有条目: suppressed=%v err=%v", suppressed, err)
	}
	st, err := s.GetPrintState(ctx, "gitea", "acme", "app", "7", v2a)
	if err != nil || st == nil || st.TimesSeen != 2 {
		t.Fatalf("既有条目计数应延续（TimesSeen=2），got %+v err=%v", st, err)
	}
	// 第三轮：漂移措辞经别名精确命中
	if _, err := s.UpsertPrint(ctx, "gitea", "acme", "app", "7", drifted,
		domain.Finding{File: "a.go", Line: 11, Severity: "high", Comment: "拼接 SQL 存在注入风险！", By: "security"},
		"t3", at); err != nil {
		t.Fatal(err)
	}
	if st, _ := s.GetPrintState(ctx, "gitea", "acme", "app", "7", v2a); st == nil || st.TimesSeen != 3 {
		t.Fatalf("别名精确命中应继续累加（TimesSeen=3），got %+v", st)
	}
}

// 抑制经近重复延续：条目被抑制后，措辞漂移的复现同样被剔除（不复活）。
func TestUpsertPrint_SuppressedNearDuplicateStillDropped(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	at := time.Now()
	fp := domain.Fingerprint("a.go", "硬编码密钥", 20)
	if _, err := s.UpsertPrint(ctx, "gitea", "acme", "app", "7", fp,
		domain.Finding{File: "a.go", Line: 20, Severity: "high", Comment: "硬编码密钥"}, "t1", at); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SuppressPrint(ctx, "gitea", "acme", "app", "7", fp,
		domain.Finding{File: "a.go", Line: 20, Comment: "硬编码密钥"}, at); err != nil {
		t.Fatal(err)
	}
	drifted := domain.Fingerprint("a.go", "密钥硬编码在源码中", 21)
	suppressed, err := s.UpsertPrint(ctx, "gitea", "acme", "app", "7", drifted,
		domain.Finding{File: "a.go", Line: 21, Severity: "high", Comment: "密钥硬编码在源码中"}, "t2", at)
	if err != nil || !suppressed {
		t.Fatalf("抑制条目的措辞漂移复现应保持剔除: suppressed=%v err=%v", suppressed, err)
	}
}

// SuppressPrintNear：file+原文 形态经近重复命中既有条目（API 无行号场景）。
func TestSuppressPrintNear_MatchesExisting(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	at := time.Now()
	if _, err := s.UpsertPrint(ctx, "gitea", "acme", "app", "7", domain.Fingerprint("a.go", "goroutine 泄漏未处理", 30),
		domain.Finding{File: "a.go", Line: 30, Severity: "medium", Comment: "goroutine 泄漏未处理"}, "t1", at); err != nil {
		t.Fatal(err)
	}
	fp, matched, err := s.SuppressPrintNear(ctx, "gitea", "acme", "app", "7", "a.go", "goroutine 泄漏未处理", at)
	if err != nil || !matched || fp == "" {
		t.Fatalf("近重复抑制应命中: matched=%v fp=%q err=%v", matched, fp, err)
	}
}

// v3.8.0 回归：suppress 能经别名进来，restore 也必须能经别名出去——
// 抑制不得是"进得来、出不去"的单向门。
func TestRestorePrint_AliasFallback(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	at := time.Now()
	orig := "goroutine 泄漏未处理"
	if _, err := s.UpsertPrint(ctx, "gitea", "acme", "app", "9",
		domain.Fingerprint("a.go", orig, 30),
		domain.Finding{File: "a.go", Line: 30, Severity: "medium", Comment: orig}, "t1", at); err != nil {
		t.Fatal(err)
	}
	// 措辞漂移：新措辞并入既有条目，产生别名
	drifted := "goroutine 泄漏了却没人处理它"
	aliasFP := domain.Fingerprint("a.go", drifted, 31)
	if _, err := s.UpsertPrint(ctx, "gitea", "acme", "app", "9", aliasFP,
		domain.Finding{File: "a.go", Line: 31, Severity: "medium", Comment: drifted}, "t2", at); err != nil {
		t.Fatal(err)
	}
	// 经别名 suppress（用别名短码直达）
	rec, ok := s.ResolvePrintRecord(ctx, "gitea", "acme", "app", "9", aliasFP[:8])
	if !ok || rec.Print == aliasFP {
		t.Fatalf("别名短码应解析回规范行: ok=%v print=%q", ok, rec.Print)
	}
	if rec.Comment != orig {
		t.Fatalf("ResolvePrintRecord 应回原始评语: got %q want %q", rec.Comment, orig)
	}
	if _, err := s.SuppressPrint(ctx, "gitea", "acme", "app", "9", rec.Print,
		domain.Finding{File: rec.File, Comment: rec.Comment}, at); err != nil {
		t.Fatal(err)
	}
	// 用别名短码 restore（此前 RestorePrint 无别名回退，此处 matched=false 单向门）
	matched, err := s.RestorePrint(ctx, "gitea", "acme", "app", 9, aliasFP[:8])
	if err != nil || !matched {
		t.Fatalf("别名短码 restore 应命中: matched=%v err=%v", matched, err)
	}
	st, err := s.GetPrintState(ctx, "gitea", "acme", "app", "9", rec.Print)
	if err != nil || st == nil || st.Status != "open" {
		t.Fatalf("restore 后状态=%+v err=%v", st, err)
	}
}

// 指纹跨轮次：更新而非新增；suppressed 保留并回报。
func TestPrintUpsert(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	f := domain.Finding{File: "a.go", Line: 10, Severity: "high", Comment: "SQL 注入"}
	now := time.Now()

	suppressed, err := s.UpsertPrint(ctx, "gitea", "acme", "app", "7", "fp1", f, "t1", now)
	if err != nil || suppressed {
		t.Fatalf("首次入库: suppressed=%v err=%v", suppressed, err)
	}
	f2 := domain.Finding{File: "a.go", Line: 11, Severity: "high", Comment: "SQL 注入"}
	suppressed, err = s.UpsertPrint(ctx, "gitea", "acme", "app", "7", "fp1", f2, "t2", now)
	if err != nil || suppressed {
		t.Fatalf("二次应为 open: %v %v", suppressed, err)
	}
	prints, err := s.ListFindingPrints(ctx, "gitea", "acme", "app", "7")
	if err != nil || len(prints) != 1 {
		t.Fatalf("同指纹应只有一条: %v %d", err, len(prints))
	}
	p := prints[0]
	if p.TimesSeen != 2 || p.Line != 11 || p.LastTaskID != "t2" {
		t.Fatalf("跨轮次更新错误: seen=%d line=%d last=%s", p.TimesSeen, p.Line, p.LastTaskID)
	}
	// 标记 suppressed 后，新轮次保持抑制状态
	if err := s.db.WithContext(ctx).Model(&FindingPrint{}).Where("print = ?", "fp1").
		Update("status", PrintSuppressed).Error; err != nil {
		t.Fatal(err)
	}
	suppressed, err = s.UpsertPrint(ctx, "gitea", "acme", "app", "7", "fp1", f, "t3", now)
	if err != nil || !suppressed {
		t.Fatalf("suppressed 应保持: %v %v", suppressed, err)
	}
}
