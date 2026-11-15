// poster_inline_test.go —— 行内意见与评审事件投递测试（自 poster_flow_test.go 拆出）。

package poster

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/yi-nology/go-git-platform/provider"

	"github.com/yi-nology/argus/internal/domain"
	"github.com/yi-nology/argus/internal/plugin"
)

// ---------- prepareInline ----------

// 行内候选过滤 + 路径归一化 + 严重度排序 + max_comments 钳制。
func TestPrepareInline_FilterNormalizeSortClamp(t *testing.T) {
	p, _ := newTestPoster(t, newFakePlatform())
	rep := mkRep("t-1")
	rep.Findings = []domain.Finding{
		{File: "a.go", Line: 0, Severity: "high", Comment: "无行号"},            // 剔除
		{File: "", Line: 3, Severity: "high", Comment: "无文件"},                // 剔除
		{File: "pkg/a.go", Line: 10, Severity: "medium", Comment: "精确命中"},    // 保留
		{File: "b.go", Line: 20, Severity: "high", Comment: "尾段归一化"},         // → pkg/b.go (high)
		{File: "README.md", Line: 1, Severity: "low", Comment: "非 Included"}, // uniqueSuffix 只在 Included 里找 → 无命中剔除
		{File: "ghost.go", Line: 5, Severity: "high", Comment: "幻觉路径"},       // 剔除
	}
	comments := p.prepareInline(mkTask("t-1"), mkDiff(), &rep)

	if len(comments) != 2 {
		t.Fatalf("应保留 2 条，实际 %d: %+v", len(comments), comments)
	}
	// 严重度降序：high（尾段归一）先于 medium（Rank 只认 high/medium/low 词表）
	if comments[0].Path != "pkg/b.go" || comments[0].Line != 20 {
		t.Fatalf("首条应是归一化后的 high: %+v", comments[0])
	}
	if comments[1].Path != "pkg/a.go" {
		t.Fatalf("次条应是精确命中: %+v", comments[1])
	}
	// 路径已归一
	if !strings.Contains(comments[0].Body, "尾段归一化") {
		t.Fatalf("评论正文应含原意见: %q", comments[0].Body)
	}
	// 剔除 2 条（README/ghost 在 diff 校验阶段）→ Degraded 注明；
	// 无行号/无文件在更早的候选过滤已丢，不计入路径剔除数。
	if len(rep.Degraded) == 0 || !strings.Contains(rep.Degraded[0], "2 条") {
		t.Fatalf("剔除应进 Degraded: %v", rep.Degraded)
	}
}

// 唯一尾段匹配：多命中 / 段边界防误配（pcap.go 不配 pcapDownload.go）。
func TestPrepareInline_SuffixAmbiguityKeptOut(t *testing.T) {
	p, _ := newTestPoster(t, newFakePlatform())
	rep := mkRep("t-1")
	rep.Findings = []domain.Finding{
		{File: "a.go", Line: 1, Severity: "high", Comment: "多义"},
	}
	diff := &plugin.DiffContext{Files: []plugin.DiffFile{
		{Path: "x/a.go", Included: true},
		{Path: "y/a.go", Included: true},
	}}
	comments := p.prepareInline(mkTask("t-1"), diff, &rep)
	if len(comments) != 0 {
		t.Fatalf("多命中应剔除（错发位置比不发更糟）: %+v", comments)
	}
	// 段边界：a.go 不应误配 ba.go
	rep2 := mkRep("t-1")
	rep2.Findings = []domain.Finding{{File: "a.go", Line: 1, Severity: "high", Comment: "段边界"}}
	diff2 := &plugin.DiffContext{Files: []plugin.DiffFile{
		{Path: "pkg/ba.go", Included: true},
	}}
	if cs := p.prepareInline(mkTask("t-1"), diff2, &rep2); len(cs) != 0 {
		t.Fatalf("段边界外后缀不得匹配: %+v", cs)
	}
}

// max_comments 钳制：只保留严重度 top N，其余进 Degraded。
func TestPrepareInline_MaxCommentsClamp(t *testing.T) {
	p, _ := newTestPoster(t, newFakePlatform())
	rep := mkRep("t-1")
	rep.MaxInlineComments = 1
	rep.Findings = []domain.Finding{
		{File: "pkg/a.go", Line: 1, Severity: "low", Comment: "low"},
		{File: "pkg/b.go", Line: 2, Severity: "high", Comment: "high"},
	}
	comments := p.prepareInline(mkTask("t-1"), mkDiff(), &rep)
	if len(comments) != 1 {
		t.Fatalf("应钳到 1 条: %+v", comments)
	}
	if !strings.Contains(comments[0].Body, "high") {
		t.Fatalf("应保留 high: %q", comments[0].Body)
	}
	clamped := false
	for _, d := range rep.Degraded {
		if strings.Contains(d, "max_comments=1") {
			clamped = true
		}
	}
	if !clamped {
		t.Fatalf("钳制应进 Degraded: %v", rep.Degraded)
	}
}

// ---------- sendReview 三级降级 ----------

// verdict → 评审事件映射；成功返回行内条数并落 review_posted。
func TestSendReview_VerdictMappingAndMark(t *testing.T) {
	for verdict, wantEvent := range map[string]string{
		"request_changes": "REQUEST_CHANGES",
		"approve":         "APPROVE",
		"comment":         "COMMENT",
		"":                "COMMENT",
	} {
		fp := newFakePlatform()
		p, store := newTestPoster(t, fp)
		task := mkTask("t-" + wantEvent)
		if err := store.CreateTask(context.Background(), task); err != nil {
			t.Fatal(err)
		}
		rep := mkRep(task.ID)
		rep.VerdictText = "结论"
		n := p.sendReview(context.Background(), mkPR(), rep, verdict,
			[]provider.ReviewComment{{Path: "pkg/a.go", Line: 1, Body: "x"}})
		if n != 1 {
			t.Fatalf("verdict=%q 成功应返回 1 条行内: %d", verdict, n)
		}
		if got := fp.lastReview().Event; got != wantEvent {
			t.Fatalf("verdict=%q → event=%s want %s", verdict, got, wantEvent)
		}
		got, err := store.GetTask(context.Background(), task.ID)
		if err != nil || got == nil || !got.ReviewPosted {
			t.Fatalf("应落 review_posted: %v %+v", err, got)
		}
	}
}

// review_posted 库标记短路：不再 CreateReview。
func TestSendReview_ShortCircuitByFlag(t *testing.T) {
	fp := newFakePlatform()
	p, store := newTestPoster(t, fp)
	task := mkTask("t-flag")
	if err := store.CreateTask(context.Background(), task); err != nil {
		t.Fatal(err)
	}
	if err := store.MarkReviewPosted(context.Background(), task.ID); err != nil {
		t.Fatal(err)
	}
	rep := mkRep(task.ID)
	if n := p.sendReview(context.Background(), mkPR(), rep, "comment", nil); n != 0 {
		t.Fatalf("短路应返回 0: %d", n)
	}
	if fp.reviewCount() != 0 {
		t.Fatalf("短路后不应 CreateReview: %d", fp.reviewCount())
	}
}

// 平台锚点查询回退：ListReviews 已有同 task_id 评审 → 跳过。
func TestSendReview_ShortCircuitByAnchor(t *testing.T) {
	fp := newFakePlatform()
	fp.reviews = []provider.Review{
		{ID: 1, Body: "无关"},
		{ID: 2, Body: "x <!-- argus:review task_id=t-anc head=sha-1 verdict=comment -->"},
	}
	p, store := newTestPoster(t, fp)
	task := mkTask("t-anc")
	if err := store.CreateTask(context.Background(), task); err != nil {
		t.Fatal(err)
	}
	rep := mkRep(task.ID)
	if n := p.sendReview(context.Background(), mkPR(), rep, "comment", nil); n != 0 {
		t.Fatalf("锚点命中应跳过: %d", n)
	}
	if fp.reviewCount() != 0 {
		t.Fatalf("锚点命中后不应 CreateReview: %d", fp.reviewCount())
	}
}

// 三级降级：blocking 拒绝 → COMMENT → 无行内纯评论。
func TestSendReview_ThreeLevelFallback(t *testing.T) {
	fp := newFakePlatform()
	blocking := fmt.Errorf("reject your own pull is not allowed")
	inlineBad := fmt.Errorf("422: comment is invalid")
	fp.reviewErrs = []error{blocking, inlineBad, nil} // ①②败 ③成
	p, store := newTestPoster(t, fp)
	task := mkTask("t-fb")
	if err := store.CreateTask(context.Background(), task); err != nil {
		t.Fatal(err)
	}
	rep := mkRep(task.ID)
	rep.VerdictText = "⛔ 阻塞"
	comments := []provider.ReviewComment{{Path: "pkg/a.go", Line: 1, Body: "x"}}

	n := p.sendReview(context.Background(), mkPR(), rep, "request_changes", comments)
	if n != 0 {
		t.Fatalf("降级路径不计行内成功数（整单含 comments 的调用失败）: %d", n)
	}
	if fp.reviewCount() != 3 {
		t.Fatalf("应尝试 3 次 CreateReview: %d", fp.reviewCount())
	}
	last := fp.lastReview()
	if last.Event != "COMMENT" {
		t.Fatalf("最终应为 COMMENT: %s", last.Event)
	}
	if len(last.Comments) != 0 {
		t.Fatalf("兜底应去掉行内 comments: %+v", last.Comments)
	}
	if !strings.Contains(last.Body, "平台限制") {
		t.Fatalf("降级说明应保留: %q", last.Body)
	}
}

// 全部降级失败 → 0 条且不 panic（report 面已计失败）。
func TestSendReview_AllFallbacksFail(t *testing.T) {
	fp := newFakePlatform()
	always := fmt.Errorf("boom")
	fp.reviewErrs = []error{always, always, always}
	p, store := newTestPoster(t, fp)
	task := mkTask("t-all-fail")
	if err := store.CreateTask(context.Background(), task); err != nil {
		t.Fatal(err)
	}
	rep := mkRep(task.ID)
	if n := p.sendReview(context.Background(), mkPR(), rep, "comment", nil); n != 0 {
		t.Fatalf("全失败应返回 0: %d", n)
	}
	if fp.reviewCount() != 3 {
		t.Fatalf("仍应尝试三次: %d", fp.reviewCount())
	}
}
