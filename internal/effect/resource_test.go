// resource_test.go —— ParseResourceRepo（#60 控制面仓库维度判定）：副作用
// 资源身份解析单源。两种在役形态：
//   - pr:<platform>:<owner>/<repo>#<n>（修复 PR 创建）
//   - branch:<platform>:<owner>/<repo>:<branch>（分支推送/租约）
package effect

import "testing"

func TestParseResourceRepo(t *testing.T) {
	cases := []struct {
		resource        string
		platform, owner string
		repo            string
		ok              bool
	}{
		{"pr:gitea:myorg/app#42", "gitea", "myorg", "app", true},
		{"pr:github:acme/pay-gateway#7", "github", "acme", "pay-gateway", true},
		{"branch:gitea:myorg/app:argus/fix/42", "gitea", "myorg", "app", true},
		{"branch:gitlab:acme/sub/repo-x:feature/a", "gitlab", "acme", "sub/repo-x", true},
		// repo 含斜杠（GitLab subgroup）必须完整归入 repo 段
		{"branch:gitea:myorg/team/app:main", "gitea", "myorg", "team/app", true},
		// 非法/未知形态
		{"res-applied", "", "", "", false},
		{"legacy:thing", "", "", "", false},
		{"pr:gitea:myorg#42", "", "", "", false},      // 缺 repo 段
		{"branch:gitea:myorg/app", "", "", "", false}, // 缺分支段
		{"", "", "", "", false},
	}
	for _, c := range cases {
		p, o, r, ok := ParseResourceRepo(c.resource)
		if ok != c.ok || (ok && (p != c.platform || o != c.owner || r != c.repo)) {
			t.Errorf("ParseResourceRepo(%q) = (%q,%q,%q,%v), want (%q,%q,%q,%v)",
				c.resource, p, o, r, ok, c.platform, c.owner, c.repo, c.ok)
		}
	}
}
