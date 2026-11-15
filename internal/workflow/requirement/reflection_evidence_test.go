package requirement

import (
	"context"
	"strings"
	"testing"
)

// ---------- 证据门（ARGUS_REFLECTION_EVIDENCE） ----------

// off：缺 evidence 不校验（与 v3.4 行为一致）。
func TestEvidenceGate_OffKeepsFindings(t *testing.T) {
	m := &reflectFakeModel{
		draftReplies: []string{`[{"id":0,"file":"a.go","line":10,"severity":"high","comment":"空指针解引用"}]`},
		verdicts:     []string{`{"pass":true,"issues":[]}`},
	}
	w := &Workflow{ReflectRounds: 1, ReflectModel: m, ReflectEvidence: EvidenceOff, Log: nopLogger{}}
	task, merged := reflectTaskFixture()
	info := w.reflectFindings(context.Background(), task, "", merged)
	if info == nil || len(merged.Findings) != 1 || info.DroppedNoEvidence != 0 {
		t.Fatalf("off 不应触发证据门: findings=%d info=%+v", len(merged.Findings), info)
	}
}

// strict：缺 evidence → 剔除 + 记 RemovedPrints（regret 信号）。
func TestEvidenceGate_StrictDropsMissingEvidence(t *testing.T) {
	m := &reflectFakeModel{
		draftReplies: []string{`[
			{"id":0,"file":"a.go","line":10,"severity":"high","comment":"空指针解引用"},
			{"id":1,"file":"b.go","line":3,"severity":"medium","comment":"变量命名不贴合语义","evidence":"b.go 第 3 行附近","counterexample":"若仅是风格偏好则为误报"}
		]`},
		verdicts: []string{`{"pass":true,"issues":[]}`},
	}
	w := &Workflow{ReflectRounds: 1, ReflectModel: m, ReflectEvidence: EvidenceStrict, Log: nopLogger{}}
	task, merged := reflectTaskFixture()
	info := w.reflectFindings(context.Background(), task, "", merged)
	if info == nil {
		t.Fatal("应返回摘要")
	}
	if info.DroppedNoEvidence != 1 || len(merged.Findings) != 1 {
		t.Fatalf("strict 应丢弃缺证据条: dropped=%d findings=%d info=%+v",
			info.DroppedNoEvidence, len(merged.Findings), info)
	}
	if len(info.RemovedPrints) == 0 {
		t.Fatal("strict 丢弃应记 RemovedPrints 供 regret 入库")
	}
	if merged.Findings[0].File != "b.go" {
		t.Fatalf("应保留有证据的 b.go，got %s", merged.Findings[0].File)
	}
}

// soft：缺 evidence → 强制 low + 标注，不剔除、不因证据门记 regret。
func TestEvidenceGate_SoftDegrades(t *testing.T) {
	m := &reflectFakeModel{
		draftReplies: []string{`[
			{"id":0,"file":"a.go","line":10,"severity":"high","comment":"空指针解引用","counterexample":"若有空则不成立"},
			{"id":1,"file":"b.go","line":3,"severity":"low","comment":"变量命名不贴合语义","evidence":"b.go:3","counterexample":"若命名约定允许则不成立"}
		]`},
		verdicts: []string{`{"pass":true,"issues":[]}`},
	}
	w := &Workflow{ReflectRounds: 1, ReflectModel: m, ReflectEvidence: EvidenceSoft, Log: nopLogger{}}
	task, merged := reflectTaskFixture()
	info := w.reflectFindings(context.Background(), task, "", merged)
	if info == nil || info.DegradedNoEvidence != 1 || len(merged.Findings) != 2 {
		t.Fatalf("soft 应降级不删: %+v findings=%d", info, len(merged.Findings))
	}
	f := merged.Findings[0]
	if f.Severity != "low" || !strings.Contains(f.Comment, "证据门") {
		t.Fatalf("应降为 low 并标注: %+v", f)
	}
	if info.Removed != 0 {
		t.Fatalf("soft 不应剔除任何条: removed=%d prints=%v", info.Removed, info.RemovedPrints)
	}
}

// evidence 指向清单外文件 → strict 剔除。
func TestEvidenceGate_StrictPathOutOfScope(t *testing.T) {
	m := &reflectFakeModel{
		draftReplies: []string{`[
			{"id":0,"file":"a.go","line":10,"severity":"high","comment":"空指针解引用",
			 "evidence":"见 evil/not_in_diff.go 的闭包","counterexample":"若不在变更范围则误报"}
		]`},
		verdicts: []string{`{"pass":true,"issues":[]}`},
	}
	w := &Workflow{ReflectRounds: 1, ReflectModel: m, ReflectEvidence: EvidenceStrict, Log: nopLogger{}}
	task, merged := reflectTaskFixture()
	info := w.reflectFindings(context.Background(), task, "", merged)
	if info == nil || info.DroppedNoEvidence != 1 || len(merged.Findings) != 0 {
		t.Fatalf("路径越界应剔除: %+v findings=%d", info, len(merged.Findings))
	}
}

// 有合法 evidence + counterexample → 保留，且不触发门。
func TestEvidenceGate_PassWithFullEvidence(t *testing.T) {
	m := &reflectFakeModel{
		draftReplies: []string{`[
			{"id":0,"file":"a.go","line":10,"severity":"high","comment":"空指针解引用",
			 "evidence":"a.go 第 10 行 req 未判空即解引用","counterexample":"若上游已保证非空则不成立，但本路径无校验"}
		]`},
		verdicts: []string{`{"pass":true,"issues":[]}`},
	}
	w := &Workflow{ReflectRounds: 1, ReflectModel: m, ReflectEvidence: EvidenceStrict, Log: nopLogger{}}
	task, merged := reflectTaskFixture()
	info := w.reflectFindings(context.Background(), task, "", merged)
	if info == nil || len(merged.Findings) != 1 || info.DroppedNoEvidence != 0 {
		t.Fatalf("完整证据应通过: %+v findings=%d", info, len(merged.Findings))
	}
}

func TestEvidenceFailureHelpers(t *testing.T) {
	changed := map[string]bool{"a.go": true}
	if r := evidenceFailure(reflectFindingWire{}, "a.go", changed, false); r != "missing_evidence" {
		t.Fatalf("空 evidence: %q", r)
	}
	if r := evidenceFailure(reflectFindingWire{Evidence: "x"}, "a.go", changed, false); r != "missing_counterexample" {
		t.Fatalf("空 counter: %q", r)
	}
	if r := evidenceFailure(reflectFindingWire{Evidence: "见 evil/x.go", Counter: "y"}, "a.go", changed, false); r == "" {
		t.Fatal("路径越界应失败")
	}
	if r := evidenceFailure(reflectFindingWire{Evidence: "a.go 第 1 行", Counter: "y"}, "a.go", changed, false); r != "" {
		t.Fatalf("合法路径应通过: %q", r)
	}
	// 清单为空时不检查路径
	if r := evidenceFailure(reflectFindingWire{Evidence: "任意说明", Counter: "y"}, "a.go", nil, true); r != "" {
		t.Fatalf("空清单应通过: %q", r)
	}
}

// 深挖修复回归：strict 证据门要求 evidence 至少含一个路径样 token——纯文字
// 描述（"问题确实存在"）不再无条件放行；soft 模式不受影响。
func TestEvidenceGate_StrictRequiresPathToken(t *testing.T) {
	changed := map[string]bool{"a.go": true}
	if r := evidenceFailure(reflectFindingWire{Evidence: "问题确实存在，很严重", Counter: "反例已排除"}, "a.go", changed, true); r != "evidence_without_path_reference" {
		t.Fatalf("strict 下纯文字证据应被拒，got %q", r)
	}
	if r := evidenceFailure(reflectFindingWire{Evidence: "见 a.go:12 的判空缺失", Counter: "反例已排除"}, "a.go", changed, true); r != "" {
		t.Fatalf("含路径引用的证据应通过，got %q", r)
	}
	if r := evidenceFailure(reflectFindingWire{Evidence: "问题确实存在", Counter: "反例已排除"}, "a.go", changed, false); r != "" {
		t.Fatalf("soft 模式不受路径 token 要求影响，got %q", r)
	}
}
