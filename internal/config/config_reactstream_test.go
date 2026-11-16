package config

import "testing"

// ARGUS_REACT_STREAM 表驱动：未设/0/1/true/非法值（非法回退默认 false）。
func TestReActStreamEnv(t *testing.T) {
	cases := []struct {
		name string
		val  string
		want bool
	}{
		{"未设", "", false},
		{"0", "0", false},
		{"1", "1", true},
		{"true", "true", true},
		{"非法", "maybe", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("ARGUS_REACT_STREAM", tc.val)
			if got := Load().ReActStream; got != tc.want {
				t.Fatalf("ARGUS_REACT_STREAM=%q -> %v, want %v", tc.val, got, tc.want)
			}
		})
	}
}
