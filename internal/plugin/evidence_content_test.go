package plugin

// 证据内容对账/四态判定测试（v7.5 #68）：从 evidence_test.go 拆出，
// 保持单文件在架构阈值以内。fakeReader 定义仍留在 evidence_test.go。

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strconv"
	"strings"
	"testing"

	"github.com/yi-nology/argus/internal/domain"
)

// TestAssessEvidenceStatusFourStates Phase 2 四态核验（spec §3 三类夹具）：
// 行号越界 / 文件不存在 / SHA 对齐，外加纯文字翻转与基础设施失败降级不冤枉；
// v7.5 #68 起 verified 还要求内容对账（snippet/摘要）且引用行非空非注释。
func TestAssessEvidenceStatusFourStates(t *testing.T) {
	changed := []string{"src/foo.go", "src/gone.go"}
	r := &fakeReader{
		files: map[string][]string{
			// 行 2=有效代码，行 3=注释，行 4=空行，行 5=有效代码
			"src/foo.go": {"package foo", "func A() {}", "// just a comment", "", "x := 1"},
		},
		sha: "abc123",
	}
	aligned := func(s string, exp evidenceContent) domain.EvidenceStatus {
		st, _, _ := assessEvidenceStatus(s, changed, nil, r, exp, nil)
		return st
	}
	lineTwo := evidenceContent{Snippet: "func A() {}"}

	// 1a. 结构全对但没给内容期望 → structural（#68：引用可定位 ≠ 内容属实）
	if st := aligned("src/foo.go:2 空值检查缺失", evidenceContent{}); st != domain.EvidenceStructuralValid {
		t.Fatalf("无内容期望应上限 structural_valid, got %q", st)
	}
	// 1b. 结构 + 版本 + 片段对账全对 → verified
	if st := aligned("src/foo.go:2 空值检查缺失", lineTwo); st != domain.EvidenceVerified {
		t.Fatalf("片段对账通过应 verified, got %q", st)
	}
	// 1c. 缩进/空白差异不算内容不符（空白归一后包含）
	if st := aligned("src/foo.go:2 空值检查缺失", evidenceContent{Snippet: "  func   A()  {}  "}); st != domain.EvidenceVerified {
		t.Fatalf("空白归一片段应 verified, got %q", st)
	}
	// 1d. 摘要对账（sha256: 前缀、大小写不敏感）→ verified
	sum := sha256.Sum256([]byte("func A() {}"))
	hexSum := hex.EncodeToString(sum[:])
	if st := aligned("src/foo.go:2 空值检查缺失", evidenceContent{SHA256: "sha256:" + strings.ToUpper(hexSum)}); st != domain.EvidenceVerified {
		t.Fatalf("摘要对账通过应 verified, got %q", st)
	}
	// 1e. 片段对不上（同文件无关行）→ unverified，不再冒充 verified
	st, _, note := assessEvidenceStatus("src/foo.go:2 空值检查缺失", changed, nil, r,
		evidenceContent{Snippet: "os.RemoveAll(userInput)"}, nil)
	if st != domain.EvidenceUnverified || !strings.Contains(note, "证据内容与引用行不符") {
		t.Fatalf("片段不匹配应 unverified+原因, got %q note=%q", st, note)
	}
	// 1f. 摘要不匹配 → unverified
	badSum := strings.Repeat("0", 64)
	if st := aligned("src/foo.go:2 空值检查缺失", evidenceContent{SHA256: badSum}); st != domain.EvidenceUnverified {
		t.Fatalf("摘要不匹配应 unverified, got %q", st)
	}
	// 1g. 摘要格式无效 → unverified（不静默当通过）
	stBad, _, noteBad := assessEvidenceStatus("src/foo.go:2 依据", changed, nil, r,
		evidenceContent{SHA256: "not-a-digest"}, nil)
	if stBad != domain.EvidenceUnverified || !strings.Contains(noteBad, "哈希格式无效") {
		t.Fatalf("摘要格式错应 unverified+原因, got %q note=%q", stBad, noteBad)
	}
	// 1h. 指向注释行 → unverified（轻量语义校验）
	stC, _, noteC := assessEvidenceStatus("src/foo.go:3 依据", changed, nil, r, evidenceContent{}, nil)
	if stC != domain.EvidenceUnverified || !strings.Contains(noteC, "注释行") {
		t.Fatalf("注释行应 unverified+原因, got %q note=%q", stC, noteC)
	}
	// 1i. 指向空行 → unverified
	stBlank, _, noteBlank := assessEvidenceStatus("src/foo.go:4 依据", changed, nil, r, evidenceContent{}, nil)
	if stBlank != domain.EvidenceUnverified || !strings.Contains(noteBlank, "空行") {
		t.Fatalf("空行应 unverified+原因, got %q note=%q", stBlank, noteBlank)
	}
	// 2. 行号越界（:999999 与 :99）→ unverified（Ruling：文件在、行越界=待人工）
	if st := aligned("src/foo.go:999999 越界引用", evidenceContent{}); st != domain.EvidenceUnverified {
		t.Fatalf("行号越界应 unverified, got %q", st)
	}
	// 3. 文件不存在 → invalid
	if st := aligned("src/missing.go:1 幻影文件", evidenceContent{}); st != domain.EvidenceInvalid {
		t.Fatalf("文件不存在应 invalid, got %q", st)
	}
	// 4. changed 集内但 reader 读不到（文件表没有）→ invalid
	if st := aligned("src/gone.go:3 diff 里有但副本缺失", evidenceContent{}); st != domain.EvidenceInvalid {
		t.Fatalf("集内文件副本缺失应 invalid, got %q", st)
	}
	// 5. 纯文字 → unverified（Phase 2 翻转：不再叫 verified）
	st5, _, note5 := assessEvidenceStatus("只是叙述没有引用", changed, nil, r, evidenceContent{}, nil)
	if st5 != domain.EvidenceUnverified || !strings.Contains(note5, "未提供结构化证据") {
		t.Fatalf("纯文字应 unverified+措辞, got %q note=%q", st5, note5)
	}
	// 6. 无版本保证（sha==""）→ 内容对账通过也上限 structural
	noVer := &fakeReader{files: r.files}
	st6, _, _ := assessEvidenceStatus("src/foo.go:2 依据", changed, nil, noVer, lineTwo, nil)
	if st6 != domain.EvidenceStructuralValid {
		t.Fatalf("无版本保证应 structural_valid, got %q", st6)
	}
	// 7. 基础设施失败（非 NotExist）+ 集内 → 集合级降级 structural（不冤枉 invalid）
	broken := &fakeReader{err: errors.New("workcopy: 凭证缺失")}
	st7, _, _ := assessEvidenceStatus("src/foo.go:2 依据", changed, nil, broken, lineTwo, nil)
	if st7 != domain.EvidenceStructuralValid {
		t.Fatalf("基础设施失败集内应降级 structural, got %q", st7)
	}
	// 8. 基础设施失败 + 集外 → unverified
	st8, _, _ := assessEvidenceStatus("external/x.go:2 依据", changed, nil, broken, evidenceContent{}, nil)
	if st8 != domain.EvidenceUnverified {
		t.Fatalf("基础设施失败集外应 unverified, got %q", st8)
	}
	// 9. 多 ref 聚合取最差（verified 混 invalid → invalid）
	if st := aligned("src/foo.go:2 对的 + src/missing.go:1 错的", lineTwo); st != domain.EvidenceInvalid {
		t.Fatalf("聚合应取最差 invalid, got %q", st)
	}
	// 10. 多 ref：至少一个 ref 命中片段即算内容对账通过（其余 ref 仍须可定位）
	if st := aligned("src/foo.go:2 与 src/foo.go:5 相关", lineTwo); st != domain.EvidenceVerified {
		t.Fatalf("至少一个 ref 命中片段应 verified, got %q", st)
	}
}

// TestEvidenceContentReconciliation v7.5 #68 语义升级正负例：
// verified 必须证明「该行内容就是证据」——片段/摘要对账通过；同文件无关行、
// 空行、注释行、无内容期望都不再拿 verified；高危条目因内容不符降级。
func TestEvidenceContentReconciliation(t *testing.T) {
	changed := []string{"src/foo.go"}
	r := &fakeReader{
		files: map[string][]string{
			"src/foo.go": {"package foo", "func A() {}", "", "// doc", "return nil"},
		},
		sha: "head-sha",
	}

	// 正向：引用行 + 照抄片段 → verified
	f := domain.Finding{File: "src/foo.go", Line: 1, Severity: "high", Comment: "x",
		Evidence: "src/foo.go:2 判空缺失", EvidenceSnippet: "func A() {}"}
	res := VerifyFindingEvidence(f, changed, nil, r)
	if res.Status != domain.EvidenceVerified || !res.Verified {
		t.Fatalf("片段一致应 verified: %+v", res)
	}

	// 正向：摘要对账（自动化上游形态）→ verified
	sum := sha256.Sum256([]byte("return nil"))
	f2 := domain.Finding{File: "src/foo.go", Line: 1, Severity: "high", Comment: "y",
		Evidence: "src/foo.go:5 返回空", EvidenceSHA256: hex.EncodeToString(sum[:])}
	if res := VerifyFindingEvidence(f2, changed, nil, r); res.Status != domain.EvidenceVerified {
		t.Fatalf("摘要一致应 verified: %+v", res)
	}

	// 负向：同文件无关行（片段对不上）→ unverified + 高危降级 medium
	mf := &MergedFindings{Findings: []domain.Finding{{
		File: "src/foo.go", Line: 2, Severity: "high", Comment: "z",
		Evidence: "src/foo.go:2 判空缺失", EvidenceSnippet: "os.RemoveAll(userInput)",
	}}}
	VerifyFindingsEvidence(mf, changed, nil, r)
	got := mf.Findings[0]
	if got.EvidenceStatus != domain.EvidenceUnverified || got.Severity != "medium" {
		t.Fatalf("无关行证据应 unverified+降级: %+v", got)
	}
	if !strings.Contains(got.Evidence, "证据内容与引用行不符") {
		t.Fatalf("应显式标注内容不符: %q", got.Evidence)
	}

	// 负向：空行/注释行引用 → unverified（不再按 verified 放行）
	for _, tc := range []struct {
		line int
		want string
	}{{3, "空行"}, {4, "注释行"}} {
		ff := domain.Finding{File: "src/foo.go", Line: 1, Severity: "low", Comment: "c",
			Evidence: "src/foo.go:" + strconv.Itoa(tc.line) + " 依据"}
		rr := VerifyFindingEvidence(ff, changed, nil, r)
		if rr.Status != domain.EvidenceUnverified || !strings.Contains(rr.Note, tc.want) {
			t.Fatalf("行 %d 应 unverified+%s: %+v", tc.line, tc.want, rr)
		}
	}

	// 负向：没有内容期望 → 引用可定位最多 structural（verified 不可达）
	ff := domain.Finding{File: "src/foo.go", Line: 1, Severity: "low", Comment: "n",
		Evidence: "src/foo.go:2 依据"}
	if rr := VerifyFindingEvidence(ff, changed, nil, r); rr.Status != domain.EvidenceStructuralValid {
		t.Fatalf("无内容期望应上限 structural: %+v", rr)
	}
}

// TestIsCodeEvidenceLine 行首注释判定（不引入语言解析；宁可漏判不误判）。
func TestIsCodeEvidenceLine(t *testing.T) {
	cases := []struct {
		path, line string
		want       bool
	}{
		{"a.go", "func A() {}", true},
		{"a.go", "// 注释", false},
		{"a.go", "/* 块注释 */", false},
		{"a.go", " * 续行", false},
		{"a.go", "*p = nil", true}, // 解引用不是注释
		{"a.go", "", false},
		{"a.go", "   ", false},
		{"a.sh", "# 注释", false},
		{"a.sh", "#!/bin/sh", false},
		{"a.c", "#include <stdio.h>", true}, // 预处理是代码
		{"a.c", "#define X 1", true},
		{"a.c", "# 注释", false},
		{"a.sql", "-- 注释", false},
		{"a.go", "--x", true}, // 自减表达式
		{"a.ini", "; 注释", false},
	}
	for _, tc := range cases {
		if got := isCodeEvidenceLine(tc.path, tc.line); got != tc.want {
			t.Fatalf("isCodeEvidenceLine(%q, %q) = %v, want %v", tc.path, tc.line, got, tc.want)
		}
	}
}

// TestEvidenceHelperEdges 状态聚合/去重/摘要解析的边界（#69 预算与 #68 对账
// 共用的底层件；这些分支在端到端用例里不一定都会走到）。
func TestEvidenceHelperEdges(t *testing.T) {
	// worseStatus：两个方向都取更差
	if got := worseStatus(domain.EvidenceVerified, domain.EvidenceInvalid); got != domain.EvidenceInvalid {
		t.Fatalf("worseStatus(verified, invalid) = %q", got)
	}
	if got := worseStatus(domain.EvidenceInvalid, domain.EvidenceVerified); got != domain.EvidenceInvalid {
		t.Fatalf("worseStatus(invalid, verified) = %q", got)
	}
	if got := worseStatus(domain.EvidenceStructuralValid, domain.EvidenceStructuralValid); got != domain.EvidenceStructuralValid {
		t.Fatalf("worseStatus 同态应保持不变: %q", got)
	}

	// dedupeStrings：保序去重 + 丢弃空串
	got := dedupeStrings([]string{"a", "b", "a", "", "b", "c"})
	if len(got) != 3 || got[0] != "a" || got[1] != "b" || got[2] != "c" {
		t.Fatalf("dedupeStrings = %v", got)
	}

	// appendRefOnce：同一 file:line 只追加一次
	seen := map[string]bool{}
	var refs []EvidenceRef
	ref := EvidenceRef{File: "a.go", Line: 1}
	refs = appendRefOnce(refs, seen, ref)
	refs = appendRefOnce(refs, seen, ref)
	if len(refs) != 1 {
		t.Fatalf("重复 ref 应只保留一次: %v", refs)
	}

	// parseSHA256Hex：前缀/大小写/长度/非法字符
	valid := "SHA256:" + strings.Repeat("A", 64)
	if got, ok := parseSHA256Hex(valid); !ok || got != strings.Repeat("a", 64) {
		t.Fatalf("parseSHA256Hex(%q) = %q/%v", valid, got, ok)
	}
	if _, ok := parseSHA256Hex(strings.Repeat("a", 63)); ok {
		t.Fatal("63 位摘要应拒绝")
	}
	if _, ok := parseSHA256Hex(strings.Repeat("z", 64)); ok {
		t.Fatal("非十六进制摘要应拒绝")
	}
}

// TestEvidenceContentMatchesDimensions 片段/摘要两个维度同时给出时的组合判定。
func TestEvidenceContentMatchesDimensions(t *testing.T) {
	line := "func A() {}"
	sum := sha256.Sum256([]byte(line))
	hexSum := hex.EncodeToString(sum[:])

	if ok, why := evidenceContentMatches(line, evidenceContent{Snippet: "func A()", SHA256: hexSum}); !ok {
		t.Fatalf("片段+摘要均通过应匹配: %s", why)
	}
	if ok, why := evidenceContentMatches(line, evidenceContent{Snippet: "func A()", SHA256: strings.Repeat("0", 64)}); ok ||
		why != "证据摘要与引用行不符" {
		t.Fatalf("摘要不符应拒绝: ok=%v why=%q", ok, why)
	}
	if ok, why := evidenceContentMatches(line, evidenceContent{Snippet: "nope", SHA256: hexSum}); ok ||
		why != "证据内容与引用行不符" {
		t.Fatalf("片段不符应拒绝: ok=%v why=%q", ok, why)
	}
	if ok, why := evidenceContentMatches(line, evidenceContent{Snippet: "   "}); ok || why != "证据片段为空" {
		t.Fatalf("空片段应拒绝: ok=%v why=%q", ok, why)
	}
	if ok, why := evidenceContentMatches(line, evidenceContent{SHA256: "zz" + strings.Repeat("0", 62)}); ok ||
		why != "证据哈希格式无效" {
		t.Fatalf("非法摘要应拒绝: ok=%v why=%q", ok, why)
	}
}

// TestSourceBudgetExceededDegrades ErrSourceBudgetExceeded 与单文件超限同口径：
// 降级 unverified + 标注，不判 invalid。
func TestSourceBudgetExceededDegrades(t *testing.T) {
	r := &fakeReader{err: ErrSourceBudgetExceeded}
	st, _, note := assessEvidenceStatus("a.go:1 依据", []string{"a.go"}, nil, r, evidenceContent{}, nil)
	if st != domain.EvidenceUnverified || !strings.Contains(note, "总字节") {
		t.Fatalf("任务预算耗尽应 unverified+标注: %q note=%q", st, note)
	}
}
