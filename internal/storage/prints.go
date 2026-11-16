// prints.go —— 意见指纹读侧：状态查询、短码解析、清单与文件历史聚合。

package storage

import (
	"context"

	"github.com/yi-nology/argus/internal/domain"
)

// GetPrintState 实现 domain.PrintStore（别名一跳解析：新措辞指纹查到既有条目）。
func (s *Store) GetPrintState(ctx context.Context, platform, owner, repo, number, fingerprint string) (*domain.PrintState, error) {
	return getPrintState(s, ctx, platform, owner, repo, number, fingerprint)
}

var _ domain.PrintStore = (*Store)(nil)

// ResolvePrintFingerprint 按短码前缀解析完整指纹（取最近更新的一条）。
// found=false 表示该前缀在此 PR 下无匹配。同时返回产出插件名（可为空）。
func (s *Store) ResolvePrintFingerprint(ctx context.Context, platform, owner, repo, number, prefix string) (string, bool) {
	return resolvePrintFingerprint(s, ctx, platform, owner, repo, number, prefix)
}

// ResolvePrintWithPlugin ResolvePrintFingerprint 的超集：附带插件名供学习环归属。
func (s *Store) ResolvePrintWithPlugin(ctx context.Context, platform, owner, repo, number, prefix string) (fp, pluginName string, ok bool) {
	return resolvePrintWithPlugin(s, ctx, platform, owner, repo, number, prefix)
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
	return resolvePrintRecord(s, ctx, platform, owner, repo, number, prefix)
}

// ListFindingPrints 观测辅助：某 PR 的全部指纹记录（GET 任务结果时可用）。
func (s *Store) ListFindingPrints(ctx context.Context, platform, owner, repo, number string) ([]FindingPrint, error) {
	return listFindingPrints(s, ctx, platform, owner, repo, number)
}

// ListAllPrints 全量意见指纹（G1c 训练数据导出用；只读，无 PR 过滤）。
func (s *Store) ListAllPrints(ctx context.Context) ([]FindingPrint, error) {
	return listAllPrints(s, ctx)
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
	return findingHistoryByFiles(s, ctx, platform, owner, repo, files)
}
