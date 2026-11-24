// store_impl_prints.go —— Store 方法体的包级实现（转发壳见原文件）。

package storage

import (
	"context"

	"github.com/yi-nology/argus/internal/domain"
	"gorm.io/gorm"
)

// getPrintState 的包级实现（转发壳见原文件）。
func getPrintState(s *Store, ctx context.Context, platform, owner, repo, number, fingerprint string) (*domain.PrintState, error) {
	var p FindingPrint
	err := s.db.WithContext(ctx).Where(
		"platform = ? AND owner = ? AND repo = ? AND number = ? AND print = ?",
		platform, owner, repo, number, resolveAliasFP(s.db.WithContext(ctx), platform, owner, repo, number, fingerprint)).First(&p).Error
	if err != nil {
		if err == gorm.ErrRecordNotFound {
			return nil, nil
		}
		return nil, err
	}
	return &domain.PrintState{
		Status:      p.Status,
		Line:        p.Line,
		Severity:    p.Severity,
		TimesSeen:   p.TimesSeen,
		RegretCount: p.RegretCount,
		Comment:     p.Comment, // 入库原文（标注前）——pattern regret 记账键的权威来源
	}, nil
}

// resolvePrintFingerprint 的包级实现（转发壳见原文件）。
func resolvePrintFingerprint(s *Store, ctx context.Context, platform, owner, repo, number, prefix string) (string, bool) {
	p, ok := s.ResolvePrintRecord(ctx, platform, owner, repo, number, prefix)
	if !ok {
		return "", false
	}
	return p.Print, true
}

// resolvePrintWithPlugin 的包级实现（转发壳见原文件）。
func resolvePrintWithPlugin(s *Store, ctx context.Context, platform, owner, repo, number, prefix string) (fp, pluginName string, ok bool) {
	p, ok := s.ResolvePrintRecord(ctx, platform, owner, repo, number, prefix)
	if !ok {
		return "", "", false
	}
	return p.Print, p.Plugin, true
}

// resolvePrintRecord 的包级实现（转发壳见原文件）。
func resolvePrintRecord(s *Store, ctx context.Context, platform, owner, repo, number, prefix string) (FindingPrint, bool) {
	if !validFPPrefix(prefix) {
		return FindingPrint{}, false
	}
	var rows []FindingPrint
	if err := s.db.WithContext(ctx).
		Where("platform = ? AND owner = ? AND repo = ? AND number = ? AND print LIKE ?",
			platform, owner, repo, number, prefix+"%").
		Order("last_seen_at DESC").Find(&rows).Error; err != nil {
		return FindingPrint{}, false
	}
	if len(rows) == 1 {
		return rows[0], true
	}
	if len(rows) > 1 {
		return FindingPrint{}, false // 前缀歧义：不猜
	}
	// 别名前缀兜底：报告意见行展示的可能是新措辞指纹（别名），短码解析需映射
	// 回规范行。别名表已 PR 域化（v3.9.2），直接按本 PR 过滤。
	var a PrintAlias
	if err := s.db.WithContext(ctx).Where(
		"platform = ? AND owner = ? AND repo = ? AND number = ? AND alias_fp LIKE ?",
		platform, owner, repo, number, prefix+"%").
		Order("updated_at DESC").First(&a).Error; err != nil {
		return FindingPrint{}, false
	}
	var p FindingPrint
	if err := s.db.WithContext(ctx).Where(
		"platform = ? AND owner = ? AND repo = ? AND number = ? AND print = ?",
		platform, owner, repo, number, a.PrintFP).First(&p).Error; err != nil {
		return FindingPrint{}, false
	}
	return p, true
}

// listFindingPrints 的包级实现（转发壳见原文件）。
func listFindingPrints(s *Store, ctx context.Context, platform, owner, repo, number string) ([]FindingPrint, error) {
	var out []FindingPrint
	err := wherePR(s.db.WithContext(ctx), platform, owner, repo, number).Order("last_seen_at DESC").Find(&out).Error
	return out, err
}

// listPrintsByRepo 的包级实现（转发壳见原文件，v8.5.0 意见治理台）。
func listPrintsByRepo(s *Store, ctx context.Context, platform, owner, repo, status string, limit int) ([]FindingPrint, error) {
	q := s.quietDB().WithContext(ctx).Model(&FindingPrint{}).
		Where("platform = ? AND owner = ? AND repo = ?", platform, owner, repo)
	if status != "" {
		q = q.Where("status = ?", status)
	}
	var out []FindingPrint
	err := q.Order("last_seen_at DESC").Limit(limit).Find(&out).Error
	return out, err
}

// printRepoSummary 的包级实现（转发壳见原文件，v8.5.0 意见治理台计数）。
func printRepoSummary(s *Store, ctx context.Context, platform, owner, repo string) (PrintRepoSummary, error) {
	var row struct {
		Total      int64 `gorm:"column:total"`
		OpenCount  int64 `gorm:"column:open_count"`
		Suppressed int64 `gorm:"column:suppressed_count"`
	}
	err := s.quietDB().WithContext(ctx).Model(&FindingPrint{}).
		Select("COUNT(*) AS total, "+
			"COALESCE(SUM(CASE WHEN status = 'open' THEN 1 ELSE 0 END),0) AS open_count, "+
			"COALESCE(SUM(CASE WHEN status = 'suppressed' THEN 1 ELSE 0 END),0) AS suppressed_count").
		Where("platform = ? AND owner = ? AND repo = ?", platform, owner, repo).
		Scan(&row).Error
	if err != nil {
		return PrintRepoSummary{}, err
	}
	return PrintRepoSummary{Total: row.Total, Open: row.OpenCount, Suppressed: row.Suppressed}, nil
}

// listAllPrints 的包级实现（转发壳见原文件）。
func listAllPrints(s *Store, ctx context.Context) ([]FindingPrint, error) {
	var rows []FindingPrint
	err := s.db.WithContext(ctx).Order("id").Find(&rows).Error
	return rows, err
}

// findingHistoryByFiles 的包级实现（转发壳见原文件）。
func findingHistoryByFiles(s *Store, ctx context.Context, platform, owner, repo string,
	files []string) (map[string]FindingFileHistory, error) {
	out := map[string]FindingFileHistory{}
	if len(files) == 0 {
		return out, nil
	}
	var rows []struct {
		File       string `gorm:"column:file"`
		Total      int    `gorm:"column:total"`
		Suppressed int    `gorm:"column:suppressed"`
	}
	err := s.quietDB().WithContext(ctx).
		Table("finding_prints").
		Select("file, COUNT(*) AS total, COALESCE(SUM(CASE WHEN status = 'suppressed' THEN 1 ELSE 0 END), 0) AS suppressed").
		Where("platform = ? AND owner = ? AND repo = ? AND file IN ?", platform, owner, repo, files).
		Group("file").
		Scan(&rows).Error
	if err != nil {
		return nil, err
	}
	for _, r := range rows {
		out[r.File] = FindingFileHistory{Total: r.Total, Suppressed: r.Suppressed}
	}
	return out, nil
}

// searchFindingsHistory 的包级实现（转发壳见原文件）。
func searchFindingsHistory(s *Store, ctx context.Context, platform, owner, repo, query string, limit int) ([]FindingHistoryItem, error) {
	if limit <= 0 || limit > 50 {
		limit = 10
	}
	q := s.db.WithContext(ctx).Where("platform = ? AND owner = ? AND repo = ?", platform, owner, repo)
	if query != "" {
		like := "%" + query + "%"
		q = q.Where("comment LIKE ? OR file LIKE ?", like, like)
	}
	var rows []FindingPrint
	if err := q.Order("last_seen_at DESC").Limit(limit).Find(&rows).Error; err != nil {
		return nil, err
	}
	out := make([]FindingHistoryItem, 0, len(rows))
	for i := range rows {
		out = append(out, FindingHistoryItem{
			FP: rows[i].Print, File: rows[i].File, Line: rows[i].Line,
			Severity: rows[i].Severity, Comment: rows[i].Comment, Status: rows[i].Status,
			TimesSeen: rows[i].TimesSeen, LastSeen: rows[i].LastSeenAt,
		})
	}
	return out, nil
}

// listSuppressedPrints 的包级实现（转发壳见原文件）。
func listSuppressedPrints(s *Store, ctx context.Context, limit int) ([]FindingPrint, error) {
	q := s.db.WithContext(ctx).
		Where("status = ?", PrintSuppressed).
		Order("last_seen_at DESC")
	if limit > 0 {
		q = q.Limit(limit)
	}
	var out []FindingPrint
	err := q.Find(&out).Error
	return out, err
}

// countFixStatuses 的包级实现（转发壳见原文件）。
func countFixStatuses(s *Store, ctx context.Context) ([]FixStatusCount, error) {
	var out []FixStatusCount
	err := s.db.WithContext(ctx).
		Model(&Task{}).
		Select("fix_status, COUNT(*) AS n").
		Where("fix_status <> ''").
		Group("fix_status").
		Find(&out).Error
	return out, err
}
