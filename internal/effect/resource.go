// resource.go —— 副作用资源身份解析（#60 控制面仓库维度判定的单源）：
// 控制面（副作用裁决/租约强释）需要从资源串还原仓库三元组做 repo_admin
// 授权；解析失败 = 资源不可归属任何仓库 = 仅 system_admin 可见。
package effect

import "strings"

// ParseResourceRepo 从副作用资源串解析仓库三元组。在役形态（编码器在
// internal/runner 的 pushEffectResource/crEffectResource，回环测试互钉）：
//
//	pr:<platform>:<owner>/<repo>#<n>            （修复 PR 创建资源）
//	branch:<platform>:<owner>/<repo>:<branch>    （分支推送/租约资源）
//
// 定界安全性：git refname 不允许出现 ':'，PR 号段以 '#' 起——branch 形态以
// **末个** ':' 定界（owner/repo 可含 '/'，GitLab subgroup），pr 形态先截首个
// '#'。ok=false = 不可解析（未知前缀/缺段）：控制面按「仅 system_admin 可见」
// 处理，fail-closed。
func ParseResourceRepo(resource string) (platform, owner, repo string, ok bool) {
	switch {
	case strings.HasPrefix(resource, "pr:"):
		rest := strings.TrimPrefix(resource, "pr:")
		if i := strings.IndexByte(rest, '#'); i >= 0 {
			rest = rest[:i]
		}
		return splitScopedRepo(rest)
	case strings.HasPrefix(resource, "branch:"):
		rest := strings.TrimPrefix(resource, "branch:")
		i := strings.LastIndexByte(rest, ':')
		if i < 0 {
			return "", "", "", false
		}
		return splitScopedRepo(rest[:i])
	default:
		return "", "", "", false
	}
}

// splitScopedRepo "<platform>:<owner>/<repo>" → 三元组；owner 与 repo 均须
// 非空，repo 可含 '/'（subgroup 归 repo 段）。
func splitScopedRepo(s string) (string, string, string, bool) {
	c := strings.IndexByte(s, ':')
	if c <= 0 {
		return "", "", "", false
	}
	platform, rest := s[:c], s[c+1:]
	slash := strings.IndexByte(rest, '/')
	if slash <= 0 || slash == len(rest)-1 {
		return "", "", "", false
	}
	return platform, rest[:slash], rest[slash+1:], true
}
