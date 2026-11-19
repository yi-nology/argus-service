package requirement

import (
	"context"
	"strings"
	"testing"

	"github.com/oagentkit/textx/reportutil"

	"github.com/yi-nology/argus/internal/dispatch"
	"github.com/yi-nology/argus/internal/domain"
	"github.com/yi-nology/argus/internal/obs"
	"github.com/yi-nology/argus/internal/plugin"
)

// acSpec 构造 R1/R3 用例数据：每个 acStatus 生成一条 AC 与对应 check。
func acSpec(acStatuses ...string) (*plugin.RequirementSpec, *plugin.ComplianceReport) {
	spec := &plugin.RequirementSpec{}
	rep := &plugin.ComplianceReport{}
	for i, st := range acStatuses {
		id := "AC-" + string(rune('1'+i))
		spec.ACs = append(spec.ACs, plugin.AcceptanceCriterion{ID: id, Text: "需求 " + id})
		rep.Checks = append(rep.Checks, plugin.ACCheck{ID: id, Status: st, Evidence: "a.go:1"})
	}
	return spec, rep
}

func finding(sev string) domain.Finding {
	return domain.Finding{File: "a.go", Line: 1, Severity: sev, Comment: "x"}
}

// v3.8.0 回归：VerdictScoped 的 scope 闸门——越界 high 不触发 R5.2（降计
// COMMENT 级并报越界数）；nil scope 与 Verdict 完全等价。
func TestVerdictScoped(t *testing.T) {
	scope := map[string]bool{"a.go": true}
	inScope := []domain.Finding{{File: "a.go", Line: 1, Severity: "high", Comment: "x"}}
	outOfScopeF := []domain.Finding{{File: "unrelated.go", Line: 9, Severity: "high", Comment: "y"}}

	if v, rule, n := VerdictScoped(nil, nil, inScope, scope); v != VerdictRequestChanges || rule != "R5.2" || n != 0 {
		t.Fatalf("范围内 high 应照常 R5.2: %s %s n=%d", v, rule, n)
	}
	if v, rule, n := VerdictScoped(nil, nil, outOfScopeF, scope); v != VerdictComment || rule != "R5.4" || n != 1 {
		t.Fatalf("越界 high 应降级 COMMENT 且计数: %s %s n=%d", v, rule, n)
	}
	// file 为空的意见不受 scope 闸门影响（保守：无法判定归属时维持原语义）
	if _, rule, n := VerdictScoped(nil, nil, []domain.Finding{{File: "", Severity: "high"}}, scope); rule != "R5.2" || n != 0 {
		t.Fatalf("无 file 的 high 不应被 scope 闸门拦截: rule=%s n=%d", rule, n)
	}
	// nil scope（inScope=nil）不受闸门影响：裁决不变且越界计数恒 0
	if _, _, n := VerdictScoped(nil, nil, outOfScopeF, nil); n != 0 {
		t.Fatalf("nil scope 越界计数应恒 0, got %d", n)
	}
}

// TestVerdictTruthTable R5 裁决真值表（design §4）——按序求值首条命中即短路。
func TestVerdictTruthTable(t *testing.T) {
	t.Run("R5.1_missing_一票否决", func(t *testing.T) {
		s, r := acSpec(plugin.ACImplemented, plugin.ACMissing)
		v, rule, _ := VerdictScoped(s, r, nil, nil)
		if v != "request_changes" || rule != "R5.1" {
			t.Fatalf("got %s/%s", v, rule)
		}
	})
	t.Run("R5.1_优先于R5.2_high", func(t *testing.T) {
		s, r := acSpec(plugin.ACMissing)
		v, rule, _ := VerdictScoped(s, r, []domain.Finding{finding("high")}, nil)
		if v != "request_changes" || rule != "R5.1" {
			t.Fatalf("want R5.1 短路，got %s/%s", v, rule)
		}
	})
	t.Run("R5.2_high_finding", func(t *testing.T) {
		s, r := acSpec(plugin.ACImplemented)
		v, rule, _ := VerdictScoped(s, r, []domain.Finding{finding("medium"), finding("high")}, nil)
		if v != "request_changes" || rule != "R5.2" {
			t.Fatalf("got %s/%s", v, rule)
		}
	})
	t.Run("R5.3_unverifiable", func(t *testing.T) {
		s, r := acSpec(plugin.ACImplemented, plugin.ACUnverifiable)
		v, rule, _ := VerdictScoped(s, r, nil, nil)
		if v != "comment" || rule != "R5.3" {
			t.Fatalf("got %s/%s", v, rule)
		}
	})
	t.Run("R5.3_模糊需求", func(t *testing.T) {
		s := &plugin.RequirementSpec{Ambiguous: []string{"原文"}}
		v, rule, _ := VerdictScoped(s, nil, nil, nil)
		if v != "comment" || rule != "R5.3" {
			t.Fatalf("got %s/%s", v, rule)
		}
	})
	t.Run("R5.3_空清单", func(t *testing.T) {
		v, rule, _ := VerdictScoped(&plugin.RequirementSpec{}, &plugin.ComplianceReport{}, nil, nil)
		if v != "comment" || rule != "R5.3" {
			t.Fatalf("got %s/%s", v, rule)
		}
	})
	t.Run("R5.3_scope_creep", func(t *testing.T) {
		s, r := acSpec(plugin.ACImplemented)
		r.ScopeCreep = []plugin.ScopeCreepItem{{File: "extra.go", Note: "需求外"}}
		v, rule, _ := VerdictScoped(s, r, nil, nil)
		if v != "comment" || rule != "R5.3" {
			t.Fatalf("got %s/%s", v, rule)
		}
	})
	t.Run("R5.4_medium_low", func(t *testing.T) {
		s, r := acSpec(plugin.ACImplemented)
		v, rule, _ := VerdictScoped(s, r, []domain.Finding{finding("low")}, nil)
		if v != "comment" || rule != "R5.4" {
			t.Fatalf("got %s/%s", v, rule)
		}
	})
	t.Run("R5.5_全绿通过", func(t *testing.T) {
		s, r := acSpec(plugin.ACImplemented)
		v, rule, _ := VerdictScoped(s, r, nil, nil)
		if v != "approve" || rule != "R5.5" {
			t.Fatalf("got %s/%s", v, rule)
		}
	})
	t.Run("code_review_only_无清单_high", func(t *testing.T) {
		v, rule, _ := VerdictScoped(nil, nil, []domain.Finding{finding("high")}, nil)
		if v != "request_changes" || rule != "R5.2" {
			t.Fatalf("got %s/%s", v, rule)
		}
	})
	t.Run("code_review_only_无清单_干净通过", func(t *testing.T) {
		v, rule, _ := VerdictScoped(nil, nil, nil, nil)
		if v != "approve" || rule != "R5.5" {
			t.Fatalf("got %s/%s", v, rule)
		}
	})
}

// ---------- 无需求类变更豁免（v3.143.0） ----------

// TestVerdict_BenignEmptyACExemption docs/tests-only + 零意见 + AC=0 → APPROVE；
// 任何代码路径/意见/模糊/scope creep 都关掉豁免——注入洞保持封死。
func TestVerdict_BenignEmptyACExemption(t *testing.T) {
	spec := &plugin.RequirementSpec{} // AC=0，无 Ambiguous
	docsOnly := map[string]bool{"README.md": true, "docs/guide.md": true}
	testsOnly := map[string]bool{"internal/x/a_test.go": true, "tests/b.py": true}
	mixed := map[string]bool{"docs/guide.md": true, "main.go": true}

	// 豁免命中：docs-only / tests-only
	if v, r, _ := VerdictScoped(spec, nil, nil, docsOnly); v != VerdictApprove || r != "R5.5" {
		t.Errorf("docs-only AC=0 应 APPROVE/R5.5, got %s/%s", v, r)
	}
	if v, r, _ := VerdictScoped(spec, nil, nil, testsOnly); v != VerdictApprove || r != "R5.5" {
		t.Errorf("tests-only AC=0 应 APPROVE/R5.5, got %s/%s", v, r)
	}
	// 带代码路径：豁免关闭，AC=0 → R5.3（注入洞封死）
	if v, r, _ := VerdictScoped(spec, nil, nil, mixed); v != VerdictComment || r != "R5.3" {
		t.Errorf("混合代码路径应 R5.3 COMMENT, got %s/%s", v, r)
	}
	// inScope 缺失（无 diff 文件集）：保守 R5.3
	if v, r, _ := VerdictScoped(spec, nil, nil, nil); v != VerdictComment || r != "R5.3" {
		t.Errorf("inScope nil 应保守 R5.3, got %s/%s", v, r)
	}
	// 有意见（哪怕 low）：豁免关闭 → AC=0 先短路 R5.3（有意见的 docs 变更同样
	// 落人工确认，比 R5.4 更保守，语义正确）
	low := []domain.Finding{{File: "README.md", Line: 1, Severity: reportutil.Low, Comment: "typo"}}
	if v, r, _ := VerdictScoped(spec, nil, low, docsOnly); v != VerdictComment || r != "R5.3" {
		t.Errorf("docs-only 带 low 意见应 R5.3, got %s/%s", v, r)
	}
	// 模糊需求：豁免关闭 → R5.3
	amb := &plugin.RequirementSpec{Ambiguous: []string{"说不清"}}
	if v, r, _ := VerdictScoped(amb, nil, nil, docsOnly); v != VerdictComment || r != "R5.3" {
		t.Errorf("Ambiguous 应压过豁免 R5.3, got %s/%s", v, r)
	}
	// scope creep：豁免关闭 → R5.3
	rep := &plugin.ComplianceReport{ScopeCreep: []plugin.ScopeCreepItem{{}}}
	if v, r, _ := VerdictScoped(spec, rep, nil, docsOnly); v != VerdictComment || r != "R5.3" {
		t.Errorf("scope creep 应压过豁免 R5.3, got %s/%s", v, r)
	}
}

// TestBenignPath 路径分类边界：配置/锁文件/二进制不是无需求类（golden 契约
// 41-binary-lockfile/60-config-yaml 期望 COMMENT，不得被豁免放行）。
func TestBenignPath(t *testing.T) {
	yes := []string{"README.md", "docs/a.md", "DOCs/b.MD", "CHANGELOG", "x_test.go",
		"tests/y.py", "test/z.js", "a/b.test.ts", "testdata/f.txt", "guide.rst"}
	for _, p := range yes {
		if !benignPath(p) {
			t.Errorf("%q 应为无需求类", p)
		}
	}
	no := []string{"main.go", "config.yaml", "deploy/app.yml", "go.sum", "package-lock.json",
		"a.bin", "Makefile", "src/x.ts", "test_helper.go", ".env"}
	for _, p := range no {
		if benignPath(p) {
			t.Errorf("%q 不应为无需求类", p)
		}
	}
}

// TestJudgeAndGateLLMFailoverGate R5.L 门禁（P2-04，#15 回归）：本轮审查期间
// LLM 主备切换发生 → APPROVE 降级为 COMMENT（R5.L）+ Degraded 注明；无切换时
// 既有裁决语义不回退（正常路径仍 R5.5 APPROVE）。
func TestJudgeAndGateLLMFailoverGate(t *testing.T) {
	base := obs.LLMFailoverTotal()
	w := &Workflow{CompletenessPolicy: plugin.CompletenessDemote, llmFailoverBase: base}
	task := plugin.Task{PR: plugin.PRContext{Platform: "gitea"}, Kind: "code_review"}
	// 一个实际产出的插件 outcome（否则 R5.0 零插件守门先拦，测不到 R5.L）
	ran := []dispatch.Outcome{{Name: "correctness"}}

	// 无降级：全绿路径 APPROVE（既有语义不回退）
	verdict, rule, _ := w.judgeAndGate(context.Background(), task, nil, &plugin.ComplianceReport{},
		&plugin.MergedFindings{}, nil, ran, nil, nil)
	if verdict != VerdictApprove || rule != "R5.5" {
		t.Fatalf("无降级路径裁决失真: %s/%s（期望 approve/R5.5）", verdict, rule)
	}

	// 注入 LLM 降级（本轮 run 期间主备切换 2 次）→ APPROVE 被拦
	obs.IncrLLMFailover(context.Background(), "r3")
	obs.IncrLLMFailover(context.Background(), "r1")
	merged := &plugin.MergedFindings{}
	verdict, rule, _ = w.judgeAndGate(context.Background(), task, nil, &plugin.ComplianceReport{}, merged,
		nil, ran, nil, nil)
	if verdict != VerdictComment || rule != "R5.L" {
		t.Fatalf("LLM 降级下 APPROVE 未被拦: %s/%s（期望 comment/R5.L）", verdict, rule)
	}
	found := false
	for _, d := range merged.Degraded {
		if strings.Contains(d, "LLM 主备切换 2 次") {
			found = true
		}
	}
	if !found {
		t.Fatalf("降级原因未结构化透出: %v", merged.Degraded)
	}
}

// TestJudgeAndGateVisualCommentOnly 批 G 误报控制（spec §2.3）：visual_check
// 任务 verdict 上限 comment——high 意见驱动的 request_changes 被钳制为
// comment/R5.G + Degraded 注明（人工终审）；code_review 不受影响。
func TestJudgeAndGateVisualCommentOnly(t *testing.T) {
	w := &Workflow{CompletenessPolicy: plugin.CompletenessDemote}
	ran := []dispatch.Outcome{{Name: "correctness"}}
	high := &plugin.MergedFindings{Findings: []domain.Finding{
		{File: "ui/page.tsx", Line: 10, Severity: "high", Comment: "布局与设计稿不符"},
	}}

	// visual_check：high 驱动的 request_changes → comment/R5.G
	vtask := plugin.Task{PR: plugin.PRContext{Platform: "gitea"}, Kind: domain.TaskKindVisualCheck}
	verdict, rule, _ := w.judgeAndGate(context.Background(), vtask, nil, &plugin.ComplianceReport{}, high,
		nil, ran, nil, nil)
	if verdict != VerdictComment || rule != "R5.G" {
		t.Fatalf("visual_check 应钳 comment/R5.G, got %s/%s", verdict, rule)
	}
	found := false
	for _, d := range high.Degraded {
		if strings.Contains(d, "comment-only") {
			found = true
		}
	}
	if !found {
		t.Fatalf("Degraded 应注明 comment-only: %v", high.Degraded)
	}

	// code_review 对照：同样 high 正常 request_changes（不受 R5.G 影响）
	cregory := &plugin.MergedFindings{Findings: []domain.Finding{
		{File: "auth/login.go", Line: 10, Severity: "high", Comment: "缺判空"},
	}}
	ctask := plugin.Task{PR: plugin.PRContext{Platform: "gitea"}, Kind: domain.TaskKindCodeReview}
	verdict2, _, _ := w.judgeAndGate(context.Background(), ctask, nil, &plugin.ComplianceReport{}, cregory,
		nil, ran, nil, nil)
	if verdict2 != VerdictRequestChanges {
		t.Fatalf("code_review 不应被 R5.G 钳制, got %s", verdict2)
	}
}
