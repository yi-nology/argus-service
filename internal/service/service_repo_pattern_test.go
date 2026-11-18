package service

import (
	"strings"
	"testing"

	"github.com/yi-nology/argus/internal/storage"
)

// TestTranslateRepoPatterns 翻译器词法钉：与 matchRepoPattern 同语法，
// 异形条目回落（ok=false）。
func TestTranslateRepoPatterns(t *testing.T) {
	cases := []struct {
		in   string
		want []storage.RepoPattern
		ok   bool
	}{
		{"gitea:acme/web", []storage.RepoPattern{{Platform: "gitea", Owner: "acme", Repo: "web"}}, true},
		{"gitlab:obs/*", []storage.RepoPattern{{Platform: "gitlab", Owner: "obs", Prefix: true}}, true},
		{"gitlab:obs/api/*", []storage.RepoPattern{{Platform: "gitlab", Owner: "obs/api", Prefix: true}}, true},
		// 中间星号形态按 guard 词法是精确串（忠实翻译，非前缀）：
		{"gitlab:group/sub/x*", []storage.RepoPattern{{Platform: "gitlab", Owner: "group/sub", Repo: "x*"}}, true},
		{"p:*/x", []storage.RepoPattern{{Platform: "p", Owner: "*", Repo: "x"}}, true},
		{"a:o/r,b:q/*", []storage.RepoPattern{
			{Platform: "a", Owner: "o", Repo: "r"},
			{Platform: "b", Owner: "q", Prefix: true},
		}, true},
		{"no-colon", nil, false},
		{":empty-platform", nil, false},
		{"p:", nil, false},
		{"p:/repo", nil, false},
		{"p:/*", nil, false}, // guard 词法下恒空集——保守回落
	}
	for _, c := range cases {
		got, ok := translateRepoPatterns(c.in)
		if ok != c.ok {
			t.Errorf("translate(%q) ok=%v, want %v", c.in, ok, c.ok)
			continue
		}
		if !c.ok {
			continue
		}
		if len(got) != len(c.want) {
			t.Fatalf("translate(%q) = %+v, want %+v", c.in, got, c.want)
		}
		for i := range got {
			if got[i] != c.want[i] {
				t.Errorf("translate(%q)[%d] = %+v, want %+v", c.in, i, got[i], c.want[i])
			}
		}
	}
}

// TestTranslateMatchesMatchRepoPattern 回环钉：翻译形态与 matchRepoPattern
// 内存判定在样例坐标上逐条等价（#94 防两形态漂移）。
func TestTranslateMatchesMatchRepoPattern(t *testing.T) {
	tp := "gitlab:obs/*,gitea:acme/web,gitlab:group/sub/x*,gitlab:obs/api/*"
	pats, ok := translateRepoPatterns(tp)
	if !ok {
		t.Fatal("样例应可翻译")
	}
	type coord struct{ platform, owner, repo string }
	coords := []coord{
		{"gitlab", "obs", "api"},       // owner/* 直子仓
		{"gitlab", "obs", "sub"},       //
		{"gitlab", "obs/deep", "repo"}, // owner/* 深层组（跨边界）
		{"gitlab", "other", "x"},       // 域外
		{"gitea", "acme", "web"},       // 精确
		{"gitea", "acme", "web2"},      // 精确不应前缀
		{"gitea", "acme2", "web"},      // owner 不应前缀
		{"gitlab", "obs", "api"},       // PATH/* 与 PATH 恰等（owner=obs repo=api）
		{"gitlab", "obs", "deep"},      //
		{"gitlab", "obs/deep", "repo"}, // PATH/* 深层组
		{"gitlab", "obsapi", "x"},      // 前缀边界：obsapi 不以 "obs/" 起也不等于 obs
		{"gitlab", "group/sub", "x*"},  // 中间星号精确串（忠实翻译）
		{"gitlab", "group/sub", "x1"},  // 精确串不前缀——guard 词法 false
	}
	for _, c := range coords {
		target := c.platform + ":" + c.owner + "/" + c.repo
		want := false
		for _, e := range strings.Split(tp, ",") {
			if matchRepoPattern(strings.TrimSpace(e), target) {
				want = true
				break
			}
		}
		// SQL 形态判定：用翻译结构的内存模拟（与 scopedWhere 同逻辑的最小复刻）。
		got := simulateScopedMatch(pats, c.platform, c.owner, c.repo)
		if got != want {
			t.Errorf("坐标 %s: matchRepoPattern=%v scoped=%v（两形态漂移）", target, want, got)
		}
	}
}

// simulateScopedMatch 与 scopedWhere 同逻辑的最小内存复刻（回环用，非生产路径）。
func simulateScopedMatch(pats []storage.RepoPattern, platform, owner, repo string) bool {
	for _, p := range pats {
		if p.Platform != platform {
			continue
		}
		if !p.Prefix {
			if p.Owner == owner && p.Repo == repo {
				return true
			}
			continue
		}
		if p.Owner == "" && p.Repo == "" {
			return true // "p:*"
		}
		if p.Repo == "" {
			// "p:PATH/*"：owner=PATH 全 repo + owner 以 "PATH/" 为前缀。
			if owner == p.Owner || strings.HasPrefix(owner, p.Owner+"/") {
				return true
			}
			continue
		}
		// 精确形态（含字面 "*" 的条目按串相等）。
		if owner == p.Owner && repo == p.Repo {
			return true
		}
	}
	return false
}
