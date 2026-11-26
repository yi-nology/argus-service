// store_task_scoped.go —— 租户/授权前置过滤的 SQL 版列举（#94）。
//
// ListTasksScoped 把授权 pattern 下沉为 WHERE 条件——此前 service 层先
// LIMIT 再内存过滤，scoped token 拿到的是被截断的页（limit 语义在过滤前
// 生效）。谓词形态与 service.matchRepoPattern 逐条等价（包级单测回环钉）：
//   - 精确 "p:o/r"  → platform=? AND owner=? AND repo=?
//   - 前缀 "p:体*"   → 末 '/' 切 ownerPart/repoPrefix，双析取
//     (owner LIKE 'o/rp%' OR (owner='o' AND repo LIKE 'rp%'))——覆盖前缀跨
//     owner/repo 边界的两种归属（owner 本身吃前缀 / owner 恰为 o 而 repo 吃前缀）；
//   - "p:*"（体为空）→ platform=?（该平台全量，与 matchRepoPattern 同语义）。
// LIKE 值统一转义（\ % _）+ ESCAPE '\'（MySQL/PG/SQLite 三方言可移植）。
// 租户谓词由 GORM 回调按 ctx 自动注入，本方法只拼授权维。

package storage

import (
	"context"
	"strings"
)

// RepoPattern 一条授权 pattern 的 SQL 等价形（Prefix=true 时 Repo 为前缀）。
type RepoPattern struct {
	Platform string
	Owner    string
	Repo     string
	Prefix   bool
}

// ListTasksScoped 按授权 pattern 前置过滤的列举（#94：limit 在过滤后生效）。
// pats 为空 = 不限（等价 ListTasks）；status 空 = 全部。
func (s *Store) ListTasksScoped(ctx context.Context, status string, limit int, pats []RepoPattern) ([]Task, error) {
	return listTasksScoped(s, ctx, status, limit, pats)
}

// listTasksScoped 的包级实现（转发壳见原文件）。
func listTasksScoped(s *Store, ctx context.Context, status string, limit int, pats []RepoPattern) ([]Task, error) {
	q := s.db.WithContext(ctx).Model(&Task{})
	if status != "" {
		q = q.Where("status = ?", status)
	}
	if len(pats) > 0 {
		expr, eargs := scopedWhere(pats)
		q = q.Where(expr, eargs...)
	}
	if limit > 0 {
		q = q.Limit(limit)
	}
	var tasks []Task
	if err := q.Order("created_at DESC, id DESC").Find(&tasks).Error; err != nil {
		return nil, err
	}
	return tasks, nil
}

// listTasksScopedFiltered 授权 pattern + 业务过滤（v8.8.2；limit 全谓词后生效）。
func listTasksScopedFiltered(s *Store, ctx context.Context, f TaskListFilter, pats []RepoPattern) ([]Task, error) {
	q := s.db.WithContext(ctx).Model(&Task{}).Select(taskSummaryColumns)
	q = applyTaskListFilter(q, f)
	if len(pats) > 0 {
		expr, eargs := scopedWhere(pats)
		q = q.Where(expr, eargs...)
	}
	q = q.Order("created_at DESC, id DESC").Limit(clampTaskListLimit(f.Limit))
	var tasks []Task
	if err := q.Find(&tasks).Error; err != nil {
		return nil, err
	}
	return tasks, nil
}

// scopedWhere 把 pattern 清单拼为 OR 条件组（单个 pattern 也走 OR 统一形态）。
func scopedWhere(pats []RepoPattern) (string, []any) {
	var conds []string
	var args []any
	for _, p := range pats {
		if !p.Prefix {
			conds = append(conds, "(platform = ? AND owner = ? AND repo = ?)")
			args = append(args, p.Platform, p.Owner, p.Repo)
			continue
		}
		if p.Repo == "" && p.Owner == "" {
			// "p:*" 形态：该平台全量。
			conds = append(conds, "(platform = ?)")
			args = append(args, p.Platform)
			continue
		}
		if p.Repo == "" {
			// "p:o/*" 形态：owner=o 全 repo + owner 以 "o/" 为前缀的深层组。
			conds = append(conds, "(platform = ? AND (owner = ? OR owner LIKE ? ESCAPE '\\'))")
			args = append(args, p.Platform, p.Owner, likeEscape(p.Owner)+"/%")
			continue
		}
		joined := p.Owner + "/" + p.Repo
		conds = append(conds, "(platform = ? AND (owner LIKE ? ESCAPE '\\' OR (owner = ? AND repo LIKE ? ESCAPE '\\')))")
		args = append(args, p.Platform, likeEscape(joined)+"%", p.Owner, likeEscape(p.Repo)+"%")
	}
	return strings.Join(conds, " OR "), args
}

// likeEscape LIKE 模式值转义（\ % _），配 ESCAPE '\' 使用。
func likeEscape(s string) string {
	r := strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`)
	return r.Replace(s)
}
