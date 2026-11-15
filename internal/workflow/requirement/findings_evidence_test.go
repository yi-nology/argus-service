package requirement

import (
	"io/fs"
	"strings"
	"testing"

	"github.com/yi-nology/argus/internal/domain"
	"github.com/yi-nology/argus/internal/plugin"
)

// TestVerifyFindingsEvidenceWiring Finding 级证据校验接线缝（finish 步骤）：
// 改动集（Diff.Files）+ 触达集（ArtifactCodegraphTouched 渲染面）交叉校验；
// 高危（critical/high）证据不可验证降级 medium 退出 R5 否决面 + ⚠️ 软标注；
// 集内条目零变化。Phase 2 Evidence v2：缝签名带 *Workflow（SourceReader 注入
// 透传），空证据翻转为 unverified+措辞标注。缝函数由 finish 在 applyRepoPolicies
// 后调用（与 synthesisAnalyst 的 VerifyClaimsEvidence 同族、不同面）。
func TestVerifyFindingsEvidenceWiring(t *testing.T) {
	task := analystTask("standard") // Diff.Files = [x.go]
	task.Artifacts[plugin.ArtifactCodegraphTouched] = "命中 x.go:3 与 touch.go:7"
	merged := &plugin.MergedFindings{Findings: []domain.Finding{
		{File: "x.go", Line: 3, Severity: "high", Comment: "a",
			Evidence: "x.go:3 改动侧缺判空"}, // 改动集内 → 不动
		{File: "x.go", Line: 4, Severity: "critical", Comment: "b",
			Evidence: "ghost.go:9 未鉴权"}, // 集外高危 → 降级 medium + ⚠️
		{File: "x.go", Line: 5, Severity: "low", Comment: "c",
			Evidence: "touch.go:7 消费侧依赖旧语义"}, // 触达集内 → 不动
		{File: "x.go", Line: 6, Severity: "medium", Comment: "d"}, // 空证据 → 不判
	}}

	w := &Workflow{} // SourceReader nil = 集合级降级路径
	verifyFindingsEvidence(w, merged, task)

	fs := merged.Findings
	if fs[0].Severity != "high" || strings.Contains(fs[0].Evidence, "⚠️") {
		t.Fatalf("改动集内 high 不应动: sev=%q evid=%q", fs[0].Severity, fs[0].Evidence)
	}
	if fs[1].Severity != "medium" || !strings.Contains(fs[1].Evidence, "⚠️ 证据待验证") {
		t.Fatalf("集外 critical 应降级 medium + ⚠️: sev=%q evid=%q", fs[1].Severity, fs[1].Evidence)
	}
	if fs[2].Severity != "low" || strings.Contains(fs[2].Evidence, "⚠️") {
		t.Fatalf("触达集内 low 不应动: sev=%q evid=%q", fs[2].Severity, fs[2].Evidence)
	}
	if fs[3].Severity != "medium" || fs[3].EvidenceStatus != domain.EvidenceUnverified {
		t.Fatalf("空证据 medium 保级但应回写 unverified: %+v", fs[3])
	}
	if !strings.Contains(fs[3].Evidence, "未提供结构化证据") {
		t.Fatalf("空证据应挂措辞标注: %q", fs[3].Evidence)
	}
}

// wiringReader 假 SourceReader（断言 Workflow.SourceReader 透传到核验核心）。
type wiringReader struct{}

func (wiringReader) ReadLines(path string) ([]string, error) {
	if path == "x.go" {
		return []string{"package x", "a := 1", "b := 2", "c := 3", "d := 4", "e := 5"}, nil
	}
	return nil, fs.ErrNotExist
}
func (wiringReader) HeadSHA() string { return "task-head" }

// TestVerifyFindingsEvidenceReaderPassthrough Phase 2：注入的 SourceReader 经
// wrapper 透传——集内文件经真读取核验为 verified（非集合级 structural）。
func TestVerifyFindingsEvidenceReaderPassthrough(t *testing.T) {
	task := analystTask("standard")
	task.Artifacts[plugin.ArtifactCodegraphTouched] = "命中 x.go:3"
	merged := &plugin.MergedFindings{Findings: []domain.Finding{
		{File: "x.go", Line: 3, Severity: "critical", Comment: "a",
			Evidence: "x.go:3 改动侧缺判空"},
	}}
	w := &Workflow{SourceReader: wiringReader{}}
	verifyFindingsEvidence(w, merged, task)
	if got := merged.Findings[0].EvidenceStatus; got != domain.EvidenceVerified {
		t.Fatalf("注入 reader 后集内引用应 verified, got %q", got)
	}
}
