// service_merge_gate_test.go —— 资格门判定与路由 scope/authz 钉子。
package service

import (
	"testing"

	argus "github.com/yi-nology/argus/biz/model/argus"
)

func TestRequiredScopeMergesAdmin(t *testing.T) {
	cases := []struct {
		method, path, want string
	}{
		{"GET", "/merges/eligibility", "admin"},
		{"POST", "/merges/preview", "admin"},
		{"POST", "/merges/confirm", "admin"},
		{"GET", "/merges/m-1", "admin"},
		{"POST", "/merges/reconcile", "admin"},
		{"GET", "/tasks", "read"},
		{"POST", "/tasks", "ci"},
		{"GET", "/controlplane/leases", "admin"},
	}
	for _, tc := range cases {
		if got := requiredScope(tc.method, tc.path); got != tc.want {
			t.Errorf("requiredScope(%s %s)=%q want %q", tc.method, tc.path, got, tc.want)
		}
	}
}

func TestAuthzMatrixCoversMergeRoutes(t *testing.T) {
	want := map[string]string{
		"GET /merges/eligibility": AuthzGateRepo,
		"POST /merges/preview":    AuthzGateRepo,
		"POST /merges/confirm":    AuthzGateExempt,
		"GET /merges/:opId":       AuthzGateExempt,
		"POST /merges/reconcile":  AuthzGateExempt,
	}
	seen := map[string]string{}
	for _, row := range AuthzMatrix() {
		seen[row.Method+" "+row.Path] = row.Gate
	}
	for k, gate := range want {
		if got, ok := seen[k]; !ok {
			t.Errorf("authz matrix missing %s", k)
		} else if got != gate {
			t.Errorf("authz %s gate=%q want %q", k, got, gate)
		}
	}
}

func TestMergeGateFirstFailCode(t *testing.T) {
	g := &mergeGateResult{
		Checks: []mergeGateCheck{
			{Code: gateCROpen, OK: true},
			{Code: gateCIPass, OK: false},
			{Code: gateReviewDone, OK: false},
		},
	}
	if got := g.firstFailCode(); got != gateCIPass {
		t.Fatalf("firstFail=%q want %q", got, gateCIPass)
	}
	g.Eligible = true
	for _, c := range g.Checks {
		if !c.OK {
			g.Eligible = false
			break
		}
	}
	if g.Eligible {
		t.Fatal("should not be eligible")
	}
}

func TestToIDLGateMapsChecks(t *testing.T) {
	g := &mergeGateResult{
		Eligible: true, HeadSHA: "h1", MergeStatus: "mergeable",
		ReviewTaskID: "t-1", ReviewVerdict: "approve",
		CIStatus: CIStateSuccessPass, BlockingFindings: 0,
		Checks: []mergeGateCheck{
			{Code: gateCROpen, OK: true, Message: "opened"},
			{Code: gateCIPass, OK: true, Message: "success"},
		},
	}
	out := toIDLGate(g)
	if !out.Eligible || out.HeadSha != "h1" || out.CiStatus != "success" {
		t.Fatalf("mapped: %+v", out)
	}
	if len(out.Checks) != 2 || out.Checks[0].Code != gateCROpen || out.Checks[0].Message != "opened" {
		t.Fatalf("checks: %+v", out.Checks)
	}
	var _ string = out.Checks[0].Message
	var _ *argus.MergeGateCheck = out.Checks[0]
}
