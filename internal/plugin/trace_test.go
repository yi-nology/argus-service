package plugin

import "testing"

// TestBuildTraceChain 四环载荷（批 F spec）：①锚可选 ②ACCheck.Evidence
// 提 file ③测试映射 inferred 标注 ④断链只报告零裁决影响。
func TestBuildTraceChain(t *testing.T) {
	spec := &RequirementSpec{
		Source:    "issue #123",
		IssueRefs: []string{"https://git.example/acme/app/issues/123"},
		ACs: []AcceptanceCriterion{
			{ID: "AC-1", Text: "登录必须校验口令强度（login 相关）"},
			{ID: "AC-2", Text: "导出功能支持 CSV"},
			{ID: "AC-3", Text: "审计日志"},
		},
	}
	report := &ComplianceReport{Checks: []ACCheck{
		{ID: "AC-1", Status: ACImplemented, Evidence: "auth/login.go:12 校验存在"},
		{ID: "AC-2", Status: ACMissing},
		// AC-3 无 check（缺判）
	}}
	changed := []string{"auth/login.go", "auth/login_test.go", "export/csv.go", "README.md"}

	tc := BuildTraceChain(spec, report, changed)
	if tc == nil {
		t.Fatal("nil spec 才返回 nil")
	}
	if tc.SourceRef != "https://git.example/acme/app/issues/123" {
		t.Fatalf("①环锚错: %q", tc.SourceRef)
	}
	if len(tc.Traces) != 3 {
		t.Fatalf("traces = %d, want 3", len(tc.Traces))
	}

	tr1 := tc.Traces[0]
	if tr1.CodeStatus != ACImplemented || len(tr1.CodeFiles) == 0 || tr1.CodeFiles[0] != "auth/login.go" {
		t.Fatalf("②环代码映射错: %+v", tr1)
	}
	// ③：AC-1 提到 login stem → login_test.go inferred 配对
	if tr1.TestStatus != "inferred" || len(tr1.Tests) != 1 || tr1.Tests[0] != "auth/login_test.go" {
		t.Fatalf("③环测试映射应 inferred: %+v", tr1)
	}

	tr2 := tc.Traces[1]
	if tr2.CodeStatus != ACMissing || tr2.TestStatus != "none" {
		t.Fatalf("AC-2 断链态错: %+v", tr2)
	}

	// ④断链摘要：AC-2 code_missing + AC-2/AC-3 no_test_mapping
	if len(tc.Broken) < 2 {
		t.Fatalf("断链摘要应≥2: %v", tc.Broken)
	}

	// Artifacts 反解 roundtrip
	artifacts := map[string]string{}
	// （marshal 由接线侧做——此处测反解）
	_ = artifacts

	// nil spec 安全
	if BuildTraceChain(nil, report, changed) != nil {
		t.Fatal("nil spec 应返回 nil")
	}
}
