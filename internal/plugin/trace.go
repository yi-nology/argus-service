package plugin

// 需求追溯链（批 F spec）：issue→AC→代码→测试四环载荷。
// 纯数据面——挂 task Artifacts（ArtifactTraceChain），零裁决影响（不进 R5）。

import (
	"encoding/json"
	"strings"

	"github.com/yi-nology/argus/internal/domain"
)

// ArtifactTraceChain 追溯链 Artifacts 键。
const ArtifactTraceChain = "trace_chain"

// ACTrace 单条 AC 的追溯链（spec §3.1）。
type ACTrace struct {
	ACID       string   `json:"ac_id"`
	SourceRef  string   `json:"source_ref,omitempty"`  // ①环：issue 锚（spec.IssueRefs 首个）
	CodeFiles  []string `json:"code_files,omitempty"`  // ②环：ACCheck.Evidence 的 file 提取
	Tests      []string `json:"tests,omitempty"`       // ③环：启发式测试映射（inferred）
	TestStatus string   `json:"test_status,omitempty"` // covered|inferred|none
	CodeStatus string   `json:"code_status,omitempty"` // implemented|missing|...（ACCheck 态）
}

// TraceChain 追溯链整体（挂 Artifacts 的 JSON 载荷）。
type TraceChain struct {
	SourceRef string    `json:"source_ref,omitempty"` // ①环锚（spec.IssueRefs 拼接）
	Traces    []ACTrace `json:"traces"`
	Broken    []string  `json:"broken,omitempty"` // 断链摘要（spec §3.2 三类）
}

// BuildTraceChain 追溯链产出（finish 步骤，与 verifyFindingsEvidence 同层）：
//   - ①锚：spec.IssueRefs（可选，空=无锚不阻断）
//   - ②代码映射：ACCheck.Evidence 提取 file（实现态才带——missing 无映射）
//   - ③测试映射：changed 中 *_test.go / tests/ 目录按 AC 提到的文件名启发式
//     配对（同 stem）——**inferred 不冒充证明**（spec §2.3 纪律）
//   - ④证据态：TestStatus=none 的 AC 进 Broken（断链只报告零裁决影响）
func BuildTraceChain(spec *RequirementSpec, report *ComplianceReport, changed []string) *TraceChain {
	if spec == nil {
		return nil
	}
	tc := &TraceChain{SourceRef: strings.Join(spec.IssueRefs, ",")}
	var tests []string
	for _, f := range changed {
		if strings.HasSuffix(f, "_test.go") || strings.Contains(f, "tests/") || strings.Contains(f, "/test/") {
			tests = append(tests, f)
		}
	}
	checks := map[string]ACCheck{}
	if report != nil {
		for _, c := range report.Checks {
			checks[c.ID] = c
		}
	}
	for _, ac := range spec.ACs {
		tr := ACTrace{ACID: ac.ID, SourceRef: tc.SourceRef}
		if c, ok := checks[ac.ID]; ok {
			tr.CodeStatus = c.Status
			// ②：Evidence 形如 file:line —— 提取 file 段
			if c.Evidence != "" {
				part := c.Evidence
				if i := strings.Index(part, ":"); i > 0 {
					part = part[:i]
				}
				for _, tok := range strings.Fields(part) {
					if tok != "" && !strings.Contains(tok, " ") {
						tr.CodeFiles = append(tr.CodeFiles, tok)
					}
				}
			}
		}
		// ③启发式：AC 文本/证据提到的文件 stem 与测试文件 stem 配对（inferred）
		for _, tf := range tests {
			stem := strings.TrimSuffix(tf[strings.LastIndex(tf, "/")+1:], "_test.go")
			if stem != "" && (strings.Contains(ac.Text, stem) || containsStem(tr.CodeFiles, stem)) {
				tr.Tests = append(tr.Tests, tf)
				tr.TestStatus = "inferred"
			}
		}
		if tr.TestStatus == "" {
			tr.TestStatus = "none"
			tc.Broken = append(tc.Broken, ac.ID+":no_test_mapping")
		}
		if tr.CodeStatus == ACMissing {
			tc.Broken = append(tc.Broken, ac.ID+":code_missing")
		}
		tc.Traces = append(tc.Traces, tr)
	}
	return tc
}

// containsStem files 中是否有路径含 stem。
func containsStem(files []string, stem string) bool {
	for _, f := range files {
		if strings.Contains(f, stem) {
			return true
		}
	}
	return false
}

// TraceChainOf 从 Artifacts JSON 反解（渲染/出口消费面）；缺位返回 nil。
func TraceChainOf(artifacts map[string]string) *TraceChain {
	raw, ok := artifacts[ArtifactTraceChain]
	if !ok || raw == "" {
		return nil
	}
	var tc TraceChain
	if err := json.Unmarshal([]byte(raw), &tc); err != nil {
		return nil
	}
	return &tc
}

// StatusOfEvidencePhase 提供给 trace 的 Phase2 状态透传（④环交汇面——
// 意见级四态与需求级链不合并，见批 F spec §3.3）。
func StatusOfEvidencePhase(st domain.EvidenceStatus) string { return string(st) }
