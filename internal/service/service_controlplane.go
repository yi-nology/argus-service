// service_controlplane.go —— 控制面人工介入 API（#48）：unknown 副作用裁决/
// 活跃资源租约/任务终止，5 个端点全部 admin-only。
//
// 设计边界（取 kernel controlplane 的「模式」，不搬类型）：kernel controlplane
// 的 Plane 绑 taskboard/tenant，宿主无法用自己的任务 ID/租户类型；本域 DTO 全部
// 在 idl/controlplane.thrift 定义。service 层只在裁决参数上使用
// sideeffect.Resolution（kernel 契约的裁决入参）——它是存储契约的一部分，
// 不是控制面模型。
package service

import (
	"context"

	argus "github.com/yi-nology/argus/biz/model/argus"
)

// ListUncertainEffects 列出 uncertain 副作用（GET /controlplane/side-effects）。
func (s *Service) ListUncertainEffects(ctx context.Context) (*argus.ControlPlaneListEffectsResponse, int) {
	return listUncertainEffects(s, ctx)
}

// ResolveUncertainEffect 人工裁决 unknown（POST /controlplane/side-effects/resolve）。
func (s *Service) ResolveUncertainEffect(ctx context.Context, req *argus.ControlPlaneResolveRequest) (*argus.ControlPlaneResolveResponse, int) {
	return resolveUncertainEffect(s, ctx, req)
}

// ListLeases 列出活跃 side-effect 资源租约（GET /controlplane/leases）。
func (s *Service) ListLeases(ctx context.Context) (*argus.ControlPlaneListLeasesResponse, int) {
	return listLeases(s, ctx)
}

// ReleaseLease 强制释放资源租约（POST /controlplane/leases/release）。
func (s *Service) ReleaseLease(ctx context.Context, req *argus.ControlPlaneReleaseLeaseRequest) (*argus.ControlPlaneReleaseLeaseResponse, int) {
	return releaseLease(s, ctx, req)
}

// CancelTask 终止任务（POST /controlplane/tasks/cancel）。
func (s *Service) CancelTask(ctx context.Context, req *argus.ControlPlaneCancelTaskRequest) (*argus.ControlPlaneCancelTaskResponse, int) {
	return cancelTask(s, ctx, req)
}
