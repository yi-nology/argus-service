// config_delta_test.go —— #67 两个 delta 开关的表驱动回归：
// ARGUS_DELTA_FANOUT（默认开）与 ARGUS_DELTA_REASONING（缺省 admin，未知值
// fail-closed 回落）。
package config

import "testing"

func TestDeltaFanoutEnv(t *testing.T) {
	cases := []struct {
		val  string
		want bool
	}{
		{"", true}, // 缺省开
		{"1", true},
		{"true", true},
		{"0", false},
		{"false", false},
		{"not-a-bool", true}, // 非法值回落默认（warn 可见）
	}
	for _, tc := range cases {
		t.Setenv("ARGUS_DELTA_FANOUT", tc.val)
		if got := Load().DeltaFanout; got != tc.want {
			t.Errorf("ARGUS_DELTA_FANOUT=%q DeltaFanout = %v, want %v", tc.val, got, tc.want)
		}
	}
}

func TestDeltaReasoningEnv(t *testing.T) {
	cases := []struct {
		val  string
		want string
	}{
		{"", "admin"}, // 缺省 admin（fail-closed）
		{"admin", "admin"},
		{"ADMIN", "admin"}, // 大小写归一
		{"all", "all"},
		{"off", "off"},
		{"weird", "admin"}, // 未知值回落 admin
		{"  all  ", "all"}, // 空白裁剪
	}
	for _, tc := range cases {
		t.Setenv("ARGUS_DELTA_REASONING", tc.val)
		if got := Load().DeltaReasoning; got != tc.want {
			t.Errorf("ARGUS_DELTA_REASONING=%q DeltaReasoning = %q, want %q", tc.val, got, tc.want)
		}
	}
}
