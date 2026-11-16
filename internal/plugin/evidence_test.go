package plugin

import (
	"io/fs"
	"strings"
	"testing"

	"github.com/yi-nology/argus/internal/domain"
)

// TestParseEvidenceRefs 自由文本解析 file:line（含中文/多引用/无引用三形态）。
func TestParseEvidenceRefs(t *testing.T) {
	refs := ParseEvidenceRefs("改动侧 src/foo.go:12 与消费侧 src/bar.go:34 不一致")
	if len(refs) != 2 {
		t.Fatalf("应解析 2 条引用, got %d", len(refs))
	}
	if refs[0].File != "src/foo.go" || refs[0].Line != 12 {
		t.Fatalf("refs[0] 解析错: %+v", refs[0])
	}
	// 纯文字（差距说明）无引用
	if got := ParseEvidenceRefs("声明了但代码没有实现该能力"); len(got) != 0 {
		t.Fatalf("纯文字应无引用, got %+v", got)
	}
	// ASCII 路径解析（中文路径 best-effort 不覆盖——\w 不含 UTF-8 中文，缺失不阻断）
	refs2 := ParseEvidenceRefs("见 docs/guide.md:5 与 cmd/x.go:1")
	if len(refs2) != 2 {
		t.Fatalf("复杂路径应解析 2 条, got %+v", refs2)
	}
}

// TestVerifyClaimEvidence 交叉校验：改动集内可验证/集外标 ⚠️/纯文字不标。
func TestVerifyClaimEvidence(t *testing.T) {
	changed := []string{"src/foo.go", "src/bar.go"}
	touched := []string{"src/other.go"}

	c := Claim{Claim: "X", Evidence: "src/foo.go:12 与 src/bar.go:34 不一致", Status: ClaimInconsistent}
	res := VerifyClaimEvidence(c, changed, touched)
	if !res.Verified {
		t.Fatalf("改动集内引用应验证通过: %+v", res)
	}

	c2 := Claim{Claim: "Y", Evidence: "external/lib.go:99 缺失", Status: ClaimMissing}
	res2 := VerifyClaimEvidence(c2, changed, touched)
	if res2.Verified {
		t.Fatalf("集外引用不应验证通过: %+v", res2)
	}
	if !contains(res2.Note, "⚠️ 证据待验证") {
		t.Fatalf("证据待验证注记缺失: %q", res2.Note)
	}

	// 行号无效（line=0）也算不可验证
	c3 := Claim{Claim: "Z", Evidence: "src/foo.go:abc", Status: ClaimMissing}
	res3 := VerifyClaimEvidence(c3, changed, touched)
	if len(res3.Refs) != 0 && res3.Verified {
		t.Fatalf("行号无效引用不应验证通过: %+v", res3)
	}

	// 纯文字不算证据缺陷
	c4 := Claim{Claim: "W", Evidence: "差距：模块 X 未与 Y 对齐", Status: ClaimMissing}
	res4 := VerifyClaimEvidence(c4, changed, touched)
	if !res4.Verified || res4.Note != "" {
		t.Fatalf("纯文字应无缺陷: %+v", res4)
	}
}

// TestVerifyClaimsEvidence 高风险降级 + 软标注（不丢弃声明）。
func TestVerifyClaimsEvidence(t *testing.T) {
	changed := []string{"src/foo.go"}
	touched := []string{"src/touch.go"}
	sec := &AnalystSections{Claims: []Claim{
		{Claim: "a", Evidence: "src/foo.go:1", Status: ClaimMissing},
		{Claim: "b", Evidence: "external/x.go:2", Status: ClaimMissing},
		{Claim: "c", Evidence: "src/touch.go:3", Status: ClaimImplemented},
	}}
	verified, total := VerifyClaimsEvidence(sec, changed, touched)
	if total != 3 {
		t.Fatalf("total=%d want 3", total)
	}
	if verified != 2 {
		t.Fatalf("verified=%d want 2", verified)
	}
	// a 可验证（missing 保留）
	if sec.Claims[0].Status != ClaimMissing {
		t.Fatalf("可验证 missing 不应降级: %v", sec.Claims[0])
	}
	// b 集外 + 高风险 missing → 降级 partial，Evidence 追加 ⚠️
	if sec.Claims[1].Status != ClaimPartial {
		t.Fatalf("不可验证 missing 应降级 partial: %v", sec.Claims[1])
	}
	if !contains(sec.Claims[1].Evidence, "⚠️ 证据待验证") {
		t.Fatalf("应追加 ⚠️ 注记: %q", sec.Claims[1].Evidence)
	}
	// c 可验证 implemented 不动
	if sec.Claims[2].Status != ClaimImplemented {
		t.Fatalf("implemented 不应变: %v", sec.Claims[2])
	}
}

// contains 简易子串（测试辅助）。
func contains(s, sub string) bool {
	return len(s) >= len(sub) && (func() bool {
		for i := 0; i+len(sub) <= len(s); i++ {
			if s[i:i+len(sub)] == sub {
				return true
			}
		}
		return false
	}())
}

// TestVerifyFindingEvidence domain.Finding 级证据交叉校验（v6.18.0，与 Claim 级同口径）：
// 集内引用可验证 / 集外引用标 ⚠️ / 无引用（空或纯文字）不判。
func TestVerifyFindingEvidence(t *testing.T) {
	changed := []string{"src/foo.go"}
	touched := []string{"src/touch.go"}

	f := domain.Finding{File: "src/foo.go", Line: 12, Severity: "high", Comment: "缺判空",
		Evidence: "src/foo.go:12 未做空值检查"}
	res := VerifyFindingEvidence(f, changed, touched, nil)
	if !res.Verified || res.Status != domain.EvidenceStructuralValid {
		t.Fatalf("改动集内引用（集合级）应 structural_valid: %+v", res)
	}

	f2 := domain.Finding{File: "src/foo.go", Line: 20, Severity: "high", Comment: "越权",
		Evidence: "external/lib.go:99 未鉴权"}
	res2 := VerifyFindingEvidence(f2, changed, touched, nil)
	if res2.Verified || res2.Status != domain.EvidenceUnverified {
		t.Fatalf("集外引用应 unverified: %+v", res2)
	}
	if !contains(res2.Note, "⚠️ 证据待验证") {
		t.Fatalf("证据待验证注记缺失: %q", res2.Note)
	}

	// 空证据（Phase 2 翻转）：unverified「未提供结构化证据」——不再叫 verified
	f3 := domain.Finding{File: "src/foo.go", Line: 1, Severity: "low", Comment: "命名"}
	res3 := VerifyFindingEvidence(f3, changed, touched, nil)
	if res3.Verified || res3.Status != domain.EvidenceUnverified {
		t.Fatalf("空证据应 unverified（Phase 2 不再不判）: %+v", res3)
	}
	if !contains(res3.Note, "未提供结构化证据") || !contains(res3.Note, evidenceWarnMark) {
		t.Fatalf("空证据注记应含措辞与 ⚠️ 锚（幂等依赖）: %q", res3.Note)
	}

	// 纯文字差距说明（无 file:line）同翻转：unverified
	f4 := domain.Finding{File: "src/foo.go", Line: 2, Severity: "medium", Comment: "口径不一",
		Evidence: "改动与既有模块口径存在差距"}
	res4 := VerifyFindingEvidence(f4, changed, touched, nil)
	if res4.Verified || res4.Status != domain.EvidenceUnverified {
		t.Fatalf("纯文字证据应 unverified: %+v", res4)
	}
}

// TestVerifyFindingsEvidence 批量：高危（critical/high）证据不可验证降级 medium
// 退出 R5 一票否决面 + ⚠️ 软标注保留线索；medium/low 只标注不降；幂等不叠加。
func TestVerifyFindingsEvidence(t *testing.T) {
	changed := []string{"src/foo.go"}
	touched := []string{"src/touch.go"}
	mf := &MergedFindings{Findings: []domain.Finding{
		// 1. 可验证 critical → 不动
		{File: "src/foo.go", Line: 1, Severity: "critical", Comment: "a",
			Evidence: "src/foo.go:1 数据损坏路径"},
		// 2. 集外 high → 降级 medium + ⚠️
		{File: "src/foo.go", Line: 2, Severity: "high", Comment: "b",
			Evidence: "external/lib.go:9 越权调用"},
		// 3. 集外 critical → 降级 medium
		{File: "src/foo.go", Line: 3, Severity: "critical", Comment: "c",
			Evidence: "external/x.go:1 注入面"},
		// 4. 集外 medium → 保级 + ⚠️
		{File: "src/foo.go", Line: 4, Severity: "medium", Comment: "d",
			Evidence: "external/y.go:2 边界遗漏"},
		// 5. 触达集内 high → 可验证不动
		{File: "src/foo.go", Line: 5, Severity: "high", Comment: "e",
			Evidence: "src/touch.go:7 消费侧依赖旧语义"},
		// 6. 空证据 low → 不判不动
		{File: "src/foo.go", Line: 6, Severity: "low", Comment: "f"},
	}}
	verified, total := VerifyFindingsEvidence(mf, changed, touched, nil)
	if total != 6 || verified != 2 {
		t.Fatalf("total=%d verified=%d, want 6/2（case6 空证据翻转出列）", total, verified)
	}
	if mf.Findings[0].EvidenceStatus != domain.EvidenceStructuralValid {
		t.Fatalf("集内引用应回写 structural_valid: %q", mf.Findings[0].EvidenceStatus)
	}
	if mf.Findings[1].EvidenceStatus != domain.EvidenceUnverified {
		t.Fatalf("集外引用应回写 unverified: %q", mf.Findings[1].EvidenceStatus)
	}
	fs := mf.Findings
	if fs[0].Severity != "critical" {
		t.Fatalf("可验证 critical 不应降级: %v", fs[0])
	}
	if fs[1].Severity != "medium" || !contains(fs[1].Evidence, "⚠️ 证据待验证") {
		t.Fatalf("集外 high 应降级 medium + ⚠️: sev=%q evid=%q", fs[1].Severity, fs[1].Evidence)
	}
	if fs[2].Severity != "medium" {
		t.Fatalf("集外 critical 应降级 medium: %q", fs[2].Severity)
	}
	if fs[3].Severity != "medium" || !contains(fs[3].Evidence, "⚠️ 证据待验证") {
		t.Fatalf("集外 medium 应保级 + ⚠️: sev=%q evid=%q", fs[3].Severity, fs[3].Evidence)
	}
	if fs[4].Severity != "high" || contains(fs[4].Evidence, "⚠️") {
		t.Fatalf("触达集内 high 不应动: sev=%q evid=%q", fs[4].Severity, fs[4].Evidence)
	}
	if fs[5].Severity != "low" || fs[5].EvidenceStatus != domain.EvidenceUnverified {
		t.Fatalf("空证据 low 保级但应回写 unverified: %+v", fs[5])
	}
	if !contains(fs[5].Evidence, "未提供结构化证据") {
		t.Fatalf("空证据应挂措辞标注: %q", fs[5].Evidence)
	}

	// 幂等：二次调用注记不叠加、严重度不再变
	VerifyFindingsEvidence(mf, changed, touched, nil)
	if n := strings.Count(mf.Findings[1].Evidence, "⚠️ 证据待验证"); n != 1 {
		t.Fatalf("注记应幂等只出现一次, got %d", n)
	}
	if mf.Findings[1].Severity != "medium" {
		t.Fatalf("二次调用严重度不应再变: %q", mf.Findings[1].Severity)
	}

	// nil 安全
	if v, tot := VerifyFindingsEvidence(nil, changed, touched, nil); v != 0 || tot != 0 {
		t.Fatalf("nil 应返回 0/0, got %d/%d", v, tot)
	}
}

// fakeReader 假 SourceReader：内存文件表 + 绑定版本（Phase 2 四态核验测试用）。
type fakeReader struct {
	files map[string][]string // path → 行表
	sha   string              // 绑定检出版本（"" = 无版本保证）
	err   error               // 非 nil 时 ReadLines 恒返之（基础设施失败模拟）
}

func (r *fakeReader) ReadLines(path string) ([]string, error) {
	if r.err != nil {
		return nil, r.err
	}
	lines, ok := r.files[path]
	if !ok {
		return nil, fs.ErrNotExist
	}
	return lines, nil
}

func (r *fakeReader) HeadSHA() string { return r.sha }

// TestVerifyFindingsEvidenceFourStates 批量回写+降级新规则：invalid|unverified
// 高危降级；structurally_valid 放行（不标不降——结构核验通过也退出否决面）。
func TestVerifyFindingsEvidenceFourStates(t *testing.T) {
	changed := []string{"src/foo.go"}
	r := &fakeReader{
		files: map[string][]string{"src/foo.go": {"l1", "l2"}},
		sha:   "sha-task",
	}
	mf := &MergedFindings{Findings: []domain.Finding{
		// verified critical → 放行（#68：内容对账通过才 verified）
		{File: "a.go", Line: 1, Severity: "critical", Comment: "v",
			Evidence: "src/foo.go:2 实锤", EvidenceSnippet: "l2"},
		// 第二条同有版本 reader → 亦 verified 放行（structural 中间态在
		// mf2 段用无版本 reader 单测）
		{File: "a.go", Line: 2, Severity: "critical", Comment: "s",
			Evidence: "src/foo.go:1 结构对", EvidenceSnippet: "l1"},
		// invalid high → 降级+⚠️
		{File: "a.go", Line: 3, Severity: "high", Comment: "i",
			Evidence: "src/missing.go:1 幻影"},
		// unverified high（行越界）→ 降级+⚠️
		{File: "a.go", Line: 4, Severity: "high", Comment: "u",
			Evidence: "src/foo.go:99 越界"},
	}}
	// 第二条用无版本 reader 才能出 structural——分开两次跑更清晰：
	// 先全量（r 有版本）跑第 1/3/4 条，structural 用例单测。
	verified, total := VerifyFindingsEvidence(mf, changed, nil, r)
	if total != 4 || verified != 2 {
		t.Fatalf("total=%d verified=%d, want 4/2", total, verified)
	}
	fs := mf.Findings
	if fs[0].Severity != "critical" || fs[0].EvidenceStatus != domain.EvidenceVerified {
		t.Fatalf("verified critical 应放行: %+v", fs[0])
	}
	if fs[1].Severity != "critical" || fs[1].EvidenceStatus != domain.EvidenceVerified {
		t.Fatalf("verified critical 应放行: %+v", fs[1])
	}
	if fs[2].Severity != "medium" || fs[2].EvidenceStatus != domain.EvidenceInvalid ||
		!strings.Contains(fs[2].Evidence, evidenceWarnMark) {
		t.Fatalf("invalid high 应降级+⚠️: %+v", fs[2])
	}
	if fs[3].Severity != "medium" || fs[3].EvidenceStatus != domain.EvidenceUnverified {
		t.Fatalf("unverified high 应降级: %+v", fs[3])
	}

	// structural 放行（无版本 reader）：不标不降
	mf2 := &MergedFindings{Findings: []domain.Finding{
		{File: "a.go", Line: 1, Severity: "critical", Comment: "s",
			Evidence: "src/foo.go:2 结构对但无版本", EvidenceSnippet: "l2"},
	}}
	noVer := &fakeReader{files: r.files}
	v2, _ := VerifyFindingsEvidence(mf2, changed, nil, noVer)
	if v2 != 1 || mf2.Findings[0].Severity != "critical" ||
		mf2.Findings[0].EvidenceStatus != domain.EvidenceStructuralValid ||
		strings.Contains(mf2.Findings[0].Evidence, evidenceWarnMark) {
		t.Fatalf("structural critical 应放行不标不降: %+v", mf2.Findings[0])
	}
}

// TestVerifyFindingsEvidenceNoEvidenceNoDemote Ruling 回归钉：unverified-未提供
// 结构化引用（空/纯文字）的高危只标不降——协议明许省略 evidence，全降会惩罚
// 诚实省略并激励编造 file:line 换 verified。
func TestVerifyFindingsEvidenceNoEvidenceNoDemote(t *testing.T) {
	changed := []string{"src/foo.go"}
	mf := &MergedFindings{Findings: []domain.Finding{
		// 空证据 critical → 保级（unverified 只标）
		{File: "a.go", Line: 1, Severity: "critical", Comment: "empty"},
		// 纯文字 high → 保级
		{File: "a.go", Line: 2, Severity: "high", Comment: "prose",
			Evidence: "改动与既有模块口径存在差距"},
		// 有引用但核验不过 high → 降级（收窄的另一半）
		{File: "a.go", Line: 3, Severity: "high", Comment: "lying",
			Evidence: "ghost.go:1 编造的引用"},
	}}
	VerifyFindingsEvidence(mf, changed, nil, nil)
	fs := mf.Findings
	if fs[0].Severity != "critical" || fs[0].EvidenceStatus != domain.EvidenceUnverified {
		t.Fatalf("空证据 critical 应保级+unverified: %+v", fs[0])
	}
	if fs[1].Severity != "high" || fs[1].EvidenceStatus != domain.EvidenceUnverified {
		t.Fatalf("纯文字 high 应保级+unverified: %+v", fs[1])
	}
	if fs[2].Severity != "medium" || fs[2].EvidenceStatus != domain.EvidenceUnverified {
		t.Fatalf("有引用核验不过 high 应降级: %+v", fs[2])
	}
}
