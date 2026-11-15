// prints.go —— 意见指纹读侧：状态查询、短码解析、清单与文件历史聚合。

package storage

import (
	"context"

	"gorm.io/gorm"

	"github.com/yi-nology/argus/internal/domain"
)

// GetPrintState 实现 domain.PrintStore（别名一跳解析：新措辞指纹查到既有条目）。
func (s *Store) GetPrintState(ctx context.Context, platform, owner, repo, number, fingerprint string) (*domain.PrintState, error) {
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

var _ domain.PrintStore = (*Store)(nil)

// ResolvePrintFingerprint 按短码前缀解析完整指纹（取最近更新的一条）。
// found=false 表示该前缀在此 PR 下无匹配。同时返回产出插件名（可为空）。
func (s *Store) ResolvePrintFingerprint(ctx context.Context, platform, owner, repo, number, prefix string) (string, bool) {
	p, ok := s.ResolvePrintRecord(ctx, platform, owner, repo, number, prefix)
	if !ok {
		return "", false
	}
	return p.Print, true
}

// ResolvePrintWithPlugin ResolvePrintFingerprint 的超集：附带插件名供学习环归属。
func (s *Store) ResolvePrintWithPlugin(ctx context.Context, platform, owner, repo, number, prefix string) (fp, pluginName string, ok bool) {
	p, ok := s.ResolvePrintRecord(ctx, platform, owner, repo, number, prefix)
	if !ok {
		return "", "", false
	}
	return p.Print, p.Plugin, true
}

// validFPPrefix 指纹短码前缀校验：4~64 位十六进制（指纹为 sha256 hex 串）。
// 前缀会直接拼进 LIKE——含 %/_ 的输入会变成通配模式越权匹配（v3.9.1 收口）。
func validFPPrefix(p string) bool {
	if len(p) < 4 || len(p) > 64 {
		return false
	}
	for _, c := range p {
		isHex := (c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F')
		if !isHex {
			return false
		}
	}
	return true
}

// ResolvePrintRecord 按短码前缀解析完整 prints 行（v3.8.0）：
// 人工反馈（suppress/restore）派生 pattern 键与回写必须用行内原始
// file/line/severity/comment——此前调用方拿"请求里的评语"（评论路径下是
// 整条评论原文）记账，PatternKey 剥离 hex/指令词后全部塌缩到垃圾键，学习环
// 账本与真实模式永远对不上。别名前缀命中时二跳回规范行。
// v3.9.1：①前缀 hex 白名单（防 LIKE 通配注入）；②本 PR 内多行命中同一前缀
// 时拒绝解析（与 restore 同款"不猜"——错误抑制比不抑制难回滚）；③别名兜底
// 限定到本 PR 的规范行（别名表全局共享，此前取全局最近更新可能落到别的 PR）。
func (s *Store) ResolvePrintRecord(ctx context.Context, platform, owner, repo, number, prefix string) (FindingPrint, bool) {
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

// ListFindingPrints 观测辅助：某 PR 的全部指纹记录（GET 任务结果时可用）。
func (s *Store) ListFindingPrints(ctx context.Context, platform, owner, repo, number string) ([]FindingPrint, error) {
	var out []FindingPrint
	err := wherePR(s.db.WithContext(ctx), platform, owner, repo, number).Order("last_seen_at DESC").Find(&out).Error
	return out, err
}

// ListAllPrints 全量意见指纹（G1c 训练数据导出用；只读，无 PR 过滤）。
func (s *Store) ListAllPrints(ctx context.Context) ([]FindingPrint, error) {
	var rows []FindingPrint
	err := s.db.WithContext(ctx).Order("id").Find(&rows).Error
	return rows, err
}

// FindingFileHistory 单文件的意见历史档案（跨 MR 聚合,v5.8.2 惯犯文件热力）。
type FindingFileHistory struct {
	Total      int `json:"total"`
	Suppressed int `json:"suppressed,omitempty"` // 被人工抑制数(误报倾向参考)
}

// FindingHistoryByFiles 按文件聚合该仓全部 MR 的意见史(total/抑制数)。
// files 为空返回空 map;IN 参数化查询,聚合在 DB 侧完成。
func (s *Store) FindingHistoryByFiles(ctx context.Context, platform, owner, repo string,
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
