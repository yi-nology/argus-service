// config_otelheaders_test.go —— ARGUS_OTEL_HEADERS 解析（Langfuse Basic auth
// 透传面）：分号分隔多个 header、首个冒号分 k/v；值可含等号/斜杠（base64 形态）。
package config

import "testing"

func TestParseOTelHeaders(t *testing.T) {
	cases := []struct {
		in   string
		want map[string]string
	}{
		{"", nil},
		{"   ", nil},
		{"Authorization: Basic cGstbGYteDpza2LGLWZ5LXk=", map[string]string{
			"Authorization": "Basic cGstbGYteDpza2LGLWZ5LXk="}},
		{"Authorization: Basic abc; X-Custom: v/1", map[string]string{
			"Authorization": "Basic abc", "X-Custom": "v/1"}},
		// 空 pair / 缺冒号段忽略（fail-soft，坏段不阻断好段）
		{"Authorization: Bearer x;;bad-segment", map[string]string{
			"Authorization": "Bearer x"}},
	}
	for _, c := range cases {
		got := ParseOTelHeaders(c.in)
		if len(got) != len(c.want) {
			t.Errorf("ParseOTelHeaders(%q) = %v, want %v", c.in, got, c.want)
			continue
		}
		for k, v := range c.want {
			if got[k] != v {
				t.Errorf("ParseOTelHeaders(%q)[%q] = %q, want %q", c.in, k, got[k], v)
			}
		}
	}
}
