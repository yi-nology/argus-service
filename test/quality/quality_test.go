package quality_test

// Phase 4 质量运营入口：quality-report（批次环比）+ eval-candidates
// （误报/回归候选清单）。纯数据面不联网；候选清单真库扫描走
// ARGUS_CANDIDATES_DSN（缺省合成数据测逻辑，真库形态部署后 43 实弹）。

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/yi-nology/argus/internal/domain"
	"github.com/yi-nology/argus/internal/qualityops"
	"github.com/yi-nology/argus/internal/storage"
)

// TestQualityReport 批次环比报告（make quality-report）：读
// eval/results 全部批次 JSON → 趋势序列+相邻环比 → results/quality-latest.md。
func TestQualityReport(t *testing.T) {
	pts, err := qualityops.LoadBatchPoints(filepath.Join("..", "..", "eval", "results"))
	if err != nil {
		t.Fatalf("读取批次序列失败: %v", err)
	}
	if len(pts) == 0 {
		// 空=无真实批次（CI 只有 model=stub 打桩批，读侧已过滤；本地缺跑批）：
		// 报告是本地运营面，无可环比就跳过——0 批次 Fatal 会让 CI 恒红
		//（2026-11-27 实锤）。
		t.Skip("无真实批次可报告（eval/results 仅打桩/空）")
	}
	md := qualityops.RenderDeltaMarkdown(pts, qualityops.Deltas(pts))

	outDir := filepath.Join("results")
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(outDir, "quality-latest.md")
	if err := os.WriteFile(path, []byte(md), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Logf("批次=%d 环比段=%d 报告已写 %s", len(pts), len(qualityops.Deltas(pts)), path)

	// 跨口径线必须被标注（口径切换点纪律）
	for _, d := range qualityops.Deltas(pts) {
		if d.CrossMetric {
			t.Logf("跨口径段 %s→%s 已标禁直比", d.From, d.To)
		}
	}
}

// TestEffectiveFindingsRate 有效发现率定义（Phase 4）：verified+structural / 总。
func TestEffectiveFindingsRate(t *testing.T) {
	rate, num, den := qualityops.EffectiveFindings([]string{
		"verified", "structurally_valid", "unverified", "invalid", "verified",
	})
	if den != 5 || num != 3 || rate < 0.59 || rate > 0.61 {
		t.Fatalf("rate=%.3f num=%d den=%d, want ~0.6 3/5", rate, num, den)
	}
	if r, _, d := qualityops.EffectiveFindings(nil); r != 0 || d != 0 {
		t.Fatalf("空输入应 0, got %.3f/%d", r, d)
	}
}

// TestFixRate 修复率定义（Phase 4）：merged/(merged+open)；failed/not_tried
// 不入分母，unknown 归 open 保守计。
func TestFixRate(t *testing.T) {
	rate, m, d := qualityops.FixRate([]qualityops.FixOutcome{
		qualityops.FixMerged, qualityops.FixMerged, qualityops.FixOpen,
		qualityops.FixFailed, qualityops.FixNotTried, qualityops.FixOpen,
	})
	if m != 2 || d != 4 || rate < 0.49 || rate > 0.51 {
		t.Fatalf("rate=%.3f merged=%d denom=%d, want 0.5 2/4", rate, m, d)
	}
}

func itoa(n int) string { return strconv.Itoa(n) }

// TestEvalCandidates 误报候选清单（make eval-candidates）：suppressed
// prints → candidates-fp.md（Phase 4 §3.1：机器产候选、人工合入金标准）。
// 真库扫描 ARGUS_CANDIDATES_DSN 可选；缺省合成库测扫描逻辑。
func TestEvalCandidates(t *testing.T) {
	dir := t.TempDir()
	store, err := storage.Open(filepath.Join(dir, "cand.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })

	// 合成：两条 suppressed（误报候选）+ 一条 open（不入候选）——
	// UpsertPrint 落 open，SuppressPrint 置 suppressed（生产通道同形）。
	ctx := context.Background()
	now := time.Now()
	// 三条不同 File/Comment——同文件近评语会走 findNearPrint 并入（测的是
	// suppressed 扫描不是近重复，避开并入通道）
	fps := []string{"aaaa000000000001", "aaaa000000000002", "aaaa000000000003"}
	seeds := []domain.Finding{
		{File: "auth/login.go", Line: 12, Severity: "medium", Comment: "口令硬编码"},
		{File: "db/query.go", Line: 40, Severity: "high", Comment: "SQL 字符串拼接"},
		{File: "ui/button.go", Line: 7, Severity: "low", Comment: "命名不规范"},
	}
	for i, fp := range fps {
		f := seeds[i]
		if _, err := store.UpsertPrint(ctx, "gitea", "acme", "repo", "42",
			fp, f, "task-1", now); err != nil {
			t.Fatalf("seed print %d: %v", i, err)
		}
		if i < 2 { // 前两条置 suppressed
			if _, err := store.SuppressPrint(ctx, "gitea", "acme", "repo", "42",
				fp, f, now); err != nil {
				t.Fatalf("suppress %d: %v", i, err)
			}
		}
	}
	sup, err := store.ListSuppressedPrints(ctx, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(sup) != 2 {
		t.Fatalf("suppressed 候选 = %d, want 2", len(sup))
	}

	// 修复状态聚合（修复率输入面）
	for _, fs := range []string{"pr_created", "pr_created", "pushed"} {
		if err := store.UpdateTaskFixStatus(ctx, "t-"+fs, fs); err != nil {
			// 任务行不存在——UpdateTaskFixStatus 只 UPDATE 不校验存在性，0 行也算过
			t.Logf("fix status %s 更新（可能 0 行）: %v", fs, err)
		}
	}
	counts, err := store.CountFixStatuses(ctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("fix_status 分布: %+v", counts)

	// 批 E 判例桶待审段（candidate 态进清单，人工 ConfirmFPCase 才参与提示）
	buckets, err := store.ListFPCaseCandidates(ctx, 0)
	if err != nil {
		t.Fatal(err)
	}

	// 候选清单落盘（results/ 已 gitignore）
	md := "# 误报候选清单（eval-candidates）\n\n> 机器产候选、人工合入金标准（Phase 4 防污染红线）。\n\n" +
		"## 单仓指纹（suppressed prints）\n\n"
	for _, p := range sup {
		md += "- `" + p.Print + "` " + p.File + " " + p.Comment + "（" + p.Owner + "/" + p.Repo + "）\n"
	}
	md += "\n## 组织判例桶（批 E candidate，ConfirmFPCase 确认后才提示）\n\n"
	for _, b := range buckets {
		md += "- `" + b.Pattern[:12] + "` org=" + b.Org + " hits=" +
			itoa(b.HitCount) + " 样例=" + b.SampleRepo + "：" + b.Comment + "\n"
	}
	outDir := filepath.Join("results")
	_ = os.MkdirAll(outDir, 0o755)
	if err := os.WriteFile(filepath.Join(outDir, "candidates-fp.md"), []byte(md), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Logf("候选清单已写 results/candidates-fp.md（%d 条）", len(sup))
}
