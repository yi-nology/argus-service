package plugin

// 证据读取预算测试与基准（v7.5 #69）：从 evidence_test.go 拆出。

import (
	"strconv"
	"strings"
	"testing"

	"github.com/yi-nology/argus/internal/domain"
)

// countingReader 记录 ReadLines 调用次数（预算/缓存行为钉住用）。
type countingReader struct {
	fakeReader
	calls int
}

func (r *countingReader) ReadLines(path string) ([]string, error) {
	r.calls++
	return r.fakeReader.ReadLines(path)
}

// TestEvidenceBudgetRefCap 任务级引用数预算：超限 ref 降级 unverified 且
// 显式标注——不读取、不判 invalid、不静默放行。
func TestEvidenceBudgetRefCap(t *testing.T) {
	r := &countingReader{fakeReader: fakeReader{
		files: map[string][]string{"a.go": {"l1", "l2", "l3"}},
		sha:   "s",
	}}
	budget := &evidenceBudget{maxRefs: 2}
	st, refs, note := assessEvidenceStatus("a.go:1 一 a.go:2 二 a.go:3 三",
		[]string{"a.go"}, nil, r, evidenceContent{}, budget)
	if st != domain.EvidenceUnverified {
		t.Fatalf("超引用数预算应 unverified, got %q", st)
	}
	if len(refs) != 3 {
		t.Fatalf("引用解析不应被预算截断（只是不核验）: %d", len(refs))
	}
	if !strings.Contains(note, "引用数上限") {
		t.Fatalf("应显式标注预算截断: %q", note)
	}
	if r.calls != 2 {
		t.Fatalf("预算内只应读 2 次, got %d", r.calls)
	}
}

// TestEvidenceBudgetByteCap 任务级总字节预算：越界文件按预算截断（unverified
// + 标注），同文件只计一次字节。
func TestEvidenceBudgetByteCap(t *testing.T) {
	r := &countingReader{fakeReader: fakeReader{
		files: map[string][]string{
			"a.go": {"l1", "l2", "l3"}, // 每行 2 字节 + 换行 = 9 字节
			"b.go": {"m1", "m2", "m3"},
		},
		sha: "s",
	}}
	// a.go=9 字节落在预算内；b.go 再 +9 越过 10 字节上限（b.go 引用截断）。
	budget := &evidenceBudget{maxBytes: 10}
	st, _, note := assessEvidenceStatus("a.go:1 一 b.go:1 二",
		[]string{"a.go", "b.go"}, nil, r, evidenceContent{}, budget)
	if st != domain.EvidenceUnverified {
		t.Fatalf("超总字节预算应 unverified, got %q", st)
	}
	if !strings.Contains(note, "总字节上限") {
		t.Fatalf("应显式标注字节预算截断: %q", note)
	}
	// 同文件二次引用不重复计字节：a.go 两个 ref 只有一次 9 字节。
	budget2 := &evidenceBudget{maxBytes: 10}
	st2, _, _ := assessEvidenceStatus("a.go:1 一 a.go:2 二",
		[]string{"a.go"}, nil, r, evidenceContent{}, budget2)
	if st2 != domain.EvidenceStructuralValid {
		t.Fatalf("同文件重复引用不应触发字节预算: %q", st2)
	}
	if budget2.bytes != 9 {
		t.Fatalf("同文件只应计一次字节, got %d", budget2.bytes)
	}
}

// TestSourceFileTooLargeDegradesNotInvalid 单文件超限：降级 unverified 并标注
// 「读取预算」，绝不按 fs.ErrNotExist 判 invalid（文件真实存在）。
func TestSourceFileTooLargeDegradesNotInvalid(t *testing.T) {
	r := &fakeReader{err: ErrSourceFileTooLarge}
	st, _, note := assessEvidenceStatus("a.go:1 依据", []string{"a.go"}, nil, r, evidenceContent{}, nil)
	if st == domain.EvidenceInvalid {
		t.Fatalf("预算超限不得判 invalid: %q note=%q", st, note)
	}
	if st != domain.EvidenceUnverified || !strings.Contains(note, "读取预算") {
		t.Fatalf("预算超限应 unverified+标注: %q note=%q", st, note)
	}
}

// TestEvidenceBudgetSharedAcrossFindings 任务预算跨 finding 共享（不是每条
// finding 各自一份）：一跑两跳之外，第 3 条必须因同一任务预算截断。
func TestEvidenceBudgetSharedAcrossFindings(t *testing.T) {
	r := &countingReader{fakeReader: fakeReader{
		files: map[string][]string{"a.go": {"l1", "l2", "l3"}},
		sha:   "s",
	}}
	mf := &MergedFindings{Findings: []domain.Finding{
		{File: "a.go", Line: 1, Severity: "high", Comment: "1", Evidence: "a.go:1 一"},
		{File: "a.go", Line: 2, Severity: "high", Comment: "2", Evidence: "a.go:2 二"},
		{File: "a.go", Line: 3, Severity: "high", Comment: "3", Evidence: "a.go:3 三"},
	}}
	verifyFindingsEvidenceBudgeted(mf, []string{"a.go"}, nil, r, &evidenceBudget{maxRefs: 2})
	if r.calls != 2 {
		t.Fatalf("跨 finding 共享预算：只应读 2 次, got %d", r.calls)
	}
	if mf.Findings[2].EvidenceStatus != domain.EvidenceUnverified {
		t.Fatalf("预算耗尽条目应 unverified: %+v", mf.Findings[2])
	}
	if !strings.Contains(mf.Findings[2].Evidence, "预算耗尽") {
		t.Fatalf("预算耗尽应显式标注: %q", mf.Findings[2].Evidence)
	}
	if mf.Findings[2].Severity != domain.SeverityMedium {
		t.Fatalf("高危预算截断应降级 medium: %q", mf.Findings[2].Severity)
	}
}

// BenchmarkVerifyFindingsEvidence100Findings 压测基线：2 万行文件 + 100 条
// finding × 每 finding 2 个 ref。源码读取缓存由 runner SourceReader 实现
// （见 runner source_reader_test.go），这里量化核验层 CPU/分配成本。
func BenchmarkVerifyFindingsEvidence100Findings(b *testing.B) {
	lines := make([]string, 20000)
	for i := range lines {
		lines[i] = "func fn" + strconv.Itoa(i) + "() { return nil }"
	}
	r := &fakeReader{files: map[string][]string{"src/big.go": lines}, sha: "head"}
	mf := &MergedFindings{Findings: make([]domain.Finding, 100)}
	for i := range mf.Findings {
		line := i*4 + 1
		mf.Findings[i] = domain.Finding{
			File: "src/big.go", Line: line, Severity: "medium", Comment: "c",
			Evidence:        "src/big.go:" + strconv.Itoa(line) + " 与 src/big.go:" + strconv.Itoa(line+2) + " 不一致",
			EvidenceSnippet: "func fn" + strconv.Itoa(line-1) + "()",
		}
	}
	changed := []string{"src/big.go"}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		VerifyFindingsEvidence(mf, changed, nil, r)
	}
}

// TestEvidenceBudgetAddFileAfterHit 预算已命中后不再接受新文件（防止截断后
// 继续读取放大资源消耗）。
func TestEvidenceBudgetAddFileAfterHit(t *testing.T) {
	b := &evidenceBudget{maxBytes: 1, hit: true}
	if b.addFile("a.go", []string{"x"}) {
		t.Fatal("已命中预算的文件不应再计入")
	}
}
