// service_repo_pattern.go —— 授权 pattern 的 SQL 翻译（#94）。
//
// matchRepoPattern（service_repo_guard.go）是内存判定的单源词法；本翻译把
// 同语法条目编译为 storage.RepoPattern 供 SQL 前置过滤——两形态由
// TestTranslateRepoPatterns 回环钉逐条等价。翻译失败（异形条目）返回 ok=false，
// 调用方回落内存后置过滤（旧语义，截断页容忍面保留）。
//
// matchRepoPattern 真实词法（读码核定，勿凭直觉扩）：前缀分支仅认末尾 "/*"
//（"p:a/b/x*" 的中间星号形态**不是**前缀——guard 按精确串处理恒 false）；
// 前缀语义 = 目标串 "p:owner/repo" 以 "p:PATH/" 打头——即 owner=PATH 全 repo
// 或 owner 以 "PATH/" 为前缀的深层组。

package service

import (
	"strings"

	"github.com/yi-nology/argus/internal/storage"
)

// translateRepoPatterns 把 TokenReposFromContext 的逗号分隔条目翻译为 SQL
// pattern 清单。条目语法与 matchRepoPattern 逐条对齐；返回 ok=false = 存在
// 不可翻译/保守回落条目（调用方走内存后置过滤，语义不漂移）。
func translateRepoPatterns(tp string) ([]storage.RepoPattern, bool) {
	var out []storage.RepoPattern
	for _, entry := range strings.Split(tp, ",") {
		entry = strings.TrimSpace(entry)
		if entry == "" {
			continue
		}
		platform, rest, ok := strings.Cut(entry, ":")
		if !ok || platform == "" || rest == "" {
			return nil, false
		}
		if strings.HasSuffix(entry, "/*") {
			// 前缀形态 "p:PATH/*"：owner=PATH 全 repo + owner 以 PATH/ 为前缀。
			path := rest[:len(rest)-2] // 去 "/*"
			if path == "" {
				return nil, false // "p:/*" 在 guard 词法下恒空集——保守回落
			}
			if strings.Contains(path, "*") {
				return nil, false
			}
			out = append(out, storage.RepoPattern{Platform: platform, Owner: path, Prefix: true})
			continue
		}
		// 精确形态 "p:o/r"（含字面 "*" 的异形条目也按精确串忠实翻译）。
		// 末 '/' 切分：owner 可含 '/'（GitLab 子组），与目标串
		// platform+":"+owner+"/"+repo 的构造对齐。
		i := strings.LastIndexByte(rest, '/')
		if i <= 0 || i == len(rest)-1 {
			return nil, false
		}
		owner, repo := rest[:i], rest[i+1:]
		out = append(out, storage.RepoPattern{Platform: platform, Owner: owner, Repo: repo})
	}
	return out, true
}
