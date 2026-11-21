// service_fix_approval.go —— 修复审批门的领域判定与票生成（service 侧）。
//
// 生成时机：审查完成（或 fix/list 请求）时按当前 head 的高危意见 Ensure；
// 门控时机：@argus fix / MCP fix_finding / 修复推送环入口——未批准即拒绝
// （fail-closed：缺票 = 未批准）。
package service

import (
	"context"
	"fmt"
	"strings"

	"github.com/yi-nology/argus/internal/storage"
)

// seedsFromPrints 把 finding_prints 行收成审批种子（只留 critical/high）。
func seedsFromPrints(rows []storage.FindingPrint) []storage.FixApprovalSeed {
	out := make([]storage.FixApprovalSeed, 0, len(rows))
	for _, r := range rows {
		if r.Status != storage.PrintOpen {
			continue
		}
		if !storage.IsBlockingSeverity(r.Severity) {
			continue
		}
		out = append(out, storage.FixApprovalSeed{
			Print: r.Print, File: r.File, Severity: r.Severity, Comment: r.Comment,
		})
	}
	return out
}

// ensureFixApprovalsForPR 为 PR 当前 head 的高危意见补建审批票。
// head 空或无高危意见时 no-op（仍会把旧 head pending 标 stale）。返回 pending 数。
func ensureFixApprovalsForPR(s *Service, ctx context.Context, platform, owner, repo, number, headSHA, taskID string) (int64, error) {
	if s.Store == nil || headSHA == "" {
		return 0, nil
	}
	rows, err := s.Store.ListFindingPrints(ctx, platform, owner, repo, number)
	if err != nil {
		return 0, err
	}
	seeds := seedsFromPrints(rows)
	_, pending, err := s.Store.EnsureFixApprovals(ctx, platform, owner, repo, number, headSHA, taskID, seeds)
	return int64(pending), err
}

// currentHeadSHA 取 PR 远端 head（审批门绑定用；失败返回空 = fail-closed）。
func currentHeadSHA(s *Service, ctx context.Context, platform, owner, repo, number string) string {
	if s.SDK == nil {
		return ""
	}
	cr, err := s.SDK.GetCR(ctx, platform, owner, repo, number)
	if err != nil || cr == nil {
		return ""
	}
	return cr.HeadSHA
}

// requireFixApproval 单条修复的审批门：blocking severity 必须有 approved 票。
// 返回 (ok, message)；非阻断级别恒 ok。
func requireFixApproval(s *Service, ctx context.Context, platform, owner, repo, number, headSHA, print, severity string) (bool, string) {
	if !storage.IsBlockingSeverity(severity) {
		return true, ""
	}
	if headSHA == "" {
		return false, "head 未知，无法核验修复审批票（fail-closed）"
	}
	if _, err := ensureFixApprovalsForPR(s, ctx, platform, owner, repo, number, headSHA, ""); err != nil {
		return false, "审批票查询失败: " + err.Error()
	}
	t, err := s.Store.GetFixApprovalByFP(ctx, platform, owner, repo, number, headSHA, print)
	if err != nil {
		return false, "审批票查询失败: " + err.Error()
	}
	if t == nil {
		return false, fmt.Sprintf("高危意见（%s）缺审批票，请等待生成或联系管理员", strings.ToLower(severity))
	}
	switch t.Status {
	case storage.FixApprovalApproved:
		return true, ""
	case storage.FixApprovalRejected:
		return false, "修复已被拒绝（fp:" + shortFP(print) + "）" + optionalFixReason(t.Reason)
	case storage.FixApprovalStale:
		return false, "审批票已陈旧（head 已前进），请基于新 head 重新审查"
	default:
		return false, fmt.Sprintf("高危修复待审批（fp:%s，severity=%s）——请管理员 POST /fix-approvals/decide 批准",
			shortFP(print), t.Severity)
	}
}

// optionalFixReason 拒绝理由后缀。
func optionalFixReason(r string) string {
	if strings.TrimSpace(r) == "" {
		return ""
	}
	return "：" + r
}
