// respond_internal_test.go —— reply 错误文案透出（v8.7.2）：服务层
// map{"error": ...}/error 载荷优先于泛化 failMsg，保持既有 fallback。
package argus

import (
	"errors"
	"testing"
)

func TestErrDetail(t *testing.T) {
	cases := []struct {
		name string
		resp any
		want string
	}{
		{"map_error", map[string]any{"error": "platform 未在部署装配"}, "platform 未在部署装配"},
		{"map_no_error", map[string]any{"tickets": []any{}}, "fallback"},
		{"map_error_not_string", map[string]any{"error": 42}, "fallback"},
		{"map_error_empty", map[string]any{"error": ""}, "fallback"},
		{"error_value", errors.New("仓库不在 token 仓库约束范围内"), "仓库不在 token 仓库约束范围内"},
		{"nil_error", error(nil), "fallback"},
		{"nil_resp", nil, "fallback"},
		{"other_type", "oops", "fallback"},
	}
	for _, tc := range cases {
		if got := errDetail(tc.resp, "fallback"); got != tc.want {
			t.Errorf("%s: errDetail = %q, want %q", tc.name, got, tc.want)
		}
	}
}
