// service_taskapi_read.go —— 用量/报告读取面（自 service_taskapi.go 拆出）。

package service

import (
	"context"
	"strings"

	argus "github.com/yi-nology/argus/biz/model/argus"
	"github.com/yi-nology/argus/internal/storage"
)

// UsageMonthly 本月各仓库 token 用量（GET /usage/monthly；🔒，v3.1）。
// v6.7.0 P0-02：token 带仓库约束时约束外仓库的行不透出（跨仓用量元数据）。
// #32 验收⑤：三口径并列——total=配额账（done 任务行）、real_total=实销
// （usage_event 逐调用）、reserved_total=活跃预留（执行前已承诺未花出）。
func (s *Service) UsageMonthly(ctx context.Context) (map[string]any, int) {
	return usageMonthly(s, ctx)
}

// GetReportByRef 按 head SHA 查最近一次成功报告（GET /reports/:ref；🔒，v3.1）。
// v6.7.0 P0-02：解析出的任务必须过 AuthorizeTask——head SHA 是全局寻址键，
// 此前无仓库约束判定，持任意 read 档 token 可按 SHA 跨仓读取完整审查报告。
// v6.12 P1：评估实锤「同 SHA 多仓（fork/镜像）碰撞」——token 带仓库约束时
// 先按约束精确寻址（查库阶段限定平台/仓），查不到再全局取列表逐条过授权
// 回退（第一条通过者返回）；无约束 token 保持旧形态。
func (s *Service) GetReportByRef(ctx context.Context, ref string) (*argus.GetTaskResponse, int) {
	return getReportByRef(s, ctx, ref)
}

// reportResponse GetReportByRef 响应构造（单源，两处共用）。
func reportResponse(task *storage.Task) *argus.GetTaskResponse {
	return &argus.GetTaskResponse{
		TaskId:  task.ID,
		Status:  task.Status,
		Verdict: strPtr(task.Verdict),
		Report:  strPtr(task.Report),
	}
}

// parseExactRepoPattern 把 token 仓库约束的精确条目 "platform:owner/repo"
// 解析成三元组；非精确形态（空/含通配 */多条目）返回空串走列表回退。
func parseExactRepoPattern(tp string) (platform, owner, repo string) {
	if tp == "" || strings.Contains(tp, ",") || strings.Contains(tp, "*") {
		return "", "", ""
	}
	rest, ok := strings.CutPrefix(tp, ":")
	if !ok {
		return "", "", ""
	}
	var rest2 string
	platform, rest2, ok = strings.Cut(rest, ":")
	if !ok || platform == "" {
		return "", "", ""
	}
	owner, repo, ok = strings.Cut(rest2, "/")
	if !ok || owner == "" || repo == "" || strings.Contains(repo, "/") {
		return "", "", ""
	}
	return platform, owner, repo
}
