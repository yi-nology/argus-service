package argusctx

import "testing"

func TestCombineCI(t *testing.T) {
	cases := []struct{ a, b, want string }{
		{CIStateSuccess, CIStateSuccess, CIStateSuccess},
		{CIStateSuccess, CIStatePending, CIStatePending},
		{CIStateSuccess, CIStateFailure, CIStateFailure},
		{CIStateSuccess, CIStateUnknown, CIStateUnknown}, // 未知即 fail-closed 阻断档
		{CIStateFailure, CIStateUnknown, CIStateFailure},
		{CIStatePending, CIStateUnknown, CIStatePending},
		{CIStateUnknown, CIStateUnknown, CIStateUnknown},
	}
	for _, c := range cases {
		if got := combineCI(c.a, c.b); got != c.want {
			t.Errorf("combine(%s,%s)=%s want %s", c.a, c.b, got, c.want)
		}
	}
}

func TestEvalCIStatusNoTokenIsUnknown(t *testing.T) {
	// rawTokenFor 未装配 / 空 token → unknown（fail-closed）
	s := &SDKCtx{rawTokenFor: func(string) string { return "" }}
	state, detail := s.evalGitHubCheckRuns(t.Context(), "o", "r", "sha")
	if state != CIStateUnknown {
		t.Fatalf("state=%s want unknown (%s)", state, detail)
	}
}
