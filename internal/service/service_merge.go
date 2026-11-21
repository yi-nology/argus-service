// service_merge.go —— 受控合并 API 门面（T2 合并资格门）。
// 实现见 service_impl_merge.go；本文件只留转发壳（与 controlplane 同构）。
package service

import (
	"context"

	argus "github.com/yi-nology/argus/biz/model/argus"
)

// MergeEligibility 只读资格查询（GET /merges/eligibility，admin 档）。
func (s *Service) MergeEligibility(ctx context.Context, req *argus.MergeEligibilityRequest) (*argus.MergeEligibilityResponse, int) {
	return mergeEligibility(s, ctx, req)
}

// MergePreview 创建合并候选（POST /merges/preview，admin 档）。
func (s *Service) MergePreview(ctx context.Context, req *argus.MergePreviewRequest) (*argus.MergePreviewResponse, int) {
	return mergePreview(s, ctx, req)
}

// MergeConfirm 确认合并（POST /merges/confirm，admin 档）。
func (s *Service) MergeConfirm(ctx context.Context, req *argus.MergeConfirmRequest) (*argus.MergeConfirmResponse, int) {
	return mergeConfirm(s, ctx, req)
}

// MergeGet 查询合并操作（GET /merges/:opId，admin 档）。
func (s *Service) MergeGet(ctx context.Context, opID string) (*argus.MergeGetResponse, int) {
	return mergeGet(s, ctx, opID)
}

// MergeReconcile 读回对账 uncertain 操作（POST /merges/reconcile，admin 档）。
func (s *Service) MergeReconcile(ctx context.Context, req *argus.MergeReconcileRequest) (*argus.MergeReconcileResponse, int) {
	return mergeReconcile(s, ctx, req)
}
