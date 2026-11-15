// service_taskapi_read.go —— 用量/报告读取面（自 service_taskapi.go 拆出）。

package service

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	argus "github.com/yi-nology/argus/biz/model/argus"
	"github.com/yi-nology/argus/internal/storage"
)

// UsageMonthly 本月各仓库 token 用量（GET /usage/monthly；🔒，v3.1）。
// v6.7.0 P0-02：token 带仓库约束时约束外仓库的行不透出（跨仓用量元数据）。
// #32 验收⑤：三口径并列——total=配额账（done 任务行）、real_total=实销
// （usage_event 逐调用）、reserved_total=活跃预留（执行前已承诺未花出）。
func (s *Service) UsageMonthly(ctx context.Context) (map[string]any, int) {
	since := time.Date(time.Now().Year(), time.Now().Month(), 1, 0, 0, 0, 0, time.Now().Location())
	usage, err := s.Store.MonthlyTokensByRepo(ctx, since)
	if err != nil {
		return nil, http.StatusInternalServerError
	}
	filter := s.AuthorizeRowFilter(ctx)
	if filter != nil {
		authzDeniedMetric(ctx, "/usage/monthly") // 行过滤发生（约束外用量不可见）
	}
	items := make([]storage.RepoTokenUsage, 0, len(usage))
	var total int64
	for _, u := range usage {
		if filter != nil && !filter(u.Platform, u.Owner, u.Repo) {
			continue
		}
		items = append(items, u)
		total += u.Tokens
	}
	// 双账本（v6.9.0 P1-06，#11 → #32 逐调用升级）：quota=done 口径（配额判定
	// 与报表，平台故障误烧不占额）+ real=usage_event 逐调用实销（含运行中/失败/
	// 重试的每次调用）。#32 验收⑤：reserved=活跃预留（执行前已承诺、尚未花出），
	// 与实销并列透出——「预留 vs 实销」的在途消耗可见面。
	var realTotal int64
	realItems := make([]storage.RepoTokenUsage, 0, len(usage))
	if usageReal, err := s.Store.MonthlyTokensByRepoReal(ctx, since); err == nil {
		for _, u := range usageReal {
			if filter != nil && !filter(u.Platform, u.Owner, u.Repo) {
				continue
			}
			realItems = append(realItems, u)
			realTotal += u.Tokens
		}
	} else {
		s.log().Warn("argus.api.usage_real_failed", "error", err.Error())
	}
	var reservedTotal int64
	reservedItems := make([]storage.RepoTokenUsage, 0)
	if reserved, err := s.Store.MonthlyReservedByRepo(ctx); err == nil {
		for _, u := range reserved {
			if filter != nil && !filter(u.Platform, u.Owner, u.Repo) {
				continue
			}
			reservedItems = append(reservedItems, u)
			reservedTotal += u.Tokens
		}
	} else {
		s.log().Warn("argus.api.usage_reserved_failed", "error", err.Error())
	}
	return map[string]any{
		"month":          since.Format("2006-01"),
		"total":          total,
		"budget":         s.Cfg.RepoMonthlyTokenBudget,
		"repos":          items,
		"real_total":     realTotal,
		"real_budget":    s.Cfg.RealTokenBudget,
		"real_repos":     realItems,
		"reserved_total": reservedTotal,
		"reserved_repos": reservedItems,
	}, http.StatusOK
}

// GetReportByRef 按 head SHA 查最近一次成功报告（GET /reports/:ref；🔒，v3.1）。
// v6.7.0 P0-02：解析出的任务必须过 AuthorizeTask——head SHA 是全局寻址键，
// 此前无仓库约束判定，持任意 read 档 token 可按 SHA 跨仓读取完整审查报告。
// v6.12 P1：评估实锤「同 SHA 多仓（fork/镜像）碰撞」——token 带仓库约束时
// 先按约束精确寻址（查库阶段限定平台/仓），查不到再全局取列表逐条过授权
// 回退（第一条通过者返回）；无约束 token 保持旧形态。
func (s *Service) GetReportByRef(ctx context.Context, ref string) (*argus.GetTaskResponse, int) {
	if ref == "" {
		return nil, http.StatusBadRequest
	}
	// v6.12 P1：评估实锤「同 SHA 多仓（fork/镜像）碰撞」+ 门禁「跨仓存在性探测
	// 403」语义调和——
	// ① token 带仓库约束：按约束精确寻址（查库阶段限定平台/仓，防碰撞）。
	//    精确条目命中但越权 = 403（e2e 门禁「跨仓 ref 必须 403」语义，防存在性
	//    探测）；未命中 = 404（该 SHA 不在本仓，语义等价「本仓无此报告」——
	//    不暴露全局存在性）。
	// ② 通配/多约束条目（无法精确寻址）：取 SHA 全量列表逐条过授权，第一条
	//    通过的返回——评估语义（合法用户不被 fork 同 SHA 碰撞误 403）。
	if tp := TokenReposFromContext(ctx); tp != "" {
		if platform, owner, repo := parseExactRepoPattern(tp); platform != "" {
			if task, err := s.Store.FindReportByHead(ctx, ref, platform, owner, repo); err == nil {
				if s.AuthorizeTask(ctx, task.Platform, task.Owner, task.Repo) {
					return reportResponse(task), http.StatusOK
				}
				authzDeniedMetric(ctx, "/reports/:ref")
				return nil, http.StatusForbidden
			}
			return nil, http.StatusNotFound
		}
		// 通配/多约束：列表回退，第一条授权通过者返回；全部拒绝则 403——
		// 语义：该 SHA 存在但当前 token 无权访问（与 e2e 门禁「跨仓 ref 403」
		// 一致，防存在性探测泄露），与「本仓无此报告」404 区分。
		tasks, err := s.Store.FindReportsByHead(ctx, ref, 0)
		if err != nil {
			return nil, http.StatusInternalServerError
		}
		for i := range tasks {
			t := &tasks[i]
			if s.AuthorizeTask(ctx, t.Platform, t.Owner, t.Repo) {
				return reportResponse(t), http.StatusOK
			}
		}
		if len(tasks) > 0 {
			authzDeniedMetric(ctx, "/reports/:ref")
			return nil, http.StatusForbidden
		}
		return nil, http.StatusNotFound
	}
	// 无仓库约束：旧语义——全局取最新一条 + 授权
	task, err := s.Store.FindReportByHead(ctx, ref, "", "", "")
	if err != nil {
		if errors.Is(err, storage.ErrNotFound) {
			return nil, http.StatusNotFound
		}
		return nil, http.StatusInternalServerError
	}
	if !s.AuthorizeTask(ctx, task.Platform, task.Owner, task.Repo) {
		authzDeniedMetric(ctx, "/reports/:ref")
		return nil, http.StatusForbidden
	}
	return reportResponse(task), http.StatusOK
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
